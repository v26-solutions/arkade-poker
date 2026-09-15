package game

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"arkade-poker/go/internal/ports"
)

func TestDriverUsesRuntimeConnectionsAfterWalletMatch(t *testing.T) {
	f := newDriverFixture(t)
	original, w, p, log := f.newDriver(t, 0, f.h.logs[0])
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	changed := original.config.Game
	changed.ArkdURL = "http://changed.invalid"
	changed.IndexerURL = "http://changed.invalid"
	calls := 0
	connect := func(ctx context.Context, current Config) (DriverConnections, error) {
		calls++
		if !equalConfig(current, changed) {
			t.Fatal("factory did not receive runtime configuration")
		}
		// A factory may retain/normalize its own argument without mutating the FSM.
		current.Wallet.Receive.Script[0] ^= 1
		return DriverConnections{Wallet: w, Indexer: f, Subscriptions: fixtureSubscriber{f, 0}, Transport: p}, nil
	}
	restored, err := NewDriver(DriverConfig{Game: changed, Log: log, Wallet: w, Connect: connect})
	if err != nil {
		t.Fatal(err)
	}
	w.driver = restored
	p.driver = restored
	t.Cleanup(func() { _ = restored.Close() })
	if err := restored.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !equalConfig(restored.journal.game.config, changed) || !equalConfig(restored.config.Game, changed) {
		t.Fatal("runtime configuration was not owned/preserved")
	}
	// The same factory cannot run before the re-imported wallet matches the log.
	wrong, ww, _, wrongLog := f.newDriver(t, 1, f.h.logs[0])
	_ = wrong.Close()
	rejected, err := NewDriver(DriverConfig{Game: wrong.config.Game, Log: wrongLog, Wallet: ww, Connect: connect})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rejected.Close() })
	if err := rejected.Restore(context.Background()); !errors.Is(err, ErrWalletIdentity) {
		t.Fatal("wrong identity passed factory gate", err)
	}
	if calls != 1 {
		t.Fatal("factory ran before wallet match")
	}
}

func TestDriverOwnsWalletArgumentsAndOpaqueSignatures(t *testing.T) {
	f := newDriverFixture(t)
	f.h.start(0)
	saved := f.savePending(t, 0, Input{Kind: Concede}, false)
	f.importHistory(t)
	d, w, _, log := f.newDriver(t, 0, f.h.logs[0])
	w.mutatePolicy = true
	w.signResult = func(b ports.Bundle) ports.Bundle {
		b.Ark = malformedScriptSignature(t, b.Ark)
		for i := range b.Checkpoints {
			b.Checkpoints[i] = malformedScriptSignature(t, b.Checkpoints[i])
		}
		return b
	}
	if err := d.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	payout := bytes.Clone(d.journal.game.setup.params.Players.Player1.PayoutScript)
	step, err := d.step(context.Background(), Input{Kind: Progress})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.commit(context.Background(), *step.Event); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payout, d.journal.game.setup.params.Players.Player1.PayoutScript) {
		t.Fatal("wallet mutated immutable agreement")
	}
	got, err := d.journal.game.PendingSpend()
	if err != nil {
		t.Fatal(err)
	}
	if !sameSaved(saved, got) || got.Signed == nil || sameBundle(got.Prepared, *got.Signed) {
		t.Fatal("signer mutated prepared work or opaque signature was discarded")
	}
	replayed, err := loadJournal(context.Background(), d.config.Game, log)
	if err != nil {
		t.Fatal(err)
	}
	defer discardGame(replayed.game, nil)
	again, err := replayed.game.PendingSpend()
	if err != nil || again.Signed == nil || !sameBundle(*got.Signed, *again.Signed) {
		t.Fatal("replay checked opaque signature validity", err)
	}
}
