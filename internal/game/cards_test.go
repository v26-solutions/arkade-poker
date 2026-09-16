package game

import (
	"context"
	"reflect"
	"testing"

	"arkade-poker/go/internal/covenant"
)

func cardSnapshot(t *testing.T, g *Game) Snapshot {
	t.Helper()
	s, err := g.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Compare the locally prepared view with the same reveal accepted by the real
// VM, while keeping the prepared branch available for pending/recovery checks.
func preparedReveal(t *testing.T, h *handHarness, input Input) *handHarness {
	t.Helper()
	i := h.actor()
	branch := h.fork()
	g := branch.games[i]
	before := cardSnapshot(t, g)
	opponentBefore := cardSnapshot(t, branch.games[1-i])
	accepted := g.hand.accepted.Transaction.TxHash()
	reveals := g.hand.reveals
	amount, err := g.RequiredFunding(input)
	if err != nil {
		t.Fatal(err)
	}
	e, err := g.PrepareAction(context.Background(), nil, input, branch.funding(i, amount), branch.at)
	if err != nil {
		t.Fatal(err)
	}
	if got := cardSnapshot(t, g); !reflect.DeepEqual(got, before) {
		t.Fatal("unrecorded reveal changed the table")
	}
	branch.record(i, e)
	got := cardSnapshot(t, g)
	h.action(i, input)
	want := cardSnapshot(t, h.games[i])
	if got.Cards != want.Cards {
		t.Fatalf("prepared reveal cards differ from accepted reveal: got %+v, want %+v", got.Cards, want.Cards)
	}
	if got.Stage != StageTransactionPrepared || !reflect.DeepEqual(got.State, before.State) || got.Choice != nil || got.Outcome != nil {
		t.Fatal("displaying prepared cards advanced the game")
	}
	if g.hand.accepted.Transaction.TxHash() != accepted || g.hand.reveals != reveals {
		t.Fatal("displaying prepared cards changed accepted evidence")
	}
	if peer := cardSnapshot(t, branch.games[1-i]); !reflect.DeepEqual(peer, opponentBefore) {
		t.Fatal("local preparation changed the opponent's view")
	}
	return branch
}

func TestPreparedBoardCardsVisibleBeforeSubmission(t *testing.T) {
	h := newHandHarness(t)
	h.start(0)
	for street := covenant.Flop; street <= covenant.River; street++ {
		for {
			state := cardSnapshot(t, h.games[0]).State
			if state.Phase.Kind != covenant.Betting {
				break
			}
			h.action(h.actor(), Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}})
		}
		// The first revealer still lacks the opponent's shares. The second can
		// display this street as soon as their own reveal has been recorded.
		preparedReveal(t, h, Input{Kind: Progress})
		i := h.actor()
		branch := preparedReveal(t, h, Input{Kind: Progress})
		g := branch.games[i]
		before := cardSnapshot(t, g)
		if _, err := g.PrepareEvaluation(context.Background()); err == nil {
			t.Fatal("prepared cards permitted showdown evaluation")
		}
		saved, err := g.PendingSpend()
		if err != nil {
			t.Fatal(err)
		}
		saved.Signed = &saved.Prepared
		e := g.NewEvent(SpendSigned)
		e.Spend = &saved
		branch.record(i, e)
		e = g.NewEvent(SubmissionAttempted)
		e.Receipt = &SubmissionReceipt{} // An uncertain submission is not acceptance.
		e.ObservedAt = branch.at
		branch.record(i, e)
		pending := cardSnapshot(t, g)
		if pending.Cards != before.Cards || !reflect.DeepEqual(pending.State, before.State) || pending.Choice != nil || pending.Outcome != nil {
			t.Fatal("pending submission changed cards or advanced the game")
		}
		restored := replayBytes(t, g.config, branch.logs[i])
		if got := cardSnapshot(t, restored); !reflect.DeepEqual(got, pending) {
			t.Fatal("replay lost the prepared card display")
		}
		// A competing accepted timeout preserves cards already visible locally,
		// without disclosing any additional cards or treating the reveal as accepted.
		branch.at = before.State.Deadline
		branch.action(1-i, Input{Kind: ClaimTimeout})
		after := cardSnapshot(t, g)
		if after.Cards != before.Cards || after.Outcome == nil || after.Outcome.Settlement.Kind != SettlementTimeout {
			t.Fatal("competing timeout lost the visible table")
		}
		restored = replayBytes(t, g.config, branch.logs[i])
		if got := cardSnapshot(t, restored); !reflect.DeepEqual(got, after) {
			t.Fatal("replay lost the completed card display")
		}
	}
}

func TestPreparedAllInCardsVisibleBeforeSubmission(t *testing.T) {
	for _, bettor := range []int{0, 1} {
		h := newHandHarness(t)
		opening := int64(0)
		if bettor == 1 {
			opening = testTerms.MaxWager
		}
		h.start(opening)
		if bettor == 0 {
			h.action(bettor, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.RaiseTo, Amount: testTerms.MaxWager}})
		}
		preparedReveal(t, h, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Call}})
		branch := preparedReveal(t, h, Input{Kind: RevealShowdown})
		g := branch.games[bettor]
		for _, card := range dealOrder(cardSnapshot(t, g).Cards) {
			if !card.Known {
				t.Fatal("prepared all-in reveal left a decodable card hidden")
			}
		}
		if _, err := g.PrepareEvaluation(context.Background()); err == nil {
			t.Fatal("unaccepted all-in reveal permitted settlement evaluation")
		}
	}
}
