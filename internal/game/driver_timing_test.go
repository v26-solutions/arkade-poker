package game

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"arkade-poker/go/internal/covenant"
)

func TestDriverAcknowledgedRetryKeepsReferenceInterval(t *testing.T) {
	f := newDriverFixture(t)
	f.h.start(0)
	f.savePending(t, 0, Input{Kind: Concede}, false)
	f.importHistory(t)
	d, w, _, _ := f.newDriver(t, 0, f.h.logs[0])
	w.delayAcceptance = true
	if err := d.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 2 { // First signature and acknowledged submission.
		step, err := d.step(context.Background(), Input{Kind: Progress})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.commit(context.Background(), *step.Event); err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []int64{1000, 1004, 1005, 1009, 1010} {
		d.config.Now = func() time.Time { return time.Unix(at, 0) }
		step, err := d.step(context.Background(), Input{Kind: Progress})
		if err != nil {
			t.Fatal(err)
		}
		if at == 1005 || at == 1010 {
			if step.Event == nil || step.Event.Receipt.Recovery {
				t.Fatal("ordinary acknowledgement became recovery")
			}
			if err := d.commit(context.Background(), *step.Event); err != nil {
				t.Fatal(err)
			}
		} else if step.Kind != Waiting {
			t.Fatal("retry before five seconds")
		}
	}
	if w.submitCalls != 3 || w.retryCalls != 0 || w.signCalls != 1 || w.selectCalls != 0 {
		t.Fatal("ordinary retries changed exact saved work")
	}
}

func TestDriverFundingFreshnessBeforeEachEffect(t *testing.T) {
	for _, player := range []int{0, 1} {
		for _, boundary := range []string{"prepare", "sign", "submit", "recovery"} {
			t.Run(fmt.Sprintf("%d/%s", player, boundary), func(t *testing.T) {
				f := newDriverFixture(t)
				if player == 0 {
					f.h.action(1, Input{Kind: Progress})
				}
				if boundary == "recovery" {
					f.savePending(t, player, Input{Kind: Progress}, true)
				}
				f.importHistory(t)
				d, w, _, _ := f.newDriver(t, player, f.h.logs[player])
				// Reject funding as soon as fewer than 30 seconds remain, even
				// though the covenant timeout itself has not matured yet.
				deadline := f.h.games[player].setup.params.InitialDeadline
				late := func() time.Time { return time.Unix(int64(deadline)-29, 0) }
				if boundary == "recovery" {
					d.config.Now = late
					if err := d.Restore(context.Background()); !errors.Is(err, ErrDeadline) {
						t.Fatal("late recovery", err)
					}
					if w.retryCalls+w.signCalls+w.selectCalls != 0 {
						t.Fatal("late recovery performed effects")
					}
					return
				}
				if err := d.Restore(context.Background()); err != nil {
					t.Fatal(err)
				}
				if boundary != "prepare" {
					step, err := d.step(context.Background(), Input{Kind: Progress})
					if err != nil {
						t.Fatal(err)
					}
					if err := d.commit(context.Background(), *step.Event); err != nil {
						t.Fatal(err)
					}
				}
				if boundary == "submit" {
					step, err := d.step(context.Background(), Input{Kind: Progress})
					if err != nil {
						t.Fatal(err)
					}
					if err := d.commit(context.Background(), *step.Event); err != nil {
						t.Fatal(err)
					}
				}
				signs, selections := w.signCalls, w.selectCalls
				d.config.Now = late
				step, err := d.step(context.Background(), Input{Kind: Progress})
				if boundary == "prepare" {
					if err != nil || step.Event == nil || step.Event.Kind != SetupAborted {
						t.Fatal("late funding not aborted", err)
					}
				} else if !errors.Is(err, ErrDeadline) {
					t.Fatal("late saved funding effect", err)
				}
				if w.signCalls != signs || w.selectCalls != selections || w.submitCalls+w.retryCalls != 0 {
					t.Fatal("effect crossed expired deadline")
				}
			})
		}
	}
}

func TestDriverDelayedSetupFundingRealEmulator(t *testing.T) {
	f := newDriverFixture(t)
	ctx := context.Background()
	p1, w1, _, _ := f.newDriver(t, 0, setupPrefix(t, 0, StageFinalShufflePrepared))
	p2, w2, _, _ := f.newDriver(t, 1, setupPrefix(t, 1, StageAwaitFinalShuffle))
	for _, d := range []*Driver{p1, p2} {
		if err := d.Restore(ctx); err != nil {
			t.Fatal(err)
		}
	}
	advance := func(d *Driver, at int64, want EventKind) {
		t.Helper()
		d.config.Now = func() time.Time { return time.Unix(at, 0) }
		f.h.at = covenant.UnixSeconds(at) // Also advance the actual emulator clock.
		step, err := d.step(ctx, Input{Kind: Progress})
		if err != nil || step.Event == nil || step.Event.Kind != want {
			t.Fatalf("at %d: got step %+v, error %v; want event %v", at, step, err, want)
		}
		if err := d.commit(ctx, *step.Event); err != nil {
			t.Fatal(err)
		}
	}
	// The final proof was generated at 1000. Delay publication and delivery past
	// the entire old setup timeout, then let both players fund progressively.
	advance(p1, 1060, PublicationPrepared)
	advance(p1, 1070, MessagePublished)
	advance(p2, 1120, MessageReceived)
	p1.config.Now = func() time.Time { return time.Unix(1150, 0) }
	step, err := p1.step(ctx, Input{Kind: Progress})
	if err != nil || step.Kind != Waiting {
		t.Fatalf("creator stopped waiting for delayed deposit: %+v, %v", step, err)
	}
	advance(p2, 1180, SpendPrepared)
	advance(p2, 1195, SpendSigned)
	advance(p2, 1210, SubmissionAttempted)
	advance(p2, 1220, DepositObserved)
	advance(p1, 1230, DepositObserved)
	advance(p1, 1240, SpendPrepared)
	advance(p1, 1250, SpendSigned)
	advance(p1, 1270, SubmissionAttempted) // Exactly 30 seconds remain.
	advance(p1, 1280, SpendObserved)
	advance(p2, 1280, SpendObserved)
	for _, d := range []*Driver{p1, p2} {
		state, err := covenant.ReadState(d.journal.game.hand.accepted.Transaction)
		if err != nil {
			t.Fatal(err)
		}
		if state.Deadline != 1360 {
			t.Fatalf("first live deadline = %d, want initial 1300 + 60", state.Deadline)
		}
		restored, err := loadJournal(ctx, d.config.Game, d.config.Log)
		if err != nil {
			t.Fatal(err)
		}
		if restored.game.previous != d.journal.game.previous || restored.game.stage != d.journal.game.stage {
			t.Fatal("delayed funding log did not replay")
		}
		discardGame(restored.game, nil)
	}
	for _, w := range []*fixtureDriverWallet{w1, w2} {
		if w.selectCalls != 1 || w.signCalls != 1 || w.submitCalls != 1 || w.retryCalls != 0 {
			t.Fatal("delayed funding did not complete exactly once")
		}
	}
}

func TestDriverTimeoutReconcilesBeforeMaturity(t *testing.T) {
	f := newDriverFixture(t)
	f.h.start(0)
	f.importHistory(t)
	d, w, _, _ := f.newDriver(t, 1, f.h.logs[1])
	if err := d.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := covenant.ReadState(d.journal.game.hand.accepted.Transaction)
	if err != nil {
		t.Fatal(err)
	}
	d.config.Now = func() time.Time { return time.Unix(int64(state.Deadline)-1, 0) }
	step, err := d.step(context.Background(), Input{Kind: ClaimTimeout})
	if err != nil || step.Kind != Waiting {
		t.Fatal("immature timeout", err)
	}
	d.config.Now = func() time.Time { return time.Unix(int64(state.Deadline), 0) }
	step, err = d.step(context.Background(), Input{Kind: Progress})
	if err != nil || step.Kind != Waiting || step.Choice == nil || len(step.Choice.Allowed) != 1 || step.Choice.Allowed[0] != ClaimTimeout {
		t.Fatal("mature timeout missing from input projection", err)
	}
	step, err = d.step(context.Background(), Input{Kind: ClaimTimeout})
	if err != nil || step.Event == nil || step.Event.Spend.Action.Kind != ActionTimeout {
		t.Fatal("mature timeout", err)
	}
	if w.signCalls+w.selectCalls+w.submitCalls != 0 {
		t.Fatal("timeout bypassed persistence")
	}
	if err := d.commit(context.Background(), *step.Event); err != nil {
		t.Fatal(err)
	}
}
