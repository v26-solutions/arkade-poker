package game

import (
	"context"
	"io"

	"arkade-poker/go/internal/covenant"
)

const (
	PrepareDepositEffect     EffectKind = iota + 20 // attach, fresh deadline, select/build
	DiscoverDepositEffect                           // attach, discover, bounded wait, discover before expiry
	ObserveLocalActionEffect                        // reconcile accepted source before choice/preparation
	WaitOpponentEffect                              // attach and reconcile before/after bounded wait
	PrepareOpeningEffect                            // already-admitted opening; no network or publication
	EvaluateShowdownEffect
	WaitSettlementEffect
	SignPreparedEffect // attach/reconcile before signing exact prepared work
	SubmitSignedEffect // attach/reconcile before exact submission
	ReconcileSubmissionEffect
)

func (g *Game) openingReady() bool {
	if g.hand == nil || g.hand.privateHoles != nil {
		return false
	}
	start := 0
	if g.setup.role == covenant.Player2 {
		start = 2
	}
	return g.hand.reveals[start] != nil && g.hand.reveals[start+1] != nil
}
func (g *Game) decideHandEffect(input Input) (Step, error) {
	onlyProgress := func(effect EffectKind) (Step, error) {
		if input.Kind != Progress {
			return Step{}, ErrInput
		}
		return Step{Kind: RunEffect, Effect: effect}, nil
	}
	opponent := func() (Step, error) {
		if input.Kind != Progress && input.Kind != ClaimTimeout {
			return Step{}, ErrInput
		}
		return Step{Kind: RunEffect, Effect: WaitOpponentEffect}, nil
	}
	switch g.stage {
	case StageInitialDeposit:
		return onlyProgress(PrepareDepositEffect)
	case StageAwaitInitialDeposit:
		return onlyProgress(DiscoverDepositEffect)
	case StagePlayer1Funding, StageBoardRevealLocal:
		return onlyProgress(ObserveLocalActionEffect)
	case StageAwaitPlayer1Funding, StageAwaitPlayer2Opening:
		if g.openingReady() {
			return onlyProgress(PrepareOpeningEffect)
		}
		return opponent()
	case StagePlayer2Opening:
		if input.Kind != Progress && (input.Kind != Bet || (input.Bet.Kind != covenant.Check && input.Bet.Kind != covenant.RaiseTo)) {
			return Step{}, ErrInput
		}
	case StageBettingLocal:
		if input.Kind != Progress && input.Kind != Bet && input.Kind != Concede {
			return Step{}, ErrInput
		}
	case StageAllInLocal:
		if input.Kind != Progress && input.Kind != Concede && (input.Kind != Bet || input.Bet.Kind != covenant.Call) {
			return Step{}, ErrInput
		}
	case StageAllInRevealLocal, StageShowdownLocal:
		if input.Kind != Progress && input.Kind != Concede && input.Kind != RevealShowdown {
			return Step{}, ErrInput
		}
	case StageBettingOpponent, StageBoardRevealOpponent, StageAllInOpponent, StageAllInRevealOpponent, StageShowdownOpponent:
		return opponent()
	case StageEvaluateShowdown:
		return onlyProgress(EvaluateShowdownEffect)
	case StageSettleShowdown:
		return onlyProgress(WaitSettlementEffect)
	case StageTransactionPrepared:
		return onlyProgress(SignPreparedEffect)
	case StageTransactionSigned:
		return onlyProgress(SubmitSignedEffect)
	case StageAwaitTransactionAcceptance:
		return onlyProgress(ReconcileSubmissionEffect)
	case StageFinished:
		if input.Kind != Progress {
			return Step{}, ErrInput
		}
		o := *g.outcome
		o.Transaction = o.Transaction.Copy()
		return Step{Kind: Finished, Outcome: &o}, nil
	default:
		return Step{}, ErrInput
	}
	return Step{Kind: RunEffect, Effect: ObserveLocalActionEffect}, nil
}
func (g *Game) handChoice() (*Choice, error) {
	if g.hand == nil {
		return nil, nil
	}
	params := g.setup.params
	switch g.stage {
	case StagePlayer2Opening:
		return &Choice{Allowed: []InputKind{Bet}, CanCheck: true, MinRaiseTo: params.MinBet, MaxRaiseTo: params.MaxWager}, nil
	case StageAllInRevealLocal, StageShowdownLocal:
		return &Choice{Allowed: []InputKind{RevealShowdown, Concede}}, nil
	case StageBettingLocal, StageAllInLocal:
		state, err := covenant.ReadState(g.hand.accepted.Transaction)
		if err != nil {
			return nil, err
		}
		mine, theirs := wager(*state, g.setup.role), wager(*state, other(g.setup.role))
		if theirs < mine || theirs > uint64(params.MaxWager) {
			return nil, protocolError("betting amounts")
		}
		call := theirs - mine
		c := &Choice{Allowed: []InputKind{Bet, Concede}, CallAmount: int64(call), CanCheck: call == 0, CanCall: call > 0}
		if g.stage == StageAllInLocal {
			c.CanCheck = false
			c.CanCall = true
			c.CallAmount = params.MaxWager - int64(mine)
			return c, nil
		}
		increment := uint64(params.MinBet)
		if state.LastAction == covenant.LastRaise {
			increment = call
		}
		minimum := min(theirs+increment, uint64(params.MaxWager))
		if theirs < uint64(params.MaxWager) {
			c.MinRaiseTo = int64(minimum)
			c.MaxRaiseTo = params.MaxWager
		}
		return c, nil
	}
	return nil, nil
}

