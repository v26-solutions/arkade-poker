package client

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/wallet"
)

func awaitBalance(t *testing.T, updates <-chan BalanceUpdate) BalanceUpdate {
	t.Helper()
	select {
	case u, ok := <-updates:
		if !ok {
			t.Fatal("balance updates closed")
		}
		return u
	case <-time.After(5 * time.Second):
		t.Fatal("balance timeout")
		return BalanceUpdate{}
	}
}

type balanceFixture struct {
	ports.Indexer
	script   []byte
	events   chan ports.ScriptEvent
	amount   atomic.Int64
	queries  atomic.Int32
	opens    atomic.Int32
	closes   atomic.Int32
	fail     atomic.Bool
	badOrder atomic.Bool
}

func (f *balanceFixture) Vtxos(ctx context.Context, query ports.VtxoQuery) ([]ports.Vtxo, error) {
	if f.opens.Load() == 0 || !bytes.Equal(query.Script, f.script) {
		f.badOrder.Store(true)
	}
	f.queries.Add(1)
	if f.fail.Load() {
		return nil, errors.New("index unavailable")
	}
	return []ports.Vtxo{{Script: f.script, Amount: f.amount.Load(), ExpiresAt: 2000}}, ctx.Err()
}
func (f *balanceFixture) Subscribe(ctx context.Context, scripts [][]byte) (ports.ScriptSubscription, error) {
	if len(scripts) != 1 || !bytes.Equal(scripts[0], f.script) {
		f.badOrder.Store(true)
	}
	f.opens.Add(1)
	return &balanceStream{fixture: f}, ctx.Err()
}

type balanceStream struct{ fixture *balanceFixture }

func (s *balanceStream) Next(ctx context.Context) (ports.ScriptEvent, error) {
	select {
	case event := <-s.fixture.events:
		return event, nil
	case <-ctx.Done():
		return ports.ScriptEvent{}, ctx.Err()
	}
}
func (s *balanceStream) Close() error { s.fixture.closes.Add(1); return nil }

func TestBalanceWatchRefreshReconnectAndClose(t *testing.T) {
	f := &balanceFixture{script: append([]byte{0x51, 0x20}, make([]byte, 32)...), events: make(chan ports.ScriptEvent, 4)}
	f.amount.Store(1000)
	updates := make(chan BalanceUpdate, 1)
	stop := startBalance(context.Background(), wallet.Services{Indexer: f, Now: func() time.Time { return time.Unix(1000, 0) }}, f, f.script, updates)
	defer stop()
	if u := awaitBalance(t, updates); u.Err != nil || u.Sats != 1000 {
		t.Fatal("initial balance", u)
	}
	f.amount.Store(123456)
	f.events <- ports.ScriptEvent{Kind: ports.ScriptChanged, Script: f.script}
	if u := awaitBalance(t, updates); u.Err != nil || u.Sats != 123456 {
		t.Fatal("change did not refetch", u)
	}
	f.events <- ports.ScriptEvent{Kind: ports.ScriptObservationGap}
	if u := awaitBalance(t, updates); u.Err == nil {
		t.Fatal("disconnected balance remained current", u)
	}
	f.amount.Store(987654)
	if u := awaitBalance(t, updates); u.Err != nil || u.Sats != 987654 {
		t.Fatal("reattachment did not refetch", u)
	}
	f.fail.Store(true)
	f.events <- ports.ScriptEvent{Kind: ports.ScriptChanged}
	if u := awaitBalance(t, updates); u.Err == nil {
		t.Fatal("query failure hidden", u)
	}
	f.fail.Store(false)
	f.amount.Store(0)
	if u := awaitBalance(t, updates); u.Err != nil || u.Sats != 0 {
		t.Fatal("query did not recover", u)
	}
	// Leave the next update unread. Neither watching nor shutdown may depend
	// on the UI draining the balance channel.
	f.events <- ports.ScriptEvent{Kind: ports.ScriptChanged}
	stop()
	if f.opens.Load() != 3 || f.closes.Load() != f.opens.Load() || f.badOrder.Load() {
		t.Fatal("watch ownership/filter/attachment", f.opens.Load(), f.closes.Load(), f.badOrder.Load())
	}
}
