package covenant

import (
	"fmt"
	"reflect"

	"github.com/btcsuite/btcd/wire"
)

// Shape selection is semantic, never caller-chosen slot indices. Unused fields
// must be zero so one witness cannot have multiple interpretations. Proof bytes
// are encoded in reverse publication order; the lowest slot executes first.
func publishReveals(state *State, r RevealWitness, expected RevealKind, actor Player) (wire.TxWitness, error) {
	if r.Kind != expected || !validPlayer(actor) {
		return nil, fmt.Errorf("covenant: reveal witness does not match action")
	}
	type item struct {
		slot byte
		card CardReveal
	}
	var items []item
	player := byte(actor - Player1)
	holes := func(opponent bool) {
		start := byte(14 + 2*player)
		if opponent {
			start = 2 * (1 - player)
		}
		for i, c := range r.Holes {
			items = append(items, item{start + byte(i), c})
		}
		r.Holes = [2]CardReveal{}
	}
	flop := func() {
		for i, c := range r.Flop {
			items = append(items, item{4 + 3*player + byte(i), c})
		}
		r.Flop = [3]CardReveal{}
	}
	turn := func() { items = append(items, item{10 + player, r.Turn}); r.Turn = CardReveal{} }
	river := func() { items = append(items, item{12 + player, r.River}); r.River = CardReveal{} }
	switch expected {
	case RevealOpponentHoles:
		holes(true)
	case RevealOwnHoles:
		holes(false)
	case RevealFlop:
		flop()
	case RevealTurn:
		turn()
	case RevealRiver:
		river()
	case RevealRemainingFromFlop:
		flop()
		turn()
		river()
		holes(false)
	case RevealRemainingFromTurn:
		turn()
		river()
		holes(false)
	case RevealRemainingFromRiver:
		river()
		holes(false)
	default:
		return nil, fmt.Errorf("covenant: unknown reveal kind")
	}
	r.Kind = 0
	if !reflect.ValueOf(r).IsZero() {
		return nil, fmt.Errorf("covenant: irrelevant reveal fields must be zero")
	}
	witness := make(wire.TxWitness, len(items))
	updated := state.Reveals
	for i, item := range items {
		share, err := item.card.Share.AffineBytes()
		if err != nil {
			return nil, fmt.Errorf("covenant: slot %d share: %w", item.slot, err)
		}
		proof, err := item.card.Proof.AffineBytes()
		if err != nil {
			return nil, fmt.Errorf("covenant: slot %d proof: %w", item.slot, err)
		}
		updated[item.slot] = share
		witness[len(items)-1-i] = append([]byte(nil), proof[:]...)
	}
	state.Reveals = updated
	return witness, nil
}
func startEqual(s State) error {
	if s.LastAction != LastStart || s.Wagers.Player1 != s.Wagers.Player2 {
		return fmt.Errorf("covenant: reveal requires Start and equal wagers")
	}
	return nil
}
func boardRevealKind(street Street) RevealKind { return RevealFlop + RevealKind(street-Flop) }
func remainingRevealKind(street Street) RevealKind {
	if street == River {
		return RevealOwnHoles
	}
	return RevealRemainingFromFlop + RevealKind(street-PreFlop)
}

