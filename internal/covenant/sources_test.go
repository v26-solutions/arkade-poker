package covenant

import (
	"bytes"
	"encoding/hex"
	"math"
	"strings"
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

func sourceFixture(t *testing.T, amount int64, nonce byte) (Source, [32]byte, []byte) {
	t.Helper()
	// Public test keys only. Use upstream ordinary receive tree and CSV checkpoint.
	_, owner := btcec.PrivKeyFromBytes([]byte{1})
	_, server := btcec.PrivKeyFromBytes([]byte{2})
	tree := script.NewDefaultVtxoScript(owner, server, arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 144})
	key, proofs, err := tree.TapTree()
	if err != nil {
		t.Fatal(err)
	}
	leaves, err := tree.Encode()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := tree.Closures[1].Script()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := proofs.GetTaprootMerkleProof(txscript.NewBaseTapLeaf(selected).TapHash())
	if err != nil {
		t.Fatal(err)
	}
	cb, err := txscript.ParseControlBlock(proof.ControlBlock)
	if err != nil {
		t.Fatal(err)
	}
	pkScript, err := script.P2TRScript(key)
	if err != nil {
		t.Fatal(err)
	}
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Index: uint32(nonce)}, Sequence: wire.MaxTxInSequenceNum})
	tx.AddTxOut(wire.NewTxOut(amount, pkScript))
	checkpoint := &script.CSVMultisigClosure{MultisigClosure: script.MultisigClosure{PubKeys: []*btcec.PublicKey{server}}, Locktime: arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 72}}
	cp, err := checkpoint.Script()
	if err != nil {
		t.Fatal(err)
	}
	return Source{PreviousTx: tx, Vtxo: offchain.VtxoInput{Outpoint: &wire.OutPoint{Hash: tx.TxHash(), Index: 0}, Amount: amount, Tapscript: &waddrmgr.Tapscript{ControlBlock: cb, RevealedScript: proof.Script}, RevealedTapscripts: leaves}}, [32]byte(schnorr.SerializePubKey(server)), cp
}

func TestSourceAdmission(t *testing.T) {
	cases := map[string]struct {
		edit   func(*Source)
		reason string
	}{
		"no transaction": {func(s *Source) { s.PreviousTx = nil }, "missing source"},
		"no outpoint":    {func(s *Source) { s.Vtxo.Outpoint = nil }, "missing source"},
		"transaction id": {func(s *Source) { s.Vtxo.Outpoint.Hash[0] ^= 1 }, "transaction id mismatch"},
		"index":          {func(s *Source) { s.Vtxo.Outpoint.Index = 1 }, "index out of range"},
		"amount":         {func(s *Source) { s.Vtxo.Amount++ }, "amount disagrees"},
		"negative": {func(s *Source) {
			s.Vtxo.Amount = -1
			s.PreviousTx.TxOut[0].Value = -1
			s.Vtxo.Outpoint.Hash = s.PreviousTx.TxHash()
		}, "outside [0, MAX_MONEY]"},
		"maximum": {func(s *Source) {
			s.Vtxo.Amount = maxMoney + 1
			s.PreviousTx.TxOut[0].Value = maxMoney + 1
			s.Vtxo.Outpoint.Hash = s.PreviousTx.TxHash()
		}, "outside [0, MAX_MONEY]"},
		"nil input":    {func(s *Source) { s.PreviousTx.TxIn[0] = nil }, "nil source transaction input"},
		"nil output":   {func(s *Source) { s.PreviousTx.TxOut[0] = nil }, "nil source transaction output"},
		"no path":      {func(s *Source) { s.Vtxo.Tapscript = nil }, "missing selected"},
		"no proof":     {func(s *Source) { s.Vtxo.Tapscript.ControlBlock = nil }, "missing selected"},
		"version":      {func(s *Source) { s.Vtxo.Tapscript.ControlBlock.LeafVersion = 0xc2 }, "requires TapScript"},
		"internal key": {func(s *Source) { _, s.Vtxo.Tapscript.ControlBlock.InternalKey = btcec.PrivKeyFromBytes([]byte{3}) }, "NUMS"},
		"trailing proof": {func(s *Source) {
			s.Vtxo.Tapscript.ControlBlock.InclusionProof = append(s.Vtxo.Tapscript.ControlBlock.InclusionProof, 0)
		}, "invalid selected control block"},
		"proof parity": {func(s *Source) {
			s.Vtxo.Tapscript.ControlBlock.OutputKeyYIsOdd = !s.Vtxo.Tapscript.ControlBlock.OutputKeyYIsOdd
		}, "selected source proof"},
		"proof branch":    {func(s *Source) { s.Vtxo.Tapscript.ControlBlock.InclusionProof[0] ^= 1 }, "selected source proof"},
		"no tree":         {func(s *Source) { s.Vtxo.RevealedTapscripts = nil }, "source tree"},
		"invalid tree":    {func(s *Source) { s.Vtxo.RevealedTapscripts[0] = "xx" }, "invalid source tree leaf"},
		"absent leaf":     {func(s *Source) { s.Vtxo.RevealedTapscripts = s.Vtxo.RevealedTapscripts[:1] }, "selected leaf absent"},
		"tree commitment": {func(s *Source) { s.Vtxo.RevealedTapscripts[0] = "51" }, "tree does not commit"},
		"no server": {func(s *Source) {
			_, pk := btcec.PrivKeyFromBytes([]byte{4})
			copy(s.Vtxo.Tapscript.RevealedScript[35:67], schnorr.SerializePubKey(pk))
		}, "canonical collaborative"},
		"extra opcode": {func(s *Source) {
			s.Vtxo.Tapscript.RevealedScript = append(s.Vtxo.Tapscript.RevealedScript, txscript.OP_TRUE)
		}, "canonical collaborative"},
		"invalid xonly": {func(s *Source) { copy(s.Vtxo.Tapscript.RevealedScript[1:33], bytes.Repeat([]byte{255}, 32)) }, "canonical collaborative"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s, server, _ := sourceFixture(t, 1000, 0)
			tc.edit(&s)
			if _, err := admitSource(s, server); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %q, got %v", tc.reason, err)
			}
		})
	}
	s, server, _ := sourceFixture(t, 1000, 0)
	got, err := admitSource(s, server)
	if err != nil {
		t.Fatal(err)
	}
	s.PreviousTx.TxOut[0].Value++
	s.Vtxo.Tapscript.RevealedScript[0] ^= 1
	s.Vtxo.Tapscript.ControlBlock.InclusionProof[0] ^= 1
	s.Vtxo.Outpoint.Index++
	if _, err := admitSource(got, server); err != nil {
		t.Fatalf("admitted source aliases caller: %v", err)
	}
}

