package game

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/merkel"
	"arkade-poker/go/internal/shuffle"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

func protocolError(why string) error { return fmt.Errorf("%w: %s", ErrProtocol, why) }
func equalOutput(a, b *wire.TxOut) bool {
	return a != nil && b != nil && a.Value == b.Value && bytes.Equal(a.PkScript, b.PkScript)
}
func addAmount(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > maxMoney || b > maxMoney-a {
		return 0, ErrAmount
	}
	return a + b, nil
}
func records(tx *wire.MsgTx) (extension.Extension, error) {
	if err := checkTxSyntax(tx); err != nil {
		return nil, err
	}
	if len(tx.TxOut) < 2 {
		return nil, protocolError("ARK output position")
	}
	out := tx.TxOut[len(tx.TxOut)-2]
	tok := txscript.MakeScriptTokenizer(0, out.PkScript)
	if !tok.Next() || tok.Opcode() != txscript.OP_RETURN || !tok.Next() {
		return nil, protocolError("ARK output")
	}
	n, op := len(tok.Data()), tok.Opcode()
	minimal := n <= 75 && op == byte(n) || n > 75 && n <= 255 && op == txscript.OP_PUSHDATA1 || n > 255 && n <= 65535 && op == txscript.OP_PUSHDATA2 || n > 65535 && op == txscript.OP_PUSHDATA4
	if !minimal || tok.Next() || tok.Err() != nil {
		return nil, protocolError("ARK push or trailing instruction")
	}
	packets, err := extension.NewExtensionFromBytes(out.PkScript)
	if err != nil {
		return nil, err
	}
	seen := map[byte]bool{}
	for _, p := range packets {
		kind := p.Type()
		if seen[kind] || kind != 1 && (kind < 0x20 || kind > 0x23) {
			return nil, protocolError("ARK record type")
		}
		seen[kind] = true
	}
	return packets, nil
}
func programWitness(tx *wire.MsgTx) (arkade.EmulatorEntry, error) {
	packets, err := records(tx)
	if err != nil {
		return arkade.EmulatorEntry{}, err
	}
	p := packets.GetPacketByType(arkade.PacketType)
	if p == nil {
		return arkade.EmulatorEntry{}, protocolError("missing emulator program")
	}
	b, err := p.Serialize()
	if err != nil {
		return arkade.EmulatorEntry{}, err
	}
	packet, err := arkade.DeserializeEmulatorPacket(b)
	if err != nil {
		return arkade.EmulatorEntry{}, err
	}
	if len(packet) != 1 || packet[0].Vin != 0 {
		return arkade.EmulatorEntry{}, protocolError("emulator input index")
	}
	return packet[0], nil
}
func checkLayout(c Config, tx *wire.MsgTx, payouts int) error {
	if err := checkTxSyntax(tx); err != nil {
		return err
	}
	if tx.Version != 3 || tx.LockTime != 0 || len(tx.TxIn) == 0 || len(tx.TxIn) > 256 || len(tx.TxOut) < payouts+2 || len(tx.TxOut) > payouts+3 || !equalOutput(tx.TxOut[len(tx.TxOut)-1], txutils.AnchorOutput()) {
		return protocolError("transaction layout")
	}
	for _, in := range tx.TxIn {
		if len(in.SignatureScript) != 0 || in.Sequence != wire.MaxTxInSequenceNum {
			return protocolError("transaction input")
		}
	}
	if tx.TxOut[len(tx.TxOut)-2].Value != 0 {
		return protocolError("ARK output value")
	}
	for _, out := range tx.TxOut[:len(tx.TxOut)-2] {
		if err := policyOutput(c, out.PkScript, out.Value); err != nil {
			return err
		}
	}
	_, err := records(tx)
	return err
}
func (g *Game) checkDeposit(a AcceptedTransaction) error {
	if err := g.checkDepositPacket(a.Transaction); err != nil {
		return err
	}
	return g.checkEdges(a, nil)
}
func (g *Game) checkDepositPacket(tx *wire.MsgTx) error {
	a := AcceptedTransaction{Transaction: tx}
	if err := checkLayout(g.config, a.Transaction, 1); err != nil {
		return err
	}
	s, err := covenant.ReadState(a.Transaction)
	if err != nil {
		return err
	}
	id, _ := g.setup.contract.ID()
	p := g.setup.params
	pk, _ := g.setup.contract.ScriptPubKey()
	want := covenant.State{ContractID: id, Phase: covenant.Phase{Kind: covenant.AwaitPlayer1FundingAndReveal}, LastAction: covenant.LastStart, Deadline: p.InitialDeadline}
	if *s != want || !equalOutput(a.Transaction.TxOut[0], wire.NewTxOut(p.Stake+p.Bond, pk)) {
		return protocolError("initial deposit packet or output")
	}
	packets, err := records(a.Transaction)
	if err != nil {
		return err
	}
	if len(packets) != 4 || packets.GetPacketByType(1) != nil {
		return protocolError("initial deposit records")
	}
	return nil
}
func (g *Game) checkEdges(a AcceptedTransaction, h *handState) error {
	if err := checkTxSyntax(a.Transaction); err != nil {
		return err
	}
	tx := a.Transaction
	if len(a.Checkpoints) != len(tx.TxIn) || len(tx.TxIn) == 0 || len(tx.TxIn) > 256 {
		return protocolError("checkpoint count")
	}
	seen := map[wire.OutPoint]bool{}
	total := int64(0)
	for i, cp := range a.Checkpoints {
		if err := checkTxSyntax(cp); err != nil {
			return err
		}
		if cp.Version != 3 || cp.LockTime != 0 || len(cp.TxIn) != 1 || len(cp.TxOut) != 2 || !equalOutput(cp.TxOut[1], txutils.AnchorOutput()) || cp.TxIn[0].Sequence != wire.MaxTxInSequenceNum || len(cp.TxIn[0].SignatureScript) != 0 || tx.TxIn[i].PreviousOutPoint != (wire.OutPoint{Hash: cp.TxHash()}) || seen[cp.TxIn[0].PreviousOutPoint] || !txscript.IsPayToTaproot(cp.TxOut[0].PkScript) {
			return protocolError("logical checkpoint edge")
		}
		seen[cp.TxIn[0].PreviousOutPoint] = true
		var err error
		total, err = addAmount(total, cp.TxOut[0].Value)
		if err != nil {
			return err
		}
	}
	outputs := int64(0)
	for _, out := range tx.TxOut {
		var err error
		outputs, err = addAmount(outputs, out.Value)
		if err != nil {
			return err
		}
	}
	if total != outputs {
		return protocolError("accepted transaction balance")
	}
	if h == nil {
		return nil
	}
	cp := a.Checkpoints[0]
	previous := h.accepted.Transaction
	if cp.TxIn[0].PreviousOutPoint != (wire.OutPoint{Hash: previous.TxHash()}) || cp.TxOut[0].Value != previous.TxOut[0].Value {
		return protocolError("covenant source continuity")
	}
	entry, err := programWitness(tx)
	if err != nil {
		return err
	}
	paths, err := g.setup.contract.SpendingPaths()
	if err != nil {
		return err
	}
	for _, path := range paths {
		if !bytes.Equal(path.Program, entry.Script) {
			continue
		}
		tree := txscript.AssembleTaprootScriptTree(txscript.NewBaseTapLeaf(g.config.Wallet.CheckpointScript), txscript.NewBaseTapLeaf(path.Leaf.Script))
		root := tree.RootNode.TapHash()
		key := txscript.ComputeTaprootOutputKey(script.UnspendableKey(), root[:])
		pk, err := script.P2TRScript(key)
		if err != nil {
			return err
		}
		if bytes.Equal(cp.TxOut[0].PkScript, pk) {
			return nil
		}
	}
	return protocolError("checkpoint program or script differs from agreement")
}
func checkFunding(c Config, f covenant.Funding) error {
	if len(f.Inputs) > 255 {
		return protocolError("funding count")
	}
	if f.Change != nil {
		if err := policyOutput(c, f.Change.PkScript, f.Change.Value); err != nil {
			return err
		}
	}
	for _, s := range f.Inputs {
		if s.Vtxo.Tapscript == nil {
			return protocolError("funding selected path")
		}
		tok := txscript.MakeScriptTokenizer(0, s.Vtxo.Tapscript.RevealedScript)
		keys := 0
		for tok.Next() {
			if bytes.Equal(tok.Data(), c.Wallet.WalletPublicKey[:]) {
				keys++
			}
		}
		if tok.Err() != nil || keys != 1 {
			return protocolError("funding owner differs from local signing key")
		}
	}
	return nil
}
func wager(s covenant.State, p covenant.Player) uint64 {
	if p == covenant.Player1 {
		return s.Wagers.Player1
	}
	return s.Wagers.Player2
}
func setWager(s *covenant.State, p covenant.Player, n uint64) {
	if p == covenant.Player1 {
		s.Wagers.Player1 = n
	} else {
		s.Wagers.Player2 = n
	}
}