func (c *Contract) BoardReveal(previous *wire.MsgTx, reveals RevealWitness) (*Unsigned, error) {
	state, err := c.livePredecessor(previous)
	if err != nil {
		return nil, err
	}
	phase := state.Phase
	if phase.Kind != BoardReveal {
		return nil, fmt.Errorf("covenant: expected board reveal")
	}
	if err := startEqual(state); err != nil {
		return nil, err
	}
	if state.Wagers.Player1 >= uint64(c.params.MaxWager) {
		return nil, fmt.Errorf("covenant: board reveal requires wagers below cap")
	}
	witness, err := publishReveals(&state, reveals, boardRevealKind(phase.Street), phase.Actor)
	if err != nil {
		return nil, err
	}
	state.Phase = Phase{Kind: Betting, Street: phase.Street, Actor: otherPlayer(phase.Actor)}
	if phase.Pass == FirstPass {
		state.Phase.Kind = BoardReveal
		state.Phase.Pass = SecondPass
	}
	return c.buildLive(previous, Funding{}, SpendingIdentity{Kind: SpendBoardReveal, Street: phase.Street, Actor: phase.Actor}, state, witness)
}
func (c *Contract) ShowdownReveal(previous *wire.MsgTx, reveals RevealWitness) (*Unsigned, error) {
	state, err := c.livePredecessor(previous)
	if err != nil {
		return nil, err
	}
	phase := state.Phase
	if phase.Kind != ShowdownReveal {
		return nil, fmt.Errorf("covenant: expected showdown reveal")
	}
	if err := startEqual(state); err != nil {
		return nil, err
	}
	if phase.Pass == FirstPass && state.Wagers.Player1 == uint64(c.params.MaxWager) {
		return nil, fmt.Errorf("covenant: first showdown reveal requires wagers below cap")
	}
	witness, err := publishReveals(&state, reveals, RevealOwnHoles, phase.Actor)
	if err != nil {
		return nil, err
	}
	state.Phase = Phase{Kind: ShowdownEvaluation}
	if phase.Pass == FirstPass {
		state.Phase = Phase{Kind: ShowdownReveal, Pass: SecondPass, Actor: otherPlayer(phase.Actor)}
	}
	return c.buildLive(previous, Funding{}, SpendingIdentity{Kind: SpendShowdownReveal, Actor: phase.Actor}, state, witness)
}
func (c *Contract) AllInCall(previous *wire.MsgTx, funding Funding, reveals RevealWitness) (*Unsigned, error) {
	state, err := c.livePredecessor(previous)
	if err != nil {
		return nil, err
	}
	phase := state.Phase
	if phase.Kind != AllIn {
		return nil, fmt.Errorf("covenant: expected all-in call")
	}
	a, o := wagers(state, phase.Actor)
	cap := uint64(c.params.MaxWager)
	if state.LastAction != LastRaise || o != cap || a >= cap {
		return nil, fmt.Errorf("covenant: all-in call requires Raise, opponent at cap, actor below cap")
	}
	witness, err := publishReveals(&state, reveals, remainingRevealKind(phase.Street), phase.Actor)
	if err != nil {
		return nil, err
	}
	state.Wagers = PerPlayer[uint64]{cap, cap}
	state.LastAction = LastStart
	if phase.Street == River {
		state.Phase = Phase{Kind: ShowdownReveal, Pass: SecondPass, Actor: otherPlayer(phase.Actor)}
	} else {
		state.Phase = Phase{Kind: AllInReveal, Street: phase.Street + 1, Actor: otherPlayer(phase.Actor)}
	}
	return c.buildLive(previous, funding, SpendingIdentity{Kind: SpendAllInCall, Street: phase.Street, Actor: phase.Actor}, state, witness)
}
func (c *Contract) AllInReveal(previous *wire.MsgTx, reveals RevealWitness) (*Unsigned, error) {
	state, err := c.livePredecessor(previous)
	if err != nil {
		return nil, err
	}
	phase := state.Phase
	if phase.Kind != AllInReveal {
		return nil, fmt.Errorf("covenant: expected all-in reveal")
	}
	if err := startEqual(state); err != nil {
		return nil, err
	}
	if state.Wagers.Player1 != uint64(c.params.MaxWager) {
		return nil, fmt.Errorf("covenant: all-in reveal requires capped wagers")
	}
	witness, err := publishReveals(&state, reveals, remainingRevealKind(phase.Street-1), phase.Actor)
	if err != nil {
		return nil, err
	}
	state.Phase = Phase{Kind: ShowdownEvaluation}
	return c.buildLive(previous, Funding{}, SpendingIdentity{Kind: SpendAllInReveal, Street: phase.Street, Actor: phase.Actor}, state, witness)
}
