package covenant

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
)

// The builder only assembles poker bytecode. Opcode semantics and resource
// enforcement belong to the real Arkade engine; this is not a poker evaluator.
// txscript's builder owns number/push encoding and the 10,000-byte script bound.
type programBuilder struct {
	b   *txscript.ScriptBuilder
	err error
}

func newProgram() *programBuilder                        { return &programBuilder{b: txscript.NewScriptBuilder()} }
func (p *programBuilder) op(ops ...byte) *programBuilder { p.b.AddOps(ops); return p }
func (p *programBuilder) num(n int64) *programBuilder    { p.b.AddInt64(n); return p }
func (p *programBuilder) data(b []byte) *programBuilder {
	// AddData collapses {00} to OP_0 (the empty vector). Unsigned packet numbers
	// need an actual zero sign byte when concatenating. Arkade accepts this exact
	// one-byte push as minimal data, matching the reference's bytes([0]).
	if len(b) == 1 && b[0] == 0 {
		p.b.AddOps([]byte{txscript.OP_DATA_1, 0})
	} else {
		p.b.AddData(b)
	}
	return p
}
func (p *programBuilder) include(b []byte, err error) *programBuilder {
	if p.err == nil {
		p.err = err
	}
	if err == nil {
		p.b.AddOps(b)
	}
	return p
}
func (p *programBuilder) bytes() ([]byte, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.b.Script()
}
func (p *programBuilder) unsignedField(depth, offset, length int64) *programBuilder {
	if depth == 0 {
		p.op(arkade.OP_DUP)
	} else {
		p.num(depth).op(arkade.OP_PICK)
	}
	return p.num(offset).num(length).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
}
func headerPrefix(id ContractID) []byte {
	b := make([]byte, offPhase)
	binary.LittleEndian.PutUint16(b, packetVersion)
	copy(b[2:], id[:])
	return b
}
func (p *programBuilder) terminalPacketAbsence() *programBuilder {
	for kind := int64(typeState); kind <= typePlayer2; kind++ {
		p.num(kind).op(arkade.OP_INSPECTPACKET, arkade.OP_NOT, arkade.OP_VERIFY, arkade.OP_DROP)
	}
	return p
}
func (p *programBuilder) payout(index int64, pkScript []byte) *programBuilder {
	version := int64(-1)
	hash := sha256.Sum256(pkScript)
	program := hash[:]
	if txscript.IsWitnessProgram(pkScript) {
		v, b, _ := txscript.ExtractWitnessProgramInfo(pkScript)
		version = int64(v)
		program = b
	}
	return p.num(index).op(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY).num(version).op(arkade.OP_NUMEQUALVERIFY).data(program).op(arkade.OP_EQUALVERIFY)
}

func validateAmounts(p Params) error {
	for _, field := range []struct {
		name  string
		value int64
	}{{"stake", p.Stake}, {"bond", p.Bond}, {"min_bet", p.MinBet}, {"max_wager", p.MaxWager}} {
		if field.value <= 0 || field.value > maxMoney {
			return fmt.Errorf("covenant: %s must be positive and at most MAX_MONEY", field.name)
		}
	}
	if p.MinBet > p.MaxWager {
		return fmt.Errorf("covenant: min_bet exceeds max_wager")
	}
	total, err := addAmount(p.Stake, p.Bond)
	if err != nil {
		return err
	}
	total, err = addAmount(total, p.MaxWager)
	if err != nil {
		return err
	}
	if _, err = addAmount(total, total); err != nil {
		return fmt.Errorf("covenant: maximum covenant value: %w", err)
	}
	return nil
}

// outerLeaf keeps participant authorization, emulator program commitment and
// server authorization together. Signatures live in the outer Taproot witness;
// the emulator witness carries only the poker program's arguments.
func outerLeaf(params Params, actor Player, program []byte) ([]byte, [32]byte, error) {
	var tweaked [32]byte
	if !validPlayer(actor) {
		return nil, tweaked, fmt.Errorf("covenant: invalid spending actor")
	}
	participant := params.Players.Player1.SigningKey
	if actor == Player2 {
		participant = params.Players.Player2.SigningKey
	}
	user, err := schnorr.ParsePubKey(participant[:])
	if err != nil {
		return nil, tweaked, fmt.Errorf("covenant: participant key: %w", err)
	}
	server, err := schnorr.ParsePubKey(params.ArkSigningKey[:])
	if err != nil {
		return nil, tweaked, fmt.Errorf("covenant: server key: %w", err)
	}
	base, err := schnorr.ParsePubKey(params.EmulatorSigningKey[:])
	if err != nil {
		return nil, tweaked, fmt.Errorf("covenant: emulator key: %w", err)
	}
	key := arkade.ComputeArkadeScriptPublicKey(base, arkade.ArkadeScriptHash(program))
	// The upstream tweak routine returns a point container even at infinity.
	if !key.IsOnCurve() {
		return nil, tweaked, fmt.Errorf("covenant: emulator tweak is infinity")
	}
	copy(tweaked[:], schnorr.SerializePubKey(key))
	// The emulator finalizes only when it is the last non-Arkd signer.
	// Commit the participant after it so /v1/tx returns signatures only.
	closure := &script.MultisigClosure{PubKeys: []*btcec.PublicKey{key, user, server}}
	outer, err := closure.Script()
	return outer, tweaked, err
}
