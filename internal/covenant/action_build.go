package covenant

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

func firstOutput(previous *wire.MsgTx) (*wire.TxOut, error) {
	if previous == nil || len(previous.TxOut) == 0 || previous.TxOut[0] == nil {
		return nil, fmt.Errorf("covenant: missing predecessor output 0")
	}
	return previous.TxOut[0], nil
}
func (c *Contract) liveValue(s State) (int64, error) {
	if s.Wagers.Player1 > uint64(c.params.MaxWager) || s.Wagers.Player2 > uint64(c.params.MaxWager) {
		return 0, fmt.Errorf("covenant: wager exceeds cap")
	}
	return 2*(c.params.Stake+c.params.Bond) + int64(s.Wagers.Player1) + int64(s.Wagers.Player2), nil
}
func (c *Contract) livePredecessor(previous *wire.MsgTx) (State, error) {
	if err := c.check(); err != nil {
		return State{}, err
	}
	out, err := firstOutput(previous)
	if err != nil {
		return State{}, err
	}
	s, err := ReadState(previous)
	if err != nil {
		return State{}, err
	}
	if s.ContractID != c.id {
		return State{}, fmt.Errorf("covenant: predecessor contract id mismatch")
	}
	value, err := c.liveValue(*s)
	if err != nil {
		return State{}, err
	}
	if out.Value != value {
		return State{}, fmt.Errorf("covenant: incorrect joint covenant value")
	}
	return *s, nil
}
func (c *Contract) exitPredecessor(previous *wire.MsgTx) (Phase, int64, error) {
	if err := c.check(); err != nil {
		return Phase{}, 0, err
	}
	out, err := firstOutput(previous)
	if err != nil {
		return Phase{}, 0, err
	}
	if _, err := addAmount(0, out.Value); err != nil {
		return Phase{}, 0, err
	}
	header, err := readHeader(previous)
	if err != nil {
		return Phase{}, 0, err
	}
	if len(header) != headerLen || binary.LittleEndian.Uint16(header) != packetVersion || !bytes.Equal(header[2:offPhase], c.id[:]) {
		return Phase{}, 0, fmt.Errorf("covenant: invalid predecessor header")
	}
	phase, err := DecodePhase(header[offPhase])
	return phase, out.Value, err
}
func (c *Contract) path(identity SpendingIdentity) (*SpendingPath, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	for i := range c.paths {
		if c.paths[i].Identity == identity {
			return &c.paths[i], nil
		}
	}
	return nil, fmt.Errorf("covenant: unavailable spending identity %+v", identity)
}
func (c *Contract) buildAction(previous *wire.MsgTx, funding []Source, path *SpendingPath, outputs []*wire.TxOut) (*Unsigned, error) {
	out, err := firstOutput(previous)
	if err != nil {
		return nil, err
	}
	// Validate structural pointers before wire hashes the source transaction.
	for _, in := range previous.TxIn {
		if in == nil {
			return nil, fmt.Errorf("covenant: nil predecessor input")
		}
	}
	for _, out := range previous.TxOut {
		if out == nil {
			return nil, fmt.Errorf("covenant: nil predecessor output")
		}
	}
	control, err := txscript.ParseControlBlock(path.Leaf.ControlBlock)
	if err != nil {
		return nil, err
	}
	source := Source{PreviousTx: previous, Vtxo: offchain.VtxoInput{Outpoint: &wire.OutPoint{Hash: previous.TxHash(), Index: 0}, Amount: out.Value, Tapscript: &waddrmgr.Tapscript{ControlBlock: control, RevealedScript: path.Leaf.Script}, RevealedTapscripts: path.Tree}}
	sources := append([]Source{source}, funding...)
	return buildUnsigned(sources, outputs, c.checkpointScript, c.params.ArkSigningKey, path)
}
func (c *Contract) buildLive(previous *wire.MsgTx, funding Funding, identity SpendingIdentity, successor State, witness wire.TxWitness) (*Unsigned, error) {
	if successor.Deadline > math.MaxUint64-DeadlineInterval {
		return nil, fmt.Errorf("covenant: successor deadline overflows u64")
	}
	successor.Deadline += DeadlineInterval
	value, err := c.liveValue(successor)
	if err != nil {
		return nil, err
	}
	out, err := firstOutput(previous)
	if err != nil {
		return nil, err
	}
	if out.Value < 0 || value < out.Value {
		return nil, fmt.Errorf("covenant: live action cannot withdraw funds")
	}
	if err := validateFunding(funding, value-out.Value); err != nil {
		return nil, err
	}
	path, err := c.path(identity)
	if err != nil {
		return nil, err
	}
	ext, err := ActionExtension(&successor, arkade.EmulatorEntry{Vin: 0, Script: path.Program, Witness: witness})
	if err != nil {
		return nil, err
	}
	data, err := ext.TxOut()
	if err != nil {
		return nil, err
	}
	outputs := []*wire.TxOut{wire.NewTxOut(value, c.script)}
	if funding.Change != nil {
		outputs = append(outputs, funding.Change)
	}
	outputs = append(outputs, data)
	return c.buildAction(previous, funding.Inputs, path, outputs)
}
func (c *Contract) buildTerminal(previous *wire.MsgTx, identity SpendingIdentity, payouts []*wire.TxOut, witness wire.TxWitness) (*Unsigned, error) {
	path, err := c.path(identity)
	if err != nil {
		return nil, err
	}
	ext, err := ActionExtension(nil, arkade.EmulatorEntry{Vin: 0, Script: path.Program, Witness: witness})
	if err != nil {
		return nil, err
	}
	data, err := ext.TxOut()
	if err != nil {
		return nil, err
	}
	return c.buildAction(previous, nil, path, append(payouts, data))
}
func otherPlayer(p Player) Player {
	if p == Player1 {
		return Player2
	}
	return Player1
}
func (c *Contract) payout(player Player, value int64) *wire.TxOut {
	script := c.params.Players.Player1.PayoutScript
	if player == Player2 {
		script = c.params.Players.Player2.PayoutScript
	}
	return wire.NewTxOut(value, script)
}