// RequiredFunding is a pure selection amount, not permission to spend. The
// driver must first reconcile the accepted source and satisfy the effect's
// attachment/time requirements before selecting coins or preparing proofs.
func (g *Game) RequiredFunding(input Input) (int64, error) {
	if _, err := g.Decide(input); err != nil {
		return 0, err
	}
	switch g.stage {
	case StageInitialDeposit, StagePlayer1Funding:
		return g.setup.params.Stake + g.setup.params.Bond, nil
	case StagePlayer2Opening:
		if input.Kind != Bet {
			return 0, ErrInput
		}
		if input.Bet.Kind == covenant.Check {
			return 0, nil
		}
		n := input.Bet.Amount
		if n < 0 || n > g.setup.params.MaxWager || n != 0 && n < g.setup.params.MinBet {
			return 0, ErrAmount
		}
		return n, nil
	case StageBettingLocal, StageAllInLocal:
		if input.Kind == Concede {
			return 0, nil
		}
		if input.Kind != Bet {
			return 0, ErrInput
		}
		c, err := g.handChoice()
		if err != nil {
			return 0, err
		}
		switch input.Bet.Kind {
		case covenant.Check:
			if c.CanCheck {
				return 0, nil
			}
		case covenant.Call:
			if c.CanCall {
				return c.CallAmount, nil
			}
		case covenant.RaiseTo:
			if c.MaxRaiseTo > 0 && input.Bet.Amount >= c.MinRaiseTo && input.Bet.Amount <= c.MaxRaiseTo {
				state, err := covenant.ReadState(g.hand.accepted.Transaction)
				if err != nil {
					return 0, err
				}
				return input.Bet.Amount - int64(wager(*state, g.setup.role)), nil
			}
		}
		return 0, ErrInput
	case StageBoardRevealLocal, StageAllInRevealLocal, StageShowdownLocal, StageSettleShowdown:
		return 0, nil
	case StageAwaitPlayer1Funding, StageAwaitPlayer2Opening, StageBettingOpponent, StageBoardRevealOpponent, StageAllInOpponent, StageAllInRevealOpponent, StageShowdownOpponent:
		if input.Kind == ClaimTimeout {
			return 0, nil
		}
	}
	return 0, ErrInput
}

// PrepareAction performs only the authorized local builder/proof work, retaining
// all exact funding inputs and witnesses. External observation, wallet selection,
// durable appends, signing and submission remain sequential driver operations.
func (g *Game) PrepareAction(ctx context.Context, entropy io.Reader, input Input, funding covenant.Funding, at covenant.UnixSeconds) (Event, error) {
	if ctx == nil {
		return Event{}, ErrInput
	}
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if _, err := g.Decide(input); err != nil {
		return Event{}, err
	}
	choice, err := g.handChoice()
	if err != nil {
		return Event{}, err
	}
	if choice != nil && input.Kind == Progress {
		return Event{}, ErrInput
	}
	required, err := g.RequiredFunding(input)
	if err != nil {
		return Event{}, err
	}
	if required == 0 && (len(funding.Inputs) != 0 || funding.Change != nil) {
		return Event{}, ErrAmount
	}
	a := PreparedAction{}
	switch g.stage {
	case StageInitialDeposit:
		a.Kind = ActionInitialDeposit
	case StagePlayer1Funding:
		a.Kind = ActionPlayer1Funding
	case StagePlayer2Opening:
		a.Kind = ActionPlayer2Opening
		a.Bet = input.Bet
	case StageBettingLocal:
		a.Kind = ActionBetting
		a.Bet = input.Bet
	case StageBoardRevealLocal:
		a.Kind = ActionBoardReveal
	case StageAllInLocal:
		a.Kind = ActionAllInCall
	case StageAllInRevealLocal:
		a.Kind = ActionAllInReveal
	case StageShowdownLocal:
		a.Kind = ActionShowdownReveal
	case StageSettleShowdown:
		a.Kind = ActionShowdown
		a.Showdown = &g.evaluated.witness
		a.ObservedAt = at
	default:
		if input.Kind == ClaimTimeout {
			a.Kind = ActionTimeout
			a.ObservedAt = at
		} else {
			return Event{}, ErrInput
		}
	}
	if input.Kind == Concede {
		a = PreparedAction{Kind: ActionConcession}
	}
	if a.Kind == ActionInitialDeposit || a.Kind == ActionPlayer1Funding {
		if err := deadlineWindow(at, g.setup.params.InitialDeadline); err != nil {
			return Event{}, err
		}
	}
	switch a.Kind {
	case ActionInitialDeposit, ActionPlayer1Funding, ActionPlayer2Opening, ActionBetting, ActionAllInCall:
		a.Funding = &funding
	}
	switch a.Kind {
	case ActionPlayer1Funding, ActionPlayer2Opening, ActionBoardReveal, ActionShowdownReveal, ActionAllInCall, ActionAllInReveal:
		r, err := g.makeReveals(ctx, entropy)
		if err != nil {
			return Event{}, err
		}
		a.Reveals = &r
	}
	built, err := g.buildAction(a)
	if err != nil {
		return Event{}, err
	}
	saved, err := g.savedAction(a, built)
	if err != nil {
		return Event{}, err
	}
	e := g.NewEvent(SpendPrepared)
	e.Spend = &saved
	// Replay's complete admission must succeed before returning work that may be
	// signed or disclose local shares. This candidate never changes the game.
	candidate := *g
	if err := candidate.reduceHand(e); err != nil {
		return Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	return e, nil
}
