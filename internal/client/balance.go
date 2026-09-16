package client

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"time"

	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/wallet"
)

// BalanceUpdate is transient wallet information, independent of game updates.
// Err means the balance is unavailable until a successful refresh arrives.
type BalanceUpdate struct {
	Sats int64
	Err  error
}

func startBalance(ctx context.Context, services wallet.Services, subscriber ports.ScriptSubscriber, script []byte, updates chan BalanceUpdate) func() {
	life, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchBalance(life, services, subscriber, bytes.Clone(script), updates)
	}()
	return func() { cancel(); <-done }
}

func watchBalance(ctx context.Context, services wallet.Services, subscriber ports.ScriptSubscriber, script []byte, updates chan BalanceUpdate) {
	// Only this goroutine writes. Keep the latest observation without letting a
	// busy or stopped UI block stream consumption or session shutdown.
	publish := func(update BalanceUpdate) {
		select {
		case updates <- update:
		default:
			select {
			case <-updates:
			default:
			}
			updates <- update
		}
	}
	refresh := func() error {
		query, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		sats, err := wallet.Balance(query, services.Indexer, script, services.Now)
		if err == nil {
			publish(BalanceUpdate{Sats: sats})
		}
		return err
	}
	attached := func() error {
		if subscriber == nil {
			return errors.New("wallet balance subscription unavailable")
		}
		sub, err := subscriber.Subscribe(ctx, [][]byte{bytes.Clone(script)})
		if err != nil {
			return err
		}
		defer sub.Close()
		// Attach before querying so a change during the fetch stays queued.
		if err := refresh(); err != nil {
			return err
		}
		for {
			event, err := sub.Next(ctx)
			if err != nil {
				return err
			}
			switch event.Kind {
			case ports.ScriptChanged:
				if err := refresh(); err != nil {
					return err
				}
			case ports.ScriptObservationGap:
				return errors.New("wallet balance subscription interrupted")
			}
		}
	}
	for ctx.Err() == nil {
		err := attached()
		if ctx.Err() != nil {
			return
		}
		publish(BalanceUpdate{Err: err})
		slog.Warn("Wallet balance unavailable; retrying", "error", err)
		// These read-only retries do not restart or advance the game driver.
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