func TestFundingAndAmountAdmission(t *testing.T) {
	s, _, _ := sourceFixture(t, 1000, 0)
	for _, tc := range []struct {
		f        Funding
		required int64
		ok       bool
	}{
		{Funding{}, 0, true}, {Funding{Inputs: []Source{s}}, 0, false}, {Funding{Change: wire.NewTxOut(1, nil)}, 0, false},
		{Funding{}, 1, false}, {Funding{Inputs: []Source{s}}, 1000, true}, {Funding{Inputs: []Source{s}}, 1001, false},
		{Funding{Inputs: []Source{s}}, 999, false}, {Funding{Inputs: []Source{s}, Change: wire.NewTxOut(1, nil)}, 999, true},
		{Funding{Inputs: []Source{s}, Change: wire.NewTxOut(0, nil)}, 1000, false}, {Funding{Inputs: []Source{s}, Change: wire.NewTxOut(2, nil)}, 999, false},
		{Funding{Inputs: []Source{s}}, -1, false}, {Funding{Inputs: []Source{s}}, math.MaxInt64, false},
	} {
		if err := validateFunding(tc.f, tc.required); (err == nil) != tc.ok {
			t.Fatalf("required %d/change %+v: %v", tc.required, tc.f.Change, err)
		}
	}
	for _, tc := range [][2]int64{{maxMoney, 1}, {-1, 0}, {0, -1}, {0, math.MaxInt64}, {math.MaxInt64, math.MaxInt64}} {
		if _, err := addAmount(tc[0], tc[1]); err == nil {
			t.Fatalf("unchecked sum %v", tc)
		}
	}
	if sum, err := addAmount(maxMoney-1, 1); err != nil || sum != maxMoney {
		t.Fatalf("exact bound: %d %v", sum, err)
	}
}

