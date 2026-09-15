package covenant

import (
	"fmt"

	"github.com/btcsuite/btcd/wire"
)

func (c *Contract) Player2RevealAndOpening(previous *wire.MsgTx, openingBet int64, funding Funding, reveals RevealWitness) (*Unsigned, error) {
	state, err := c.livePredecessor(previous)
	if err != nil {
		return nil, err
	}
	if state.Phase != (Phase{Kind: AwaitPlayer2RevealAndOpening}) || state.LastAction != LastStart || state.Wagers != (PerPlayer[uint64]{}) {
		return nil, fmt.Errorf("covenant: expected Player2 opening with Start and zero wagers")
	}
	if openingBet < 0 || openingBet > c.params.MaxWager || (openingBet != 0 && openingBet < c.params.MinBet) {
		return nil, fmt.Errorf("covenant: opening wager must be zero or within minimum and cap")
	}
	witness, err := publishReveals(&state, reveals, RevealOpponentHoles, Player2)
	if err != nil {
		return nil, err
	}
	state.Wagers.Player2 = uint64(openingBet)
	state.LastAction = LastRaise
	if openingBet == 0 {
		state.LastAction = LastCheck
	}
	state.Phase = Phase{Kind: Betting, Street: PreFlop, Actor: Player1}
	if openingBet == c.params.MaxWager {
		state.Phase.Kind = AllIn
	}
	return c.buildLive(previous, funding, SpendingIdentity{Kind: SpendPlayer2Opening, Actor: Player2}, state, witness)
}
func wagers(s State, actor Player) (uint64, uint64) {
	if actor == Player1 {
		return s.Wagers.Player1, s.Wagers.Player2
	}
	return s.Wagers.Player2, s.Wagers.Player1
}
func (c *Contract) Bet(previous *wire.MsgTx, action BettingAction, funding Funding) (*Unsigned, error) {
	state, err := c.livePredecessor(previous)
	if err != nil {
		return nil, err
	}
	phase := state.Phase
	if phase.Kind != Betting {
		return nil, fmt.Errorf("covenant: expected ordinary betting phase")
	}
	actor, street := phase.Actor, phase.Street
	opponent := otherPlayer(actor)
	a, o := wagers(state, actor)
	cap, minBet := uint64(c.params.MaxWager), uint64(c.params.MinBet)
	if a >= cap || o >= cap {
		return nil, fmt.Errorf("covenant: ordinary betting requires wagers below cap")
	}
	last := state.LastAction
	if street == PreFlop && (last == LastStart || (actor == Player2 && last == LastCheck)) {
		return nil, fmt.Errorf("covenant: invalid betting history for street and actor")
	}
	if last == LastRaise {
		if o <= a || o-a < minBet {
			return nil, fmt.Errorf("covenant: Raise history requires opponent lead of at least minimum bet")
		}
	} else if a != o {
		return nil, fmt.Errorf("covenant: Start or Check requires equal wagers")
	}
	var target uint64
	switch action.Kind {
	case Check:
		if action.Amount != 0 || last == LastRaise || a != o {
			return nil, fmt.Errorf("covenant: check requires equal wagers without outstanding raise")
		}
		target = a
	case Call:
		if action.Amount != 0 || last != LastRaise || o <= a {
			return nil, fmt.Errorf("covenant: call requires an outstanding raise")
		}
		target = o
	case RaiseTo:
		if action.Amount < 0 {
			return nil, fmt.Errorf("covenant: negative raise target")
		}
		target = uint64(action.Amount)
		if target > cap || target <= a || target <= o {
			return nil, fmt.Errorf("covenant: raise target must exceed both wagers and be at or below cap")
		}
		minimum := minBet
		if last == LastRaise {
			minimum = o - a
		}
		if target != cap && target-o < minimum {
			return nil, fmt.Errorf("covenant: raise below minimum increment")
		}
	default:
		return nil, fmt.Errorf("covenant: unknown betting action")
	}
	if actor == Player1 {
		state.Wagers.Player1 = target
	} else {
		state.Wagers.Player2 = target
	}
	if target == o {
		if last == LastStart {
			state.LastAction = LastCheck
			state.Phase = Phase{Kind: Betting, Street: street, Actor: opponent}
		} else {
			state.LastAction = LastStart
			if street == River {
				state.Phase = Phase{Kind: ShowdownReveal, Pass: FirstPass, Actor: opponent}
			} else {
				state.Phase = Phase{Kind: BoardReveal, Street: street + 1, Pass: FirstPass, Actor: opponent}
			}
		}
	} else {
		state.LastAction = LastRaise
		state.Phase = Phase{Kind: Betting, Street: street, Actor: opponent}
		if target == cap {
			state.Phase.Kind = AllIn
		}
	}
	return c.buildLive(previous, funding, SpendingIdentity{Kind: SpendBetting, Street: street, Actor: actor}, state, nil)
}
