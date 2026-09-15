package game

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

func TestDriverRestoreExactRetryAndFailure(t *testing.T) {
	for _, mode := range []string{"accepted", "retry_error", "indexer_error", "ack_before_indexing", "missing", "pending", "unavailable", "unsigned"} {
		t.Run(mode, func(t *testing.T) {
			f := newDriverFixture(t)
			f.h.start(0)
			saved := f.savePending(t, 0, Input{Kind: Concede}, mode != "unsigned")
			f.importHistory(t)
			d, w, _, log := f.newDriver(t, 0, f.h.logs[0])
			d.config.Entropy = unexpectedEntropy{}
			fault := errors.New("injected failure")
			source := *saved.Source
			switch mode {
			case "retry_error":
				w.retryErr = fault
			case "indexer_error":
				f.queryErr = fault
			case "ack_before_indexing":
				w.delayAcceptance = true
			case "missing":
				delete(f.records, source)
			case "pending":
				v := f.records[source]
				cp, id := chainhash.Hash{91}, chainhash.Hash{92}
				v.Spent = true
				v.SpentBy = &cp
				v.ArkTxID = &id
				f.records[source] = v
			case "unavailable":
				v := f.records[source]
				id := chainhash.Hash{93}
				v.Spent = true
				v.SettledBy = &id
				f.records[source] = v
			}
			err := d.Restore(context.Background())
			if mode == "retry_error" || mode == "indexer_error" {
				if !errors.Is(err, fault) {
					t.Fatalf("restore failure: %v", err)
				}
				if err := d.Run(context.Background(), nil, nil); !errors.Is(err, ErrDriverStopped) {
					t.Fatalf("failure restarted advancement: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			wantRetry := 0
			if mode == "accepted" || mode == "retry_error" || mode == "ack_before_indexing" {
				wantRetry = 1
			}
			if w.retryCalls != wantRetry || w.submitCalls != 0 || w.signCalls != 0 || w.selectCalls != 0 {
				t.Fatalf("recovery effects: retry=%d submit=%d sign=%d select=%d", w.retryCalls, w.submitCalls, w.signCalls, w.selectCalls)
			}
			if mode == "accepted" {
				if d.journal.game.stage != StageFinished {
					t.Fatal("accepted retry did not finish")
				}
				return
			}
			got, e := d.journal.game.PendingSpend()
			if e != nil || !sameSaved(saved, got) {
				t.Fatalf("lost saved work: %v", e)
			}
			if saved.Signed != nil && (got.Signed == nil || !sameBundle(*saved.Signed, *got.Signed)) {
				t.Fatal("changed signed work")
			}
			if d.journal.game.outcome != nil {
				t.Fatal("failure created terminal outcome")
			}
			if mode == "retry_error" {
				e, err := DecodeEvent(log.records[len(log.records)-1])
				if err != nil || e.Kind != SubmissionFailed || e.FailureCode != "retry_failed" {
					t.Fatal("retry failure not recorded", err)
				}
				if err := d.Close(); err != nil {
					t.Fatal(err)
				}
				// A new explicit recovery attempt can try this exact durable work once.
				next, w2, _, _ := f.newDriver(t, 0, log.records)
				w2.retryErr = fault
				if err := next.Restore(context.Background()); !errors.Is(err, fault) || w2.retryCalls != 1 || w2.signCalls != 0 {
					t.Fatal("new recovery attempt", err)
				}
			}
			if mode == "ack_before_indexing" || mode == "missing" || mode == "pending" || mode == "unavailable" {
				for range 3 {
					if err := d.recover(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				if w.retryCalls != wantRetry {
					t.Fatal("unbounded retry or non-unspent retry")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				if err := d.Run(ctx, nil, nil); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("pending recovery wait: %v", err)
				}
				if w.retryCalls != wantRetry {
					t.Fatal("Run repeated recovery retry")
				}
			}
			if mode == "unsigned" {
				// First signing after durable preparation is not a retry. No selection or
				// replacement action/proofs may occur, and existing signed work is never signed again.
				if err := d.Run(context.Background(), nil, nil); err != nil {
					t.Fatal(err)
				}
				if w.signCalls != 1 || w.submitCalls != 1 || w.retryCalls != 0 || w.selectCalls != 0 || d.journal.game.stage != StageFinished {
					t.Fatal("unsigned work did not resume its first signing")
				}
			}
		})
	}
}

func TestDriverRestoreAcceptedAndOpponentCatchup(t *testing.T) {
	f := newDriverFixture(t)
	f.h.start(0)
	f.h.action(0, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}})
	// Intact local prefix ends at signed work, before receiving submit ACK.
	prefix := append([][]byte(nil), f.h.logs[0][:len(f.h.logs[0])-2]...)
	f.h.action(1, Input{Kind: Progress}) // Accepted opponent's first flop reveal.
	f.importHistory(t)
	d, w, _, log := f.newDriver(t, 0, prefix)
	d.config.Entropy = unexpectedEntropy{}
	if err := d.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.journal.game.stage != StageBoardRevealLocal || d.recovering {
		t.Fatal("did not follow accepted response to local turn")
	}
	if w.retryCalls+w.submitCalls+w.signCalls+w.selectCalls != 0 {
		t.Fatal("repeated completed local work")
	}
	if len(log.records) != len(prefix)+2 {
		t.Fatalf("did not record every accepted edge: %d", len(log.records)-len(prefix))
	}
	if d.watch == nil {
		t.Fatal("did not keep watching the resulting covenant")
	}
	for _, b := range log.records[len(prefix):] {
		e, err := DecodeEvent(b)
		if err != nil || e.Kind != SpendObserved {
			t.Fatal("catchup not accepted source edges", err)
		}
	}
}

func TestDriverRestoreCompetingSpendAndWrongWallet(t *testing.T) {
	f := newDriverFixture(t)
	f.h.start(0)
	f.savePending(t, 0, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}}, true)
	prefix := append([][]byte(nil), f.h.logs[0]...)
	state, _ := covenant.ReadState(f.h.games[1].hand.accepted.Transaction)
	f.h.at = state.Deadline
	f.h.action(1, Input{Kind: ClaimTimeout})
	f.importHistory(t)
	d, w, _, _ := f.newDriver(t, 0, prefix)
	if err := d.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.journal.game.stage != StageFinished || d.journal.game.outcome.Kind != Lost || w.retryCalls+w.signCalls+w.selectCalls != 0 {
		t.Fatal("competing accepted timeout was not authoritative")
	}
	wrong, ww, peer, _ := f.newDriver(t, 1, prefix)
	before := len(f.trace)
	if err := wrong.Restore(context.Background()); !errors.Is(err, ErrWalletIdentity) {
		t.Fatalf("wrong re-imported wallet: %v", err)
	}
	if len(f.trace) != before || ww.retryCalls+ww.signCalls+ww.selectCalls+peer.openCalls != 0 {
		t.Fatal("wrong wallet caused effects")
	}
}

func TestDriverUncertainSubmissionReconcilesBeforeRetry(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_accepted", true: "accepted_response_lost"}[accepted], func(t *testing.T) {
			f := newDriverFixture(t)
			f.h.start(0)
			f.savePending(t, 0, Input{Kind: Concede}, true)
			f.importHistory(t)
			d, w, _, log := f.newDriver(t, 0, f.h.logs[0])
			// Load an ordinary live signed state, rather than starting a restore retry.
			j, err := loadJournal(context.Background(), d.config.Game, log)
			if err != nil {
				t.Fatal(err)
			}
			d.journal = j
			d.loaded = true
			w.submitErr = errors.New("lost submit response")
			w.acceptBeforeError = accepted
			if err := d.Run(context.Background(), nil, nil); err != nil {
				t.Fatal(err)
			}
			expected := 1
			if accepted {
				expected = 0
			}
			if w.submitCalls != 1 || w.retryCalls != expected || w.signCalls != 0 || w.selectCalls != 0 {
				t.Fatal("uncertain submission repeated wrong effects")
			}
			if d.journal.game.stage != StageFinished {
				t.Fatal("uncertain submission not reconciled")
			}

		})
	}
}

