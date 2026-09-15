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
				late := func() time.Time { return time.Unix(5000, 0) }
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
