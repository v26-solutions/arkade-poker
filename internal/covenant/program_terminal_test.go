package covenant

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

func terminalParams() Params {
	p := Params{Stake: 100, Bond: 50, MinBet: 10, MaxWager: 500}
	for i, target := range []*[32]byte{&p.Players.Player1.SigningKey, &p.ArkSigningKey, &p.EmulatorSigningKey, &p.Players.Player2.SigningKey} {
		_, key := btcec.PrivKeyFromBytes([]byte{byte(i + 1)})
		copy(target[:], schnorr.SerializePubKey(key))
	}
	p.Players.Player1.PayoutScript = append([]byte{txscript.OP_1, 32}, p.Players.Player1.SigningKey[:]...)
	// Generic caller-selected payout scripts must also be bound exactly.
	p.Players.Player2.PayoutScript = []byte{txscript.OP_TRUE}
	return p
}

type executionFixture struct {
	bundle   *Unsigned
	source   Source
	emulator *btcec.PublicKey
}

func terminalFixture(t *testing.T, params Params, program []byte, actor Player, header []byte, payouts []*wire.TxOut, successor *State) executionFixture {
	t.Helper()
	var amount int64
	for _, out := range payouts {
		amount += out.Value
	}
	source, server, checkpoint := sourceFixture(t, amount, 0)
	outer, _, err := outerLeaf(params, actor, program)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := script.DecodeClosure(outer)
	if err != nil {
		t.Fatal(err)
	}
	other, err := script.DecodeClosure(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	tree := &script.TapscriptsVtxoScript{Closures: []script.Closure{other, closure}}
	key, proofs, err := tree.TapTree()
	if err != nil {
		t.Fatal(err)
	}
	source.Vtxo.RevealedTapscripts, err = tree.Encode()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := proofs.GetTaprootMerkleProof(txscript.NewBaseTapLeaf(outer).TapHash())
	if err != nil {
		t.Fatal(err)
	}
	source.Vtxo.Tapscript.ControlBlock, err = txscript.ParseControlBlock(proof.ControlBlock)
	if err != nil {
		t.Fatal(err)
	}
	source.Vtxo.Tapscript.RevealedScript = proof.Script
	source.PreviousTx.TxOut[0].PkScript, err = script.P2TRScript(key)
	if err != nil {
		t.Fatal(err)
	}
	before, err := extension.NewExtensionFromPackets(extension.UnknownPacket{PacketType: typeState, Data: header})
	if err != nil {
		t.Fatal(err)
	}
	beforeOut, err := before.TxOut()
	if err != nil {
		t.Fatal(err)
	}
	source.PreviousTx.AddTxOut(beforeOut)
	source.Vtxo.Outpoint.Hash = source.PreviousTx.TxHash()
	entry := arkade.EmulatorEntry{Vin: 0, Script: program}
	after, err := ActionExtension(successor, entry)
	if err != nil {
		t.Fatal(err)
	}
	afterOut, err := after.TxOut()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := buildUnsigned([]Source{source}, append(payouts, afterOut), checkpoint, server, nil)
	if err != nil {
		t.Fatal(err)
	}
	emulator, err := schnorr.ParsePubKey(params.EmulatorSigningKey[:])
	if err != nil {
		t.Fatal(err)
	}
	return executionFixture{bundle, source, emulator}
}

// This fetcher supplies authenticated fixture data through the exact interface
// consumed by the actual emulator. It contains no poker decision logic.
type fixtureFetcher struct {
	txscript.PrevOutputFetcher
	previous map[wire.OutPoint]*wire.MsgTx
	indices  map[wire.OutPoint]uint32
}

func (f fixtureFetcher) FetchPrevOutArkTx(op wire.OutPoint) *wire.MsgTx { return f.previous[op] }
func (f fixtureFetcher) FetchVtxoPrevOutPkScript(op wire.OutPoint) []byte {
	tx := f.previous[op]
	if tx == nil {
		return nil
	}
	return tx.TxOut[f.indices[op]].PkScript
}

func executeFixture(f executionFixture) error {
	main := f.bundle.Ark
	base := txscript.NewMultiPrevOutFetcher(nil)
	fetcher := fixtureFetcher{base, map[wire.OutPoint]*wire.MsgTx{}, map[wire.OutPoint]uint32{}}
	for i, in := range main.UnsignedTx.TxIn {
		cp := f.bundle.Checkpoints[i]
		if err := checkBuiltLink(main, cp, i, f.source); err != nil {
			return err
		}
		prev, err := txutils.GetArkPsbtFields(main, i, arkade.PrevArkTxField)
		if err != nil {
			return err
		}
		if len(prev) != 1 {
			return errors.New("fixture missing logical source")
		}
		base.AddPrevOut(in.PreviousOutPoint, main.Inputs[i].WitnessUtxo)
		fetcher.previous[in.PreviousOutPoint] = &prev[0]
		fetcher.indices[in.PreviousOutPoint] = cp.UnsignedTx.TxIn[0].PreviousOutPoint.Index
	}
	entries, err := arkade.FindEmulatorPacket(main.UnsignedTx)
	if err != nil {
		return err
	}
	if len(entries) != 1 {
		return errors.New("fixture missing emulator entry")
	}
	leaf := main.Inputs[0].TaprootLeafScript[0]
	if err := arkade.VerifyTaprootLeafCommitment(main.Inputs[0].WitnessUtxo.PkScript, leaf); err != nil {
		return err
	}
	program, err := arkade.ReadArkadeScript(main, f.emulator, entries[0])
	if err != nil {
		return err
	}
	return program.Execute(main.UnsignedTx, fetcher, 0)
}

func terminalHeader(phase byte, deadline uint64) []byte {
	b := make([]byte, 60)
	b[0] = 1
	b[34] = phase
	b[35] = 255
	// Invalid wager/action history is intentionally irrelevant to terminal exits.
	binary.LittleEndian.PutUint64(b[36:], math.MaxUint64)
	binary.LittleEndian.PutUint64(b[44:], math.MaxUint64)
	binary.LittleEndian.PutUint64(b[52:], deadline)
	return b
}

func concessionPayouts(params Params, actor Player) []*wire.TxOut {
	values := [2]int64{1150, 1150}
	values[actor-Player1] = params.Bond
	return []*wire.TxOut{wire.NewTxOut(values[0], bytes.Clone(params.Players.Player1.PayoutScript)), wire.NewTxOut(values[1], bytes.Clone(params.Players.Player2.PayoutScript))}
}

func TestTerminalProgramsRealEmulatorPhaseAdmission(t *testing.T) {
	params := terminalParams()
	for _, actor := range []Player{Player1, Player2} {
		concession, err := emitConcession(params, ContractID{}, actor)
		if err != nil {
			t.Fatal(err)
		}
		timeout, err := emitTimeout(params, ContractID{}, actor)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 256 {
			phase, phaseErr := DecodePhase(byte(i))
			due, _ := phase.RequiredActor()
			// Explicit covenant behavior: folds/mucks include all-in reveal obligations,
			// but ordinary board reveal and the two opening phases cannot concede.
			canConcede := phaseErr == nil && due == actor && (phase.Kind == Betting || phase.Kind == ShowdownReveal || phase.Kind == AllIn || phase.Kind == AllInReveal)
			f := terminalFixture(t, params, concession, actor, terminalHeader(byte(i), math.MaxUint64), concessionPayouts(params, actor), nil)
			err := executeFixture(f)
			if (err == nil) != canConcede {
				t.Fatalf("concession actor %d phase %02x expected %t: %v", actor, i, canConcede, err)
			}
			if err != nil {
				var e txscript.Error
				if !errors.As(err, &e) || e.ErrorCode != txscript.ErrVerify {
					t.Fatalf("concession rejected for unrelated reason: %v", err)
				}
			}
			destination := params.Players.Player1.PayoutScript
			if actor == Player2 {
				destination = params.Players.Player2.PayoutScript
			}
			f = terminalFixture(t, params, timeout, actor, terminalHeader(byte(i), 1), []*wire.TxOut{wire.NewTxOut(1200, destination)}, nil)
			canTimeout := phaseErr == nil && due != 0 && due != actor
			err = executeFixture(f)
			if (err == nil) != canTimeout {
				t.Fatalf("timeout beneficiary %d phase %02x expected %t: %v", actor, i, canTimeout, err)
			}
			if err != nil {
				var e txscript.Error
				if !errors.As(err, &e) || e.ErrorCode != txscript.ErrVerify {
					t.Fatalf("timeout rejected for unrelated reason: %v", err)
				}
			}
		}
	}
}

func TestTerminalProgramsRejectTampering(t *testing.T) {
	params := terminalParams()
	concession, err := emitConcession(params, ContractID{}, Player1)
	if err != nil {
		t.Fatal(err)
	}
	makeFixture := func() executionFixture {
		return terminalFixture(t, params, concession, Player1, terminalHeader(2, math.MaxUint64), concessionPayouts(params, Player1), nil)
	}
	// All mutations start from a fully assembled, passing PSBT bundle.
	baseline := makeFixture()
	if err := executeFixture(baseline); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*executionFixture){
		"actor bond": func(f *executionFixture) {
			f.bundle.Ark.UnsignedTx.TxOut[0].Value++
			f.bundle.Ark.UnsignedTx.TxOut[1].Value--
		},
		"opponent amount":      func(f *executionFixture) { f.bundle.Ark.UnsignedTx.TxOut[1].Value-- },
		"actor destination":    func(f *executionFixture) { f.bundle.Ark.UnsignedTx.TxOut[0].PkScript[3] ^= 1 },
		"opponent destination": func(f *executionFixture) { f.bundle.Ark.UnsignedTx.TxOut[1].PkScript = []byte{txscript.OP_FALSE} },
		"output order":         func(f *executionFixture) { o := f.bundle.Ark.UnsignedTx.TxOut; o[0], o[1] = o[1], o[0] },
	} {
		t.Run(name, func(t *testing.T) {
			f := makeFixture()
			edit(&f)
			err := executeFixture(f)
			var e txscript.Error
			if !errors.As(err, &e) || (e.ErrorCode != txscript.ErrNumEqualVerify && e.ErrorCode != txscript.ErrEqualVerify) {
				t.Fatalf("wrong failure: %v", err)
			}
		})
	}
	for _, kind := range []byte{typeState, typeOpening, typePlayer1, typePlayer2} {
		f := makeFixture()
		entries, _ := arkade.FindEmulatorPacket(f.bundle.Ark.UnsignedTx)
		packet, _ := arkade.NewPacket(entries...)
		ext, _ := extension.NewExtensionFromPackets(packet, extension.UnknownPacket{PacketType: kind, Data: []byte{1}})
		out, _ := ext.TxOut()
		f.bundle.Ark.UnsignedTx.TxOut[2] = out
		err := executeFixture(f)
		var e txscript.Error
		if !errors.As(err, &e) || e.ErrorCode != txscript.ErrVerify {
			t.Fatalf("successor %x: %v", kind, err)
		}
	}
	// The header's ID, version and length are authenticated even on an exit.
	for _, mutate := range []func([]byte) []byte{
		func(h []byte) []byte { h[2] = 1; return h }, func(h []byte) []byte { h[0] = 2; return h }, func(h []byte) []byte { return h[:59] },
	} {
		f := terminalFixture(t, params, concession, Player1, mutate(terminalHeader(2, 1)), concessionPayouts(params, Player1), nil)
		err := executeFixture(f)
		var e txscript.Error
		if !errors.As(err, &e) || (e.ErrorCode != txscript.ErrNumEqualVerify && e.ErrorCode != txscript.ErrEqualVerify) {
			t.Fatalf("header tamper: %v", err)
		}
	}
	timeout, err := emitTimeout(params, ContractID{}, Player2)
	if err != nil {
		t.Fatal(err)
	}
	f := terminalFixture(t, params, timeout, Player2, terminalHeader(0, math.MaxUint64), []*wire.TxOut{wire.NewTxOut(150, params.Players.Player2.PayoutScript)}, nil)
	err = executeFixture(f)
	var e txscript.Error
	if !errors.As(err, &e) || e.ErrorCode != txscript.ErrUnsatisfiedLockTime {
		t.Fatalf("unsigned deadline bypass: %v", err)
	}
	// Program substitution is rejected by upstream key-tweak admission before VM execution.
	entries, _ := arkade.FindEmulatorPacket(baseline.bundle.Ark.UnsignedTx)
	entries[0].Script = []byte{txscript.OP_TRUE}
	if _, err := arkade.ReadArkadeScript(baseline.bundle.Ark, baseline.emulator, entries[0]); !errors.Is(err, arkade.ErrTweakedArkadePubKeyNotFound) {
		t.Fatalf("program substitution: %v", err)
	}
}