// InitialDeposit uses the ordinary Ark route; no poker input executes. Signing,
// accepted source evidence, payout/change service admission and the initial
// clock policy remain wallet/client responsibilities for every public builder.
func (c *Contract) InitialDeposit(funding Funding) (*Unsigned, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	required := c.params.Stake + c.params.Bond
	if err := validateFunding(funding, required); err != nil {
		return nil, err
	}
	state := State{ContractID: c.id, Phase: Phase{Kind: AwaitPlayer1FundingAndReveal}, Deadline: c.params.InitialDeadline}
	ext, err := ActionExtension(&state)
	if err != nil {
		return nil, err
	}
	data, err := ext.TxOut()
	if err != nil {
		return nil, err
	}
	outputs := []*wire.TxOut{wire.NewTxOut(required, c.script)}
	if funding.Change != nil {
		outputs = append(outputs, funding.Change)
	}
	outputs = append(outputs, data)
	return buildUnsigned(funding.Inputs, outputs, c.checkpointScript, c.params.ArkSigningKey, nil)
}

func (c *Contract) Player1FundingAndReveal(previous *wire.MsgTx, funding Funding, reveals RevealWitness) (*Unsigned, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	out, err := firstOutput(previous)
	if err != nil {
		return nil, err
	}
	if out.Value != c.params.Stake+c.params.Bond {
		return nil, fmt.Errorf("covenant: deposit must equal stake + bond")
	}
	state, err := ReadState(previous)
	if err != nil {
		return nil, err
	}
	initial := State{ContractID: c.id, Phase: Phase{Kind: AwaitPlayer1FundingAndReveal}, Deadline: c.params.InitialDeadline}
	if *state != initial {
		return nil, fmt.Errorf("covenant: expected exact initial state with zero wagers and reveals")
	}
	witness, err := publishReveals(state, reveals, RevealOpponentHoles, Player1)
	if err != nil {
		return nil, err
	}
	state.Phase = Phase{Kind: AwaitPlayer2RevealAndOpening}
	return c.buildLive(previous, funding, SpendingIdentity{Kind: SpendPlayer1Funding, Actor: Player1}, *state, witness)
}
