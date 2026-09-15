package game

import (
	"bytes"
	"context"
	"io"
	"math"
	"slices"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/wallet"
	"github.com/btcsuite/btcd/wire"
)

func (g *Game) buildAction(a PreparedAction) (*covenant.Unsigned, error) {
	if g.setup == nil || g.setup.contract == nil {
		return nil, ErrInput
	}
	c := g.setup.contract
	if a.Kind == ActionInitialDeposit {
		if g.hand != nil || a.Funding == nil {
			return nil, ErrInput
		}
		return c.InitialDeposit(*a.Funding)
	}
	if g.hand == nil {
		return nil, ErrInput
	}
	tx := g.hand.accepted.Transaction
	switch a.Kind {
	case ActionPlayer1Funding:
		return c.Player1FundingAndReveal(tx, *a.Funding, *a.Reveals)
	case ActionPlayer2Opening:
		if a.Bet.Kind != covenant.Check && a.Bet.Kind != covenant.RaiseTo {
			return nil, ErrInput
		}
		amount := int64(0)
		if a.Bet.Kind == covenant.RaiseTo {
			amount = a.Bet.Amount
		}
		return c.Player2RevealAndOpening(tx, amount, *a.Funding, *a.Reveals)
	case ActionBetting:
		return c.Bet(tx, a.Bet, *a.Funding)
	case ActionBoardReveal:
		return c.BoardReveal(tx, *a.Reveals)
	case ActionShowdownReveal:
		return c.ShowdownReveal(tx, *a.Reveals)
	case ActionAllInCall:
		return c.AllInCall(tx, *a.Funding, *a.Reveals)
	case ActionAllInReveal:
		return c.AllInReveal(tx, *a.Reveals)
	case ActionConcession:
		return c.Concession(tx)
	case ActionTimeout:
		return c.Timeout(tx)
	case ActionShowdown:
		return c.Showdown(tx, g.setup.role, *a.Showdown)
	}
	return nil, ErrInput
}
func (g *Game) actionPermitted(k ActionKind) bool {
	switch k {
	case ActionInitialDeposit:
		return g.stage == StageInitialDeposit
	case ActionPlayer1Funding:
		return g.stage == StagePlayer1Funding
	case ActionPlayer2Opening:
		return g.stage == StagePlayer2Opening
	case ActionBetting:
		return g.stage == StageBettingLocal
	case ActionBoardReveal:
		return g.stage == StageBoardRevealLocal
	case ActionShowdownReveal:
		return g.stage == StageShowdownLocal
	case ActionAllInCall:
		return g.stage == StageAllInLocal
	case ActionAllInReveal:
		return g.stage == StageAllInRevealLocal
	case ActionConcession:
		return g.stage == StageBettingLocal || g.stage == StageAllInLocal || g.stage == StageAllInRevealLocal || g.stage == StageShowdownLocal
	case ActionTimeout:
		return g.stage == StageAwaitPlayer1Funding || g.stage == StageAwaitPlayer2Opening || g.stage == StageBettingOpponent || g.stage == StageBoardRevealOpponent || g.stage == StageAllInOpponent || g.stage == StageAllInRevealOpponent || g.stage == StageShowdownOpponent
	case ActionShowdown:
		return g.stage == StageSettleShowdown
	}
	return false
}
func (g *Game) savedAction(a PreparedAction, b *covenant.Unsigned) (SavedSpend, error) {
	bundle, err := serializeBuild(b)
	if err != nil {
		return SavedSpend{}, err
	}
	s := SavedSpend{Action: a, Route: wallet.ArkdRoute, Prepared: bundle}
	if g.hand != nil {
		op := wire.OutPoint{Hash: g.hand.accepted.Transaction.TxHash()}
		s.Source = &op
		s.Route = wallet.EmulatorRoute
	}
	if a.Funding != nil {
		for _, source := range a.Funding.Inputs {
			if source.Vtxo.Outpoint == nil {
				return SavedSpend{}, ErrEncoding
			}
			s.WalletSources = append(s.WalletSources, *source.Vtxo.Outpoint)
		}
	}
	return s, nil
}
func sameBundle(a, b ports.Bundle) bool {
	return a.Ark == b.Ark && slices.Equal(a.Checkpoints, b.Checkpoints)
}
func sameSaved(a, b SavedSpend) bool {
	a.Signed = nil
	b.Signed = nil
	wa, wb := new(encoder), new(encoder)
	encodeSaved(wa, a)
	encodeSaved(wb, b)
	return wa.err == nil && wb.err == nil && bytes.Equal(wa.Bytes(), wb.Bytes())
}
func (g *Game) reduceHand(e Event) error {
	switch e.Kind {
	case DepositObserved:
		if g.stage != StageAwaitInitialDeposit {
			if g.prepared == nil || g.prepared.saved.Action.Kind != ActionInitialDeposit {
				return protocolError("deposit observation phase")
			}
			if e.Accepted.Transaction.TxHash() != g.prepared.built.Ark.UnsignedTx.TxHash() {
				return protocolError("deposit transaction identity")
			}
		}
		if err := g.checkDeposit(*e.Accepted); err != nil {
			return err
		}
		g.hand = &handState{accepted: *e.Accepted, observedAt: e.ObservedAt}
		g.prepared = nil
		return g.routeHand()
	case SpendPrepared:
		s := e.Spend
		a := s.Action
		if s.Signed != nil || !g.actionPermitted(a.Kind) {
			return protocolError("prepared action phase")
		}
		if g.hand != nil {
			state, err := covenant.ReadState(g.hand.accepted.Transaction)
			if err != nil {
				return err
			}
			actor, _ := state.Phase.RequiredActor()
			switch a.Kind {
			case ActionTimeout:
				if actor != other(g.setup.role) || a.ObservedAt < state.Deadline {
					return protocolError("timeout eligibility")
				}
			case ActionShowdown:
				winner, err := g.checkEvaluation(g.hand, *a.Showdown)
				if err != nil {
					return err
				}
				eligible, err := g.settlementEligible(winner, a.ObservedAt)
				if err != nil {
					return err
				}
				if !eligible {
					return protocolError("settlement priority")
				}
			default:
				if actor != g.setup.role {
					return protocolError("local action actor")
				}
			}
		}
		built, err := g.buildAction(a)
		if err != nil {
			return err
		}
		expected, err := g.savedAction(a, built)
		if err != nil {
			return err
		}
		if !sameSaved(expected, *s) {
			return protocolError("prepared bundle differs from action")
		}
		if a.Funding != nil {
			if err := checkFunding(g.config, *a.Funding); err != nil {
				return err
			}
		}
		if a.Kind == ActionInitialDeposit {
			if err := g.checkDeposit(acceptedBuild(built)); err != nil {
				return err
			}
		} else {
			if _, _, err := g.admitSpend(g.hand, acceptedBuild(built), g.hand.observedAt); err != nil {
				return err
			}
		}
		g.prepared = &preparedState{saved: *s, built: built}
		g.stage = StageTransactionPrepared
		return nil
	case SpendSigned:
		if g.stage != StageTransactionPrepared || g.prepared == nil || e.Spend.Signed == nil {
			return protocolError("signed spend phase")
		}
		if !sameSaved(g.prepared.saved, *e.Spend) {
			return protocolError("signed prepared work changed")
		}
		if err := wallet.CompareBundles(g.prepared.saved.Prepared, *e.Spend.Signed); err != nil {
			return err
		}
		next := *g.prepared
		next.saved = *e.Spend
		g.prepared = &next
		g.stage = StageTransactionSigned
		return nil
	case SubmissionAttempted:
		if (g.stage != StageTransactionSigned && g.stage != StageAwaitTransactionAcceptance) || g.prepared == nil {
			return protocolError("submission phase")
		}
		if e.Receipt.TxID != nil && *e.Receipt.TxID != g.prepared.built.Ark.UnsignedTx.TxHash() {
			return protocolError("submission receipt identity")
		}
		if g.stage == StageAwaitTransactionAcceptance && !e.Receipt.Recovery {
			if uint64(g.prepared.lastAttempt) > math.MaxUint64-5 || e.ObservedAt < g.prepared.lastAttempt+5 {
				return protocolError("retry interval")
			}
		}
		next := *g.prepared
		next.lastAttempt = e.ObservedAt
		g.prepared = &next
		g.stage = StageAwaitTransactionAcceptance
		return nil
	case SubmissionFailed:
		if g.prepared == nil || (g.stage != StageTransactionSigned && g.stage != StageAwaitTransactionAcceptance) {
			return protocolError("submission failure phase")
		}
		// Failure retains exact work and the accepted source. Driver stops automatic
		// advancement; it is not a terminal outcome or permission to start a new game.
		g.stage = StageAwaitTransactionAcceptance
		return nil
	case SpendObserved:
		if g.hand == nil {
			return protocolError("spend observation phase")
		}
		next, outcome, err := g.admitSpend(g.hand, *e.Accepted, e.ObservedAt)
		if err != nil {
			return err
		}
		g.prepared = nil
		g.evaluated = nil
		if outcome != nil {
			state, err := covenant.ReadState(g.hand.accepted.Transaction)
			if err != nil {
				return err
			}
			cards, err := g.knownCards(g.hand)
			if err != nil {
				return err
			}
			g.completed = &completedTable{state: *state, cards: cards}
			g.outcome = outcome
			g.hand = nil
			g.stage = StageFinished
			return nil
		}
		g.hand = next
		return g.routeHand()
	case OpeningObserved:
		if g.stage != StageAwaitPlayer1Funding && g.stage != StageAwaitPlayer2Opening {
			return protocolError("opening observation phase")
		}
		if g.hand == nil {
			return ErrInput
		}
		next, err := g.openingHand(e.Accepted, e.ObservedAt)
		if err != nil {
			return err
		}
		next.privateHoles = e.PrivateHoles
		if _, err := g.decodedCards(next); err != nil {
			return err
		}
		g.hand = next
		return g.routeHand()
	case ShowdownEvaluated:
		if g.stage != StageEvaluateShowdown || g.hand == nil {
			return protocolError("evaluation phase")
		}
		winner, err := g.checkEvaluation(g.hand, *e.Evaluation)
		if err != nil {
			return err
		}
		if winner != e.Winner {
			return protocolError("showdown result")
		}
		g.evaluated = &evaluatedState{witness: *e.Evaluation, winner: winner}
		g.stage = StageSettleShowdown
		return nil
	}
	return ErrInput
}
func (g *Game) routeHand() error {
	if g.hand == nil {
		return ErrInput
	}
	h := g.hand
	if h.privateHoles == nil {
		if g.setup.role == covenant.Player2 && h.reveals[2] != nil {
			g.stage = StageAwaitPlayer1Funding
			return nil
		}
		if g.setup.role == covenant.Player1 && h.reveals[0] != nil {
			g.stage = StageAwaitPlayer2Opening
			return nil
		}
	}
	state, err := covenant.ReadState(h.accepted.Transaction)
	if err != nil {
		return err
	}
	actor, err := state.Phase.RequiredActor()
	if err != nil {
		return err
	}
	local := actor == g.setup.role
	switch state.Phase.Kind {
	case covenant.AwaitPlayer1FundingAndReveal:
		g.stage = StageAwaitPlayer1Funding
		if local {
			g.stage = StagePlayer1Funding
		}
	case covenant.AwaitPlayer2RevealAndOpening:
		g.stage = StageAwaitPlayer2Opening
		if local {
			g.stage = StagePlayer2Opening
		}
	case covenant.Betting:
		g.stage = StageBettingOpponent
		if local {
			g.stage = StageBettingLocal
		}
	case covenant.BoardReveal:
		g.stage = StageBoardRevealOpponent
		if local {
			g.stage = StageBoardRevealLocal
		}
	case covenant.AllIn:
		g.stage = StageAllInOpponent
		if local {
			g.stage = StageAllInLocal
		}
	case covenant.AllInReveal:
		g.stage = StageAllInRevealOpponent
		if local {
			g.stage = StageAllInRevealLocal
		}
	case covenant.ShowdownReveal:
		g.stage = StageShowdownOpponent
		if local {
			g.stage = StageShowdownLocal
		}
	case covenant.ShowdownEvaluation:
		g.stage = StageEvaluateShowdown
	default:
		return ErrInput
	}
	return nil
}
func (g *Game) settlementEligible(winner covenant.Player, at covenant.UnixSeconds) (bool, error) {
	priority := covenant.Player1
	if winner == covenant.Player2 {
		priority = winner
	}
	if g.setup.role == priority {
		return true, nil
	}
	if uint64(g.hand.observedAt) > math.MaxUint64-30 {
		return false, protocolError("settlement time overflow")
	}
	return at >= g.hand.observedAt+30, nil
}