func TestTerminalProgramsPayoutScripts(t *testing.T) {
	// Bind witness versions/programs and hash arbitrary exact caller scripts.
	scripts := [][]byte{{}, {txscript.OP_TRUE}, {txscript.OP_RETURN}, append([]byte{txscript.OP_0, 20}, make([]byte, 20)...), append([]byte{txscript.OP_16, 2}, 1, 2)}
	for _, destination := range scripts {
		params := terminalParams()
		params.Players.Player1.PayoutScript = destination
		params.Players.Player2.PayoutScript = destination
		program, err := emitConcession(params, ContractID{}, Player1)
		if err != nil {
			t.Fatal(err)
		}
		f := terminalFixture(t, params, program, Player1, terminalHeader(2, 1), concessionPayouts(params, Player1), nil)
		if err := executeFixture(f); err != nil {
			t.Fatalf("destination %x: %v", destination, err)
		}
	}
}

// Ensure all fixtures remain actual serializable PSBTs after adding programs.
func TestTerminalFixturePSBTEncoding(t *testing.T) {
	params := terminalParams()
	program, _ := emitConcession(params, ContractID{}, Player2)
	f := terminalFixture(t, params, program, Player2, terminalHeader(3, 1), concessionPayouts(params, Player2), nil)
	for _, p := range append([]*psbt.Packet{f.bundle.Ark}, f.bundle.Checkpoints...) {
		var b bytes.Buffer
		if err := p.Serialize(&b); err != nil {
			t.Fatal(err)
		}
		if _, err := psbt.NewFromRawBytes(&b, false); err != nil {
			t.Fatal(err)
		}
	}
}