func TestDriverFailedDurableWritesStopEffects(t *testing.T) {
	for _, kind := range []EventKind{SpendPrepared, SpendSigned, SubmissionAttempted, SpendObserved} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			f := newDriverFixture(t)
			f.h.start(0)
			f.importHistory(t)
			d, w, _, log := f.newDriver(t, 0, f.h.logs[0])
			fault := errors.New("durable append failed")
			log.before = func(_ uint64, b []byte) {
				e, err := DecodeEvent(b)
				if err != nil {
					t.Fatal(err)
				}
				if e.Kind == kind {
					log.fail = fault
				}
			}
			inputs := make(chan Input, 1)
			inputs <- Input{Kind: Concede}
			if err := d.Run(context.Background(), inputs, nil); !errors.Is(err, fault) {
				t.Fatalf("append failure: %v", err)
			}
			switch kind {
			case SpendPrepared:
				if w.signCalls != 0 || w.submitCalls != 0 {
					t.Fatal("effects after failed preparation")
				}
			case SpendSigned:
				if w.signCalls != 1 || w.submitCalls != 0 {
					t.Fatal("effects after failed signing record")
				}
			default:
				if w.submitCalls != 1 {
					t.Fatal("missing prior external effect")
				}
			}
			if d.journal.game.stage == StageFinished {
				t.Fatal("failure advanced to terminal")
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == SubmissionAttempted || kind == SpendObserved {
				log.fail = nil
				next, w2, _, _ := f.newDriver(t, 0, log.records)
				if err := next.Restore(context.Background()); err != nil {
					t.Fatal(err)
				}
				if next.journal.game.stage != StageFinished || w2.retryCalls+w2.signCalls+w2.selectCalls != 0 {
					t.Fatal("lost durable acknowledgement repeated accepted work")
				}
			}
		})
	}
}

