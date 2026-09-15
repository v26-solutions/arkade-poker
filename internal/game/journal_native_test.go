//go:build !js

package game

import (
	"context"
	"errors"
	"testing"

	"arkade-poker/go/internal/storage"
)

func TestJournalNativeRestartExactWork(t *testing.T) {
	ctx := context.Background()
	h := newHandHarness(t)
	h.action(1, Input{Kind: Progress})
	g := h.games[0]
	e, err := g.PrepareAction(ctx, nil, Input{Kind: Progress}, h.funding(0, 1100), h.at)
	if err != nil {
		t.Fatal(err)
	}
	h.record(0, e)
	saved, err := g.PendingSpend()
	if err != nil {
		t.Fatal(err)
	}
	saved.Signed = &saved.Prepared
	e = g.NewEvent(SpendSigned)
	e.Spend = &saved
	h.record(0, e)
	directory := t.TempDir()
	log, err := storage.Open(ctx, directory, g.config.Wallet.WalletPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	j, err := loadJournal(ctx, g.config, log)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range h.logs[0] {
		e, err := DecodeEvent(b)
		if err != nil {
			t.Fatal(err)
		}
		err = j.append(ctx, e)
		if e.Secrets != nil {
			_ = e.Secrets.Destroy()
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := storage.Open(ctx, directory, g.config.Wallet.WalletPublicKey); !errors.Is(err, storage.ErrLocked) {
		t.Fatalf("live journal allowed duplicate writer: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	discardGame(j.game, nil)
	log, err = storage.Open(ctx, directory, g.config.Wallet.WalletPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	restored, err := loadJournal(ctx, g.config, log)
	if err != nil {
		t.Fatal(err)
	}
	defer discardGame(restored.game, nil)
	got, err := restored.game.PendingSpend()
	if err != nil || !sameSaved(saved, got) || got.Signed == nil || !sameBundle(*saved.Signed, *got.Signed) {
		t.Fatalf("native restore changed exact work: %v", err)
	}
	if restored.game.previous != g.previous || restored.game.nextEvent != g.nextEvent {
		t.Fatal("native restore changed event chain")
	}
}
