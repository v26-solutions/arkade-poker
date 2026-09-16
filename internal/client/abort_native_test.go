//go:build !js

package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/storage"
	"arkade-poker/go/internal/wallet"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

type blockedSetupPeer struct {
	ports.PeerTransport
	started, stopped chan struct{}
}

func (p *blockedSetupPeer) Open(ctx context.Context, _ ports.PeerSession) error {
	close(p.started)
	<-ctx.Done()
	close(p.stopped)
	return ctx.Err()
}
func (*blockedSetupPeer) Close() error { return nil }

func TestAbortSetupDeletesFilesAndKeepsWallet(t *testing.T) {
	for _, role := range []covenant.Player{covenant.Player1, covenant.Player2} {
		t.Run(fmt.Sprint(role), func(t *testing.T) {
			cfg, gc, _, _ := fixture(t)
			dir := t.TempDir()
			key := importKey(t, 20)
			public := key.PublicKey()
			peer := &blockedSetupPeer{started: make(chan struct{}), stopped: make(chan struct{})}
			connect := cfg.Connect
			cfg.Connect = func(ctx context.Context, gc game.Config) (Connections, error) {
				conns, err := connect(ctx, gc)
				conns.Transport = peer
				return conns, err
			}
			cfg.Open = func(ctx context.Context, p [32]byte) (storage.Log, error) { return storage.Open(ctx, dir, p) }
			clears := 0
			cfg.Clear = func(ctx context.Context, p [32]byte) error {
				select {
				case <-peer.stopped:
				default:
					t.Fatal("deleted files before setup worker stopped")
				}
				if err := storage.Clear(ctx, dir, p); err != nil {
					return err
				}
				entries, err := os.ReadDir(filepath.Join(dir, hex.EncodeToString(p[:])))
				if err != nil || len(entries) != 1 || entries[0].Name() != "writer.lock" {
					t.Fatal("saved session files remain after abort", err)
				}
				clears++
				return nil
			}
			c := New(cfg)
			defer c.Close()
			s, err := c.Open(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			await(t, s, func(u game.Update) bool { return u.Snapshot.Choice != nil })
			input := game.Input{Kind: game.StartSession, Terms: game.Terms{Stake: 1000, Bond: 1000, MinBet: 100, MaxWager: 1000}, RelayURL: "ws://relay.invalid"}
			if role == covenant.Player2 {
				inv, secrets, err := game.CreateInvitation(t.Context(), nil, gc, input.Terms, input.RelayURL)
				if err != nil {
					t.Fatal(err)
				}
				defer secrets.Destroy()
				input = game.Input{Kind: game.JoinSession, Invitation: &inv}
			}
			select {
			case s.Inputs <- input:
			case <-time.After(5 * time.Second):
				t.Fatal("setup did not accept input")
			}
			await(t, s, func(u game.Update) bool { return u.Snapshot.Stage == game.StageSessionPrepared })
			select {
			case <-peer.started:
			case <-time.After(5 * time.Second):
				t.Fatal("setup did not start")
			}
			if _, err := c.AbortSetup(t.Context(), [32]byte{3}); !errors.Is(err, wallet.ErrKey) {
				t.Fatal("wrong-wallet abort accepted", err)
			}
			fresh, err := c.AbortSetup(t.Context(), public)
			if err != nil {
				t.Fatal(err)
			}
			u := await(t, fresh, func(u game.Update) bool { return u.Err != nil || u.Snapshot.Choice != nil })
			if u.Err != nil || u.Snapshot.Stage != game.StageInit || clears != 1 || fresh == s || key.PublicKey() != public || fresh.Receive.Address != s.Receive.Address {
				t.Fatal("abort did not return to a fresh game with loaded wallet", u.Err)
			}
			c.Close()
			if key.PublicKey() != [32]byte{} {
				t.Fatal("close retained wallet key")
			}
		})
	}
}

func TestAbortChecksHistoryAfterJoiningWorker(t *testing.T) {
	for _, kind := range []game.EventKind{game.SpendPrepared, game.SpendSigned, game.SubmissionAttempted, game.SubmissionFailed} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			cfg, gc, log, _ := fixture(t)
			seed(t, gc, log, false)
			e := game.Event{Kind: kind}
			switch kind {
			case game.SpendPrepared, game.SpendSigned:
				tx := wire.NewMsgTx(2)
				tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{}, nil, nil))
				tx.AddTxOut(wire.NewTxOut(1000, []byte{0x51}))
				packet, err := psbt.NewFromUnsignedTx(tx)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := packet.B64Encode()
				if err != nil {
					t.Fatal(err)
				}
				e.Spend = &game.SavedSpend{Action: game.PreparedAction{Kind: game.ActionInitialDeposit, Funding: &covenant.Funding{}}, Prepared: ports.Bundle{Ark: encoded, Checkpoints: []string{encoded}}}
				if kind == game.SpendSigned {
					e.Spend.Signed = &e.Spend.Prepared
				}
			case game.SubmissionAttempted:
				e.Receipt = &game.SubmissionReceipt{}
			case game.SubmissionFailed:
				e.FailureCode = "submission_failed"
			}
			raw, err := game.EncodeEvent(e)
			if err != nil {
				t.Fatal(err)
			}
			key := importKey(t, 20)
			public := key.PublicKey()
			ctx, stop := context.WithCancel(t.Context())
			s := &Session{stop: stop, done: make(chan struct{}), key: key}
			c := New(cfg)
			c.session, c.public = s, public
			defer c.Close()
			c.config.Clear = func(context.Context, [32]byte) error { t.Fatal("deleted funded history"); return nil }
			go func() {
				<-ctx.Done()
				// Simulate funding recorded after the last UI setup snapshot, but
				// before the canceled worker completes its final storage operation.
				_ = log.Append(context.Background(), 1, raw)
				close(s.done)
			}()
			if _, err := c.AbortSetup(t.Context(), public); !errors.Is(err, ErrSetupFunded) {
				t.Fatal("late funding was not protected", err)
			}
			if len(log.records) != 2 || !bytes.Equal(log.records[1], raw) || key.PublicKey() != public {
				t.Fatal("rejected abort lost history or wallet")
			}
			if _, err := c.AbortSetup(t.Context(), public); !errors.Is(err, ErrSetupFunded) {
				t.Fatal("retry bypassed funding check", err)
			}
		})
	}
}