var _ ports.Indexer = (*driverFixture)(nil)

func TestDriverRecoveryChecksEveryFundingSource(t *testing.T) {
	for _, mode := range []string{"deposit", "player1_funding", "multi_input_missing", "multi_input_unspent"} {
		t.Run(mode, func(t *testing.T) {
			f := newDriverFixture(t)
			i := 0
			if mode == "deposit" {
				i = 1
			} else if mode == "player1_funding" {
				f.h.action(1, Input{Kind: Progress})
			} else {
				f.h.start(0)
			}
			if mode == "deposit" || mode == "player1_funding" {
				f.savePending(t, i, Input{Kind: Progress}, true)
			} else {
				g := f.h.games[i]
				a, b := f.h.funding(i, 6), f.h.funding(i, 4)
				a.Inputs = append(a.Inputs, b.Inputs...)
				e, err := g.PrepareAction(context.Background(), nil, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.RaiseTo, Amount: 10}}, a, f.h.at)
				if err != nil {
					t.Fatal(err)
				}
				f.h.record(i, e)
				saved, _ := g.PendingSpend()
				saved.Signed = &saved.Prepared
				e = g.NewEvent(SpendSigned)
				e.Spend = &saved
				f.h.record(i, e)
			}
			f.importHistory(t)
			saved, _ := f.h.games[i].PendingSpend()
			if mode == "multi_input_missing" {
				delete(f.records, saved.WalletSources[1])
			}
			d, w, _, _ := f.newDriver(t, i, f.h.logs[i])
			d.config.Entropy = unexpectedEntropy{}
			if err := d.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if mode == "multi_input_missing" {
				if w.retryCalls != 0 || !d.recovering {
					t.Fatal("first unspent source authorized retry with missing second source")
				}
			} else if w.retryCalls != 1 || d.journal.game.prepared != nil {
				t.Fatal("exact funded retry was not accepted")
			}
			if w.signCalls+w.selectCalls+w.submitCalls != 0 {
				t.Fatal("recovery replaced funded work")
			}
		})
	}
}

func TestDriverFailedReplayClearsProgressWithoutEffects(t *testing.T) {
	f := newDriverFixture(t)
	d, w, p, _ := f.newDriver(t, 0, [][]byte{{1, 2, 3}})
	updates := make(chan Update, 4)
	if err := d.Run(context.Background(), nil, updates); err == nil {
		t.Fatal("invalid log ran")
	}
	first, last := <-updates, <-updates
	if !first.Shuffling || last.Shuffling || last.Err == nil {
		t.Fatal("failed replay left progress active")
	}
	if len(f.trace) != 0 || w.signCalls+w.retryCalls+w.submitCalls+p.openCalls != 0 {
		t.Fatal("failed replay caused effects")
	}
}