func (g *Game) admitSpend(h *handState, a AcceptedTransaction, at covenant.UnixSeconds) (*handState, *Outcome, error) {
	if err := g.checkEdges(a, h); err != nil {
		return nil, nil, err
	}
	packets, err := records(a.Transaction)
	if err != nil {
		return nil, nil, err
	}
	hasState := false
	for _, p := range packets {
		hasState = hasState || p.Type() >= 0x20
	}
	if !hasState {
		o, err := g.admitTerminal(h, a)
		return nil, o, err
	}
	if err := checkLayout(g.config, a.Transaction, 1); err != nil {
		return nil, nil, err
	}
	before, err := covenant.ReadState(h.accepted.Transaction)
	if err != nil {
		return nil, nil, err
	}
	after, err := covenant.ReadState(a.Transaction)
	if err != nil {
		return nil, nil, err
	}
	actor, err := before.Phase.RequiredActor()
	if err != nil || actor == 0 {
		return nil, nil, protocolError("live spend after evaluation")
	}
	opponent := other(actor)
	cap, min := uint64(g.setup.params.MaxWager), uint64(g.setup.params.MinBet)
	mine, theirs, target := wager(*before, actor), wager(*before, opponent), wager(*after, actor)
	expected := *before
	if uint64(before.Deadline) > math.MaxUint64-covenant.DeadlineInterval {
		return nil, nil, protocolError("live deadline overflow")
	}
	expected.Deadline += covenant.DeadlineInterval
	contribution := uint64(0)
	slots := []int(nil)
	switch phase := before.Phase; phase.Kind {
	case covenant.AwaitPlayer1FundingAndReveal:
		expected.Phase = covenant.Phase{Kind: covenant.AwaitPlayer2RevealAndOpening}
		slots = []int{2, 3}
		contribution = uint64(g.setup.params.Stake + g.setup.params.Bond)
	case covenant.AwaitPlayer2RevealAndOpening:
		if target > cap || target != 0 && target < min {
			return nil, nil, protocolError("opening wager")
		}
		expected.Wagers.Player2 = target
		expected.LastAction = covenant.LastCheck
		kind := covenant.Betting
		if target != 0 {
			expected.LastAction = covenant.LastRaise
		}
		if target == cap {
			kind = covenant.AllIn
		}
		expected.Phase = covenant.Phase{Kind: kind, Street: covenant.PreFlop, Actor: opponent}
		slots = []int{0, 1}
		contribution = target
	case covenant.Betting:
		if target < mine || target > cap {
			return nil, nil, protocolError("betting wager")
		}
		contribution = target - mine
		if target == theirs {
			if before.LastAction == covenant.LastStart {
				expected.Phase = covenant.Phase{Kind: covenant.Betting, Street: phase.Street, Actor: opponent}
				expected.LastAction = covenant.LastCheck
			} else {
				if phase.Street == covenant.River {
					expected.Phase = covenant.Phase{Kind: covenant.ShowdownReveal, Pass: covenant.FirstPass, Actor: opponent}
				} else {
					expected.Phase = covenant.Phase{Kind: covenant.BoardReveal, Street: phase.Street + 1, Pass: covenant.FirstPass, Actor: opponent}
				}
				expected.LastAction = covenant.LastStart
			}
		} else {
			minimum := min
			if before.LastAction == covenant.LastRaise {
				if theirs < mine {
					return nil, nil, protocolError("raise history")
				}
				minimum = theirs - mine
			}
			if target <= theirs || target <= mine || target != cap && target-theirs < minimum {
				return nil, nil, protocolError("raise increment")
			}
			kind := covenant.Betting
			if target == cap {
				kind = covenant.AllIn
			}
			expected.Phase = covenant.Phase{Kind: kind, Street: phase.Street, Actor: opponent}
			expected.LastAction = covenant.LastRaise
		}
		setWager(&expected, actor, target)
	case covenant.BoardReveal:
		slots = boardSlots(actor, phase.Street)
		if phase.Pass == covenant.FirstPass {
			expected.Phase = covenant.Phase{Kind: covenant.BoardReveal, Street: phase.Street, Pass: covenant.SecondPass, Actor: opponent}
		} else {
			expected.Phase = covenant.Phase{Kind: covenant.Betting, Street: phase.Street, Actor: opponent}
		}
	case covenant.ShowdownReveal:
		slots = ownSlots(actor)
		if phase.Pass == covenant.FirstPass {
			expected.Phase = covenant.Phase{Kind: covenant.ShowdownReveal, Pass: covenant.SecondPass, Actor: opponent}
		} else {
			expected.Phase = covenant.Phase{Kind: covenant.ShowdownEvaluation}
		}
	case covenant.AllIn:
		if mine > cap {
			return nil, nil, protocolError("all-in wager")
		}
		contribution = cap - mine
		expected.Wagers = covenant.PerPlayer[uint64]{Player1: cap, Player2: cap}
		expected.LastAction = covenant.LastStart
		if phase.Street == covenant.River {
			expected.Phase = covenant.Phase{Kind: covenant.ShowdownReveal, Pass: covenant.SecondPass, Actor: opponent}
		} else {
			expected.Phase = covenant.Phase{Kind: covenant.AllInReveal, Street: phase.Street + 1, Actor: opponent}
		}
		slots = revealSlots(phase)
	case covenant.AllInReveal:
		expected.Phase = covenant.Phase{Kind: covenant.ShowdownEvaluation}
		slots = remainingSlots(actor, phase.Street)
	default:
		return nil, nil, protocolError("evaluation has no live successor")
	}
	entry, err := programWitness(a.Transaction)
	if err != nil {
		return nil, nil, err
	}
	if len(entry.Witness) != len(slots) {
		return nil, nil, protocolError("new reveal proof count")
	}
	next := *h
	for i, slot := range slots {
		if h.reveals[slot] != nil {
			return nil, nil, protocolError("repeated reveal publication")
		}
		share, err := shuffle.RevealTokenFromAffine(after.Reveals[slot])
		if err != nil {
			return nil, nil, err
		}
		data := entry.Witness[len(slots)-1-i]
		if len(data) != 160 {
			return nil, nil, protocolError("reveal proof length")
		}
		proof, err := shuffle.RevealProofFromAffine([160]byte(data))
		if err != nil {
			return nil, nil, err
		}
		r := covenant.CardReveal{Share: share, Proof: proof}
		if _, err := g.verifyShare(slot, r); err != nil {
			return nil, nil, err
		}
		expected.Reveals[slot] = after.Reveals[slot]
		next.reveals[slot] = &r
	}
	if *after != expected {
		return nil, nil, protocolError("successor phase, wagers, deadline or preserved shares")
	}
	if contribution > uint64(maxMoney) {
		return nil, nil, ErrAmount
	}
	value, err := addAmount(h.accepted.Transaction.TxOut[0].Value, int64(contribution))
	if err != nil {
		return nil, nil, err
	}
	pk, _ := g.setup.contract.ScriptPubKey()
	if !equalOutput(a.Transaction.TxOut[0], wire.NewTxOut(value, pk)) {
		return nil, nil, protocolError("successor covenant value or script")
	}
	if contribution == 0 && (len(a.Transaction.TxIn) != 1 || len(a.Transaction.TxOut) != 3) {
		return nil, nil, protocolError("zero contribution has funding or change")
	}
	if contribution > 0 && len(a.Transaction.TxIn) < 2 {
		return nil, nil, protocolError("missing wager funding")
	}
	next.accepted = a
	next.observedAt = at
	if _, err := g.decodedCards(&next); err != nil {
		return nil, nil, err
	}
	return &next, nil, nil
}
func matchesTerminal(build *covenant.Unsigned, tx *wire.MsgTx) bool {
	expected := build.Ark.UnsignedTx
	if len(expected.TxIn) != len(tx.TxIn) || len(expected.TxOut) != len(tx.TxOut) {
		return false
	}
	for i, in := range expected.TxIn {
		if in.PreviousOutPoint != tx.TxIn[i].PreviousOutPoint {
			return false
		}
	}
	for i, out := range expected.TxOut {
		if !equalOutput(out, tx.TxOut[i]) {
			return false
		}
	}
	return true
}
func (g *Game) admitTerminal(h *handState, a AcceptedTransaction) (*Outcome, error) {
	previous := h.accepted.Transaction
	source, err := covenant.ReadState(previous)
	if err != nil {
		return nil, err
	}
	c := g.setup.contract
	actor, _ := source.Phase.RequiredActor()
	settlement := Settlement{}
	if b, err := c.Concession(previous); err == nil && matchesTerminal(b, a.Transaction) {
		settlement = Settlement{Kind: SettlementConcession, Winner: other(actor)}
	} else if b, err := c.Timeout(previous); err == nil && matchesTerminal(b, a.Transaction) {
		settlement = Settlement{Kind: SettlementTimeout, Winner: other(actor)}
	} else if source.Phase.Kind == covenant.ShowdownEvaluation {
		entry, err := programWitness(a.Transaction)
		if err != nil {
			return nil, err
		}
		w, err := decodeShowdownStack(entry.Witness)
		if err != nil {
			return nil, err
		}
		winner, err := g.checkEvaluation(h, w)
		if err != nil {
			return nil, err
		}
		matched := false
		for _, p := range []covenant.Player{covenant.Player1, covenant.Player2} {
			b, err := c.Showdown(previous, p, w)
			if err != nil {
				return nil, err
			}
			matched = matched || matchesTerminal(b, a.Transaction)
		}
		if !matched {
			return nil, protocolError("showdown payout or path")
		}
		settlement = Settlement{Kind: SettlementShowdown, Winner: winner}
	} else {
		return nil, protocolError("terminal payout or path")
	}
	payouts := 2
	if settlement.Kind == SettlementTimeout {
		payouts = 1
	}
	if err := checkLayout(g.config, a.Transaction, payouts); err != nil {
		return nil, err
	}
	if len(a.Transaction.TxIn) != 1 || len(a.Transaction.TxOut) != payouts+2 {
		return nil, protocolError("terminal funding or change")
	}
	kind := Tied
	if settlement.Winner != 0 {
		kind = Lost
		if settlement.Winner == g.setup.role {
			kind = Won
		}
	}
	o := &Outcome{Kind: kind, Transaction: a.Transaction, Settlement: settlement}
	id := a.Transaction.TxHash()
	if settlement.Kind == SettlementTimeout {
		op := wire.OutPoint{Hash: id}
		if settlement.Winner == covenant.Player1 {
			o.Payouts.Player1 = op
		} else {
			o.Payouts.Player2 = op
		}
	} else {
		o.Payouts = covenant.PerPlayer[wire.OutPoint]{Player1: wire.OutPoint{Hash: id}, Player2: wire.OutPoint{Hash: id, Index: 1}}
	}
	return o, nil
}
func decodeShowdownStack(stack wire.TxWitness) (covenant.ShowdownWitness, error) {
	if len(stack) != 7 || len(stack[6]) != 9 {
		return covenant.ShowdownWitness{}, protocolError("showdown witness layout")
	}
	var proofs [2]merkel.HandProof
	for i := range proofs {
		start := 3 * i
		if len(stack[start]) != 2 || len(stack[start+1]) != 416 || len(stack[start+2]) != 448 {
			return covenant.ShowdownWitness{}, protocolError("showdown rank segments")
		}
		p, err := merkel.DecodeProof(append(append([]byte(nil), stack[start+1]...), stack[start+2]...))
		if err != nil {
			return covenant.ShowdownWitness{}, err
		}
		proofs[i] = merkel.HandProof{Rank: binary.LittleEndian.Uint16(stack[start]), Proof: p}
	}
	return covenant.ShowdownWitness{Cards: dealFrom([9]byte(stack[6])), RankProofs: covenant.PerPlayer[merkel.HandProof]{Player1: proofs[0], Player2: proofs[1]}}, nil
}