func TestUpstreamBundleAssembly(t *testing.T) {
	s1, server, cpScript := sourceFixture(t, 600, 1)
	s2, _, _ := sourceFixture(t, 900, 2)
	state := State{Phase: Phase{Kind: AwaitPlayer1FundingAndReveal}, Deadline: math.MaxUint64}
	e, _ := ActionExtension(&state)
	data, _ := e.TxOut()
	outputs := []*wire.TxOut{wire.NewTxOut(1000, bytes.Clone(s1.PreviousTx.TxOut[0].PkScript)), wire.NewTxOut(500, []byte{txscript.OP_TRUE}), data}
	bundle, err := buildUnsigned([]Source{s1, s2}, outputs, cpScript, server, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Spending) != 0 || len(bundle.Checkpoints) != 2 || len(bundle.Ark.Inputs) != 2 || len(bundle.Ark.UnsignedTx.TxOut) != 4 {
		t.Fatal("wrong shape")
	}
	if bundle.Ark.UnsignedTx.Version != 3 || bundle.Ark.UnsignedTx.LockTime != 0 {
		t.Fatal("wrong transaction header")
	}
	for i, source := range []Source{s1, s2} {
		if err := checkBuiltLink(bundle.Ark, bundle.Checkpoints[i], i, source); err != nil {
			t.Fatal(err)
		}
		previous, err := txutils.GetArkPsbtFields(bundle.Ark, i, arkade.PrevArkTxField)
		if err != nil || len(previous) != 1 || previous[0].TxHash() != source.PreviousTx.TxHash() {
			t.Fatalf("logical previous field: %v", err)
		}
		if bundle.Ark.Inputs[i].NonWitnessUtxo.TxHash() != bundle.Checkpoints[i].UnsignedTx.TxHash() || bundle.Checkpoints[i].Inputs[0].NonWitnessUtxo.TxHash() != source.PreviousTx.TxHash() {
			t.Fatal("nonwitness source linkage")
		}
		trees, err := txutils.GetArkPsbtFields(bundle.Ark, i, txutils.VtxoTaprootTreeField)
		if err != nil || len(trees) != 1 || len(trees[0]) != 2 {
			t.Fatalf("checkpoint tree: %v", err)
		}
		for _, p := range []*psbt.Packet{bundle.Ark, bundle.Checkpoints[i]} {
			var buf bytes.Buffer
			if err := p.Serialize(&buf); err != nil {
				t.Fatal(err)
			}
			if _, err := psbt.NewFromRawBytes(&buf, false); err != nil {
				t.Fatalf("real PSBT roundtrip: %v", err)
			}
		}
	}
	got, err := ReadState(bundle.Ark.UnsignedTx)
	if err != nil || *got != state {
		t.Fatalf("state: %v", err)
	}
	for i, out := range outputs {
		if !equalOutput(out, bundle.Ark.UnsignedTx.TxOut[i]) {
			t.Fatalf("changed action output %d", i)
		}
	}
	outputs[0].PkScript[0] ^= 1
	s1.PreviousTx.TxOut[0].Value++
	if bundle.Ark.UnsignedTx.TxOut[0].PkScript[0] != txscript.OP_1 || bundle.Checkpoints[0].Inputs[0].NonWitnessUtxo.TxOut[0].Value != 600 {
		t.Fatal("result aliases caller")
	}
}

func TestBundleRejectsWrongSourcesAndOutputs(t *testing.T) {
	s, server, checkpoint := sourceFixture(t, 1000, 0)
	valid := []*wire.TxOut{wire.NewTxOut(1000, []byte{txscript.OP_TRUE})}
	for _, tc := range []struct {
		sources []Source
		outputs []*wire.TxOut
		reason  string
	}{
		{nil, valid, "inputs"}, {[]Source{s}, nil, "outputs"}, {[]Source{s, s}, []*wire.TxOut{wire.NewTxOut(2000, nil)}, "duplicates source"},
		{[]Source{s}, []*wire.TxOut{wire.NewTxOut(999, nil)}, "does not equal"}, {[]Source{s}, []*wire.TxOut{wire.NewTxOut(-1, nil)}, "outside"},
		{[]Source{s}, []*wire.TxOut{nil}, "nil"}, {[]Source{s}, append(valid, txutils.AnchorOutput()), "premature anchor"},
	} {
		if _, err := buildUnsigned(tc.sources, tc.outputs, checkpoint, server, nil); err == nil || !strings.Contains(err.Error(), tc.reason) {
			t.Fatalf("want %q, got %v", tc.reason, err)
		}
	}
	if _, err := buildUnsigned([]Source{s}, valid, []byte{txscript.OP_TRUE}, server, nil); err == nil {
		t.Fatal("upstream accepted invalid checkpoint")
	}
}

func TestSpendingMetadataUsesCheckpointProof(t *testing.T) {
	// The bundle adapter operates on any admitted collaborative path. A production
	// poker path will additionally be compiled and executed by emulator tests.
	s, server, cpScript := sourceFixture(t, 1000, 0)
	cb, _ := s.Vtxo.Tapscript.ControlBlock.ToBytes()
	leaf := &psbt.TaprootTapLeafScript{Script: bytes.Clone(s.Vtxo.Tapscript.RevealedScript), ControlBlock: cb, LeafVersion: txscript.BaseLeafVersion}
	path := SpendingPath{Identity: SpendingIdentity{Kind: SpendBetting, Actor: Player1, Street: Flop}, Leaf: leaf, Tree: append(txutils.TapTree(nil), s.Vtxo.RevealedTapscripts...), Program: []byte{txscript.OP_TRUE}}
	bundle, err := buildUnsigned([]Source{s}, []*wire.TxOut{wire.NewTxOut(1000, nil)}, cpScript, server, &path)
	if err != nil {
		t.Fatal(err)
	}
	got := bundle.Spending[0].Path
	if len(bundle.Spending) != 1 || bundle.Spending[0].InputIndex != 0 || got.Identity != path.Identity || !bytes.Equal(got.Leaf.Script, leaf.Script) || bytes.Equal(got.Leaf.ControlBlock, leaf.ControlBlock) || len(got.Tree) != 2 || got.Tree[0] != hex.EncodeToString(cpScript) {
		t.Fatal("metadata did not follow checkpoint")
	}
	leaf.ControlBlock[0] ^= 1
	if _, err := buildUnsigned([]Source{s}, []*wire.TxOut{wire.NewTxOut(1000, nil)}, cpScript, server, &path); err == nil {
		t.Fatal("mismatched source metadata accepted")
	}
}