// PendingSpend returns independently owned exact prepared/signed bytes and
// builder inputs. Submission/recovery must use these bytes without replacing work.
func (g *Game) PendingSpend() (SavedSpend, error) {
	if g == nil || g.prepared == nil {
		return SavedSpend{}, ErrInput
	}
	w := new(encoder)
	encodeSaved(w, g.prepared.saved)
	b, err := w.finish()
	if err != nil {
		return SavedSpend{}, err
	}
	r := &decoder{data: b}
	s := decodeSaved(r)
	return s, r.done()
}

// PrepareOpening records private local shares only after the opponent's opening
// and the local stake/bond obligation have been admitted. No public message or
// speculative UI disclosure is produced.
func (g *Game) PrepareOpening(ctx context.Context, entropy io.Reader, a AcceptedTransaction, at covenant.UnixSeconds) (Event, error) {
	if g == nil || g.hand == nil || (g.stage != StageAwaitPlayer1Funding && g.stage != StageAwaitPlayer2Opening) {
		return Event{}, ErrInput
	}
	if err := checkTxSyntax(a.Transaction); err != nil {
		return Event{}, err
	}
	for _, cp := range a.Checkpoints {
		if err := checkTxSyntax(cp); err != nil {
			return Event{}, err
		}
	}
	if _, err := g.openingHand(&a, at); err != nil {
		return Event{}, err
	}
	// The same reducer check is repeated by Apply, before any cards become visible.
	var holes [2]covenant.CardReveal
	for i, slot := range ownSlots(g.setup.role) {
		r, err := g.makeShare(ctx, entropy, slot)
		if err != nil {
			return Event{}, err
		}
		holes[i] = r
	}
	e := g.NewEvent(OpeningObserved)
	e.Accepted = &a
	e.PrivateHoles = &holes
	e.ObservedAt = at
	candidate := *g
	if err := candidate.reduceHand(e); err != nil {
		return Event{}, err
	}
	return e, nil
}
func (g *Game) PrepareEvaluation(ctx context.Context) (Event, error) {
	if g == nil || g.stage != StageEvaluateShowdown {
		return Event{}, ErrInput
	}
	w, winner, err := g.evaluate(ctx)
	if err != nil {
		return Event{}, err
	}
	e := g.NewEvent(ShowdownEvaluated)
	e.Evaluation = &w
	e.Winner = winner
	return e, nil
}

