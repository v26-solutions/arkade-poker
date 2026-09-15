package game

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"arkade-poker/go/internal/covenant"
)

func TestDriverCompleteOrdinaryHandRealEmulator(t *testing.T)   { runDriverHand(t, false, -1) }
func TestDriverSetupThroughSettlementRealEmulator(t *testing.T) { runDriverHand(t, true, -1) }
func TestDriverEitherPlayerAllInRealEmulator(t *testing.T) {
	for i := 0; i < 2; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) { runDriverHand(t, false, i) })
	}
}
func runDriverHand(t *testing.T, fresh bool, allInPlayer int, configure ...func(*fixtureDriverWallet)) {
	f := newDriverFixture(t)
	var drivers [2]*Driver
	var wallets [2]*fixtureDriverWallet
	var logs [2]*journalLog
	var updates [2]chan Update
	var inputs [2]chan Input
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	done := make(chan error, 2)
	for i := range drivers {
		records := f.h.logs[i]
		if fresh {
			records = nil
		}
		drivers[i], wallets[i], _, logs[i] = f.newDriver(t, i, records)
		for _, apply := range configure {
			apply(wallets[i])
		}
		updates[i] = make(chan Update, 128)
		inputs[i] = make(chan Input, 8)
		go func(i int) { done <- drivers[i].Run(ctx, inputs[i], updates[i]) }(i)
	}
	var last [2]string
	finished := 0
	joined, started, raised := false, false, false
	consume := func(i int, u Update) {
		if u.Err != nil {
			t.Fatal(u.Err)
		}
		if fresh && i == 0 && u.Snapshot.Invitation != nil && !joined {
			inv := *u.Snapshot.Invitation
			inputs[1] <- Input{Kind: JoinSession, Invitation: &inv}
			joined = true
		}
		if u.Snapshot.Outcome != nil {
			return
		}
		if u.Snapshot.Choice == nil {
			return
		}
		if fresh && u.Snapshot.Stage == StageInit {
			if i == 0 && !started {
				inputs[0] <- Input{Kind: StartSession, Terms: testTerms, RelayURL: "ws://fixture.invalid"}
				started = true
			}
			return
		}
		key := fmt.Sprintf("%d:%v", u.Snapshot.Stage, u.Snapshot.State)
		if key == last[i] {
			return
		}
		last[i] = key
		choice := u.Snapshot.Choice
		input := Input{Kind: RevealShowdown}
		for _, kind := range choice.Allowed {
			if kind == Bet {
				input = Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}}
				if choice.CanCall {
					input.Bet.Kind = covenant.Call
				}
				if i == allInPlayer && !raised && choice.MaxRaiseTo > 0 {
					input.Bet = covenant.BettingAction{Kind: covenant.RaiseTo, Amount: choice.MaxRaiseTo}
					raised = true
				}
				break
			}
		}
		inputs[i] <- input
	}
	for finished < 2 {
		select {
		case <-ctx.Done():
			t.Fatal("driver timeout", ctx.Err())
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			finished++
		case u := <-updates[0]:
			consume(0, u)
		case u := <-updates[1]:
			consume(1, u)
		}
	}
	for i, d := range drivers {
		if d.journal.game.stage != StageFinished || d.journal.game.outcome.Transaction == nil {
			t.Fatal("no accepted driver outcome")
		}
		restored, err := loadJournal(context.Background(), d.config.Game, logs[i])
		if err != nil {
			t.Fatal(err)
		}
		if restored.game.previous != d.journal.game.previous || restored.game.stage != StageFinished {
			t.Fatal("driver log replay")
		}
		discardGame(restored.game, nil)
		if wallets[i].signCalls == 0 || wallets[i].submitCalls == 0 || wallets[i].retryCalls != 0 {
			t.Fatal("missing normal effects or unexpected recovery")
		}
	}
}

func TestDriverSequentialOwnershipAndClose(t *testing.T) {
	f := newDriverFixture(t)
	d, _, _, log := f.newDriver(t, 0, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan Update, 8)
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, nil, updates) }()
	for {
		select {
		case u := <-updates:
			if !u.Shuffling {
				goto started
			}
		case <-time.After(5 * time.Second):
			t.Fatal("driver did not start")
		}
	}
started:
	if err := d.Restore(ctx); !errors.Is(err, ErrDriverBusy) {
		t.Fatalf("concurrent restore: %v", err)
	}
	if err := d.Run(ctx, nil, nil); !errors.Is(err, ErrDriverBusy) {
		t.Fatalf("concurrent run: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("close result: %v", err)
	}
	if len(log.records) != 1 {
		t.Fatal("close invented a game action")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Restore(ctx); !errors.Is(err, ErrDriverClosed) {
		t.Fatal("closed driver reopened")
	}
}
