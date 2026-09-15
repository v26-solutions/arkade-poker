package wallet

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

type balanceIndexer struct {
	ports.Indexer
	records []ports.Vtxo
	err     error
	query   ports.VtxoQuery
}

func (i *balanceIndexer) Vtxos(_ context.Context, query ports.VtxoQuery) ([]ports.Vtxo, error) {
	i.query = query
	return i.records, i.err
}

func TestBalanceEligibilityAndInvalidEvidence(t *testing.T) {
	script := p2tr(1)
	now := func() time.Time { return time.Unix(1000, 0) }
	coin := ports.Vtxo{Script: script, Amount: 123456, ExpiresAt: 2000}
	spent := chainhash.Hash{1}
	for name, change := range map[string]func(*ports.Vtxo){
		"spent":    func(v *ports.Vtxo) { v.Spent = true },
		"swept":    func(v *ports.Vtxo) { v.Swept = true },
		"unrolled": func(v *ports.Vtxo) { v.Unrolled = true },
		"expired":  func(v *ports.Vtxo) { v.ExpiresAt = 1000 },
		"spending": func(v *ports.Vtxo) { v.ArkTxID = &spent },
		"spent by": func(v *ports.Vtxo) { v.SpentBy = &spent },
		"settled":  func(v *ports.Vtxo) { v.SettledBy = &spent },
		"asset":    func(v *ports.Vtxo) { v.Assets = []ports.Asset{{ID: "asset", Amount: 1}} },
	} {
		t.Run(name, func(t *testing.T) {
			excluded := coin
			excluded.Outpoint.Index = 1
			change(&excluded)
			index := &balanceIndexer{records: []ports.Vtxo{coin, excluded}}
			got, err := Balance(context.Background(), index, script, now)
			if err != nil || got != coin.Amount || !bytes.Equal(index.query.Script, script) {
				t.Fatal(got, err, index.query)
			}
		})
	}
	preconfirmed := coin
	preconfirmed.Preconfirmed, preconfirmed.Outpoint = true, wire.OutPoint{Index: 1}
	index := &balanceIndexer{records: []ports.Vtxo{coin, preconfirmed}}
	if got, err := Balance(context.Background(), index, script, now); err != nil || got != 246912 {
		t.Fatal("preconfirmed balance", got, err)
	}
	for name, records := range map[string][]ports.Vtxo{
		"duplicate":      {coin, coin},
		"foreign script": {{Script: p2tr(2), Amount: 100, ExpiresAt: 2000}},
		"negative":       {{Script: script, Amount: -1, ExpiresAt: 2000}},
		"missing expiry": {{Script: script, Amount: 100}},
		"overflow":       {coin, {Script: script, Amount: btcutil.MaxSatoshi, ExpiresAt: 2000, Outpoint: wire.OutPoint{Index: 1}}},
	} {
		t.Run(name, func(t *testing.T) {
			index := &balanceIndexer{records: records}
			if got, err := Balance(context.Background(), index, script, now); !errors.Is(err, ErrPolicy) || got != 0 {
				t.Fatal("invalid/partial balance", got, err)
			}
		})
	}
	index = &balanceIndexer{err: errors.New("offline")}
	if _, err := Balance(context.Background(), index, script, now); !errors.Is(err, index.err) {
		t.Fatal("query error hidden", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Balance(ctx, index, script, now); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}