// openingHand admits the locked opening before private shares are generated.
func (g *Game) openingHand(accepted *AcceptedTransaction, observedAt covenant.UnixSeconds) (*handState, error) {
	next := *g.hand
	if accepted.Transaction.TxHash() == g.hand.accepted.Transaction.TxHash() {
		if len(accepted.Checkpoints) != len(g.hand.accepted.Checkpoints) {
			return nil, protocolError("opening checkpoints changed")
		}
		for i, cp := range accepted.Checkpoints {
			a, b := new(encoder), new(encoder)
			encodeTx(a, cp)
			encodeTx(b, g.hand.accepted.Checkpoints[i])
			if a.err != nil || b.err != nil || !bytes.Equal(a.Bytes(), b.Bytes()) {
				return nil, protocolError("opening checkpoints changed")
			}
		}
	} else {
		h, o, err := g.admitSpend(g.hand, *accepted, observedAt)
		if err != nil {
			return nil, err
		}
		if o != nil {
			return nil, protocolError("terminal opening")
		}
		next = *h
	}
	if next.privateHoles != nil {
		return nil, protocolError("private holes already recorded")
	}
	start := 0
	if g.setup.role == covenant.Player2 {
		start = 2
	}
	if next.reveals[start] == nil || next.reveals[start+1] == nil {
		return nil, protocolError("opponent opening not accepted")
	}

	return &next, nil
}
