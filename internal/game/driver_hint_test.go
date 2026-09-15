package game

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/wire"
)

// Simulate query views catching up at different times while retaining the real
// transaction/output evidence used by wallet.Accepted.
type laggedIndexer struct {
	ports.Indexer
	hideScript     bool
	source         wire.OutPoint
	hideSourceOnce bool
}

func (l *laggedIndexer) Vtxos(ctx context.Context, q ports.VtxoQuery) ([]ports.Vtxo, error) {
	if l.hideScript && len(q.Script) != 0 {
		return nil, nil
	}
	if l.hideSourceOnce && len(q.Outpoints) == 1 && q.Outpoints[0] == l.source {
		l.hideSourceOnce = false
		return nil, nil
	}
	return l.Indexer.Vtxos(ctx, q)
}
func injectHint(t *testing.T, d *Driver, hint ports.ScriptEvent) {
	t.Helper()
	if err := d.ensureWatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.watch.sub.(*fixtureSubscription).events <- hint
	select {
	case <-d.watch.changed:
	case <-time.After(time.Second):
		t.Fatal("hint not received")
	}
}
func TestDriverDepositHintBeforeExpiry(t *testing.T) {
	for _, mode := range []string{"accepted", "unaccepted", "missing_output_hint", "query_error"} {
		t.Run(mode, func(t *testing.T) {
			f := newDriverFixture(t)
			prefix := f.h.logs[0]
			f.h.action(1, Input{Kind: Progress})
			accepted := f.h.games[0].hand.accepted.Transaction
			id := accepted.TxHash()
			f.importHistory(t)
			d, w, _, _ := f.newDriver(t, 0, prefix)
			d.config.Indexer = &laggedIndexer{Indexer: f, hideScript: true}
			d.config.Now = func() time.Time { return time.Unix(5000, 0) }
			if err := d.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			hint := ports.ScriptEvent{Kind: ports.ScriptChanged, TxID: id, NewVtxos: []wire.OutPoint{{Hash: id}}}
			if mode == "missing_output_hint" {
				hint.NewVtxos = nil
			}
			if mode == "unaccepted" {
				f.mu.Lock()
				v := f.records[wire.OutPoint{Hash: id}]
				v.Preconfirmed = false
				f.records[v.Outpoint] = v
				f.mu.Unlock()
			}
			fault := errors.New("hint accepted query failed")
			if mode == "query_error" {
				f.queryErr = fault
			}
			injectHint(t, d, hint)
			step, err := d.step(context.Background(), Input{Kind: Progress})
			if mode == "query_error" {
				if !errors.Is(err, fault) {
					t.Fatal("query failure was expiry", err)
				}
				return
			}
			want := SetupAborted
			if mode == "accepted" {
				want = DepositObserved
			}
			if err != nil || step.Event == nil || step.Event.Kind != want || w.selectCalls != 0 {
				t.Fatal("deposit hint admission", step.Kind, err)
			}
			if err := d.commit(context.Background(), *step.Event); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestDriverSourceHintsRequireAcceptedEdges(t *testing.T) {
	for _, mode := range []string{"continuation", "missing_continuation", "terminal", "invalid_kind", "wrong_script"} {
		t.Run(mode, func(t *testing.T) {
			f := newDriverFixture(t)
			f.h.start(0)
			f.importHistory(t)
			d, _, _, _ := f.newDriver(t, 1, f.h.logs[1])
			if err := d.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			source := wire.OutPoint{Hash: d.journal.game.hand.accepted.Transaction.TxHash()}
			input := Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}}
			if mode == "terminal" {
				input = Input{Kind: Concede}
			}
			f.h.action(0, input)
			f.importHistory(t)
			last, err := DecodeEvent(f.h.logs[0][len(f.h.logs[0])-1])
			if err != nil {
				t.Fatal(err)
			}
			id := last.Accepted.Transaction.TxHash()
			d.config.Indexer = &laggedIndexer{Indexer: f, source: source, hideSourceOnce: true}
			hint := ports.ScriptEvent{Kind: ports.ScriptChanged, TxID: id, SpentVtxos: []wire.OutPoint{source}}
			if mode != "missing_continuation" && mode != "terminal" {
				hint.NewVtxos = []wire.OutPoint{{Hash: id}}
			}
			if mode == "invalid_kind" {
				hint.Kind = 255
			}
			if mode == "wrong_script" {
				hint.Script = []byte{0x51}
			}
			injectHint(t, d, hint)
			step, err := d.step(context.Background(), Input{Kind: Progress})
			if mode == "missing_continuation" || mode == "invalid_kind" || mode == "wrong_script" {
				if !errors.Is(err, ErrProtocol) {
					t.Fatal("invalid hint passed", err)
				}
				return
			}
			if err != nil || step.Event == nil || step.Event.Kind != SpendObserved {
				t.Fatal("accepted source hint", err)
			}
			if err := d.commit(context.Background(), *step.Event); err != nil {
				t.Fatal(err)
			}
			if mode == "terminal" && d.journal.game.stage != StageFinished {
				t.Fatal("terminal hint lost")
			}
		})
	}
}
