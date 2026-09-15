package wallet

import (
	"bytes"
	"context"
	"time"

	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// Balance reads the indexed satoshi balance of the default receive script.
// It uses the funding policy's eligibility filters, including preconfirmed
// outputs and excluding expired, consumed and asset-bearing outputs. This is a
// display value; SelectFunding still authenticates transactions before spending.
func Balance(ctx context.Context, indexer ports.Indexer, script []byte, now func() time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if indexer == nil || !txscript.IsPayToTaproot(script) {
		return 0, ErrPolicy
	}
	if now == nil {
		now = time.Now
	}
	records, err := indexer.Vtxos(ctx, ports.VtxoQuery{Script: bytes.Clone(script)})
	if err != nil {
		return 0, err
	}
	at := now().Unix()
	if at < 0 || len(records) > 10000 {
		return 0, ErrPolicy
	}
	seen := make(map[wire.OutPoint]bool, len(records))
	var balance int64
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if !bytes.Equal(record.Script, script) || seen[record.Outpoint] || record.ExpiresAt <= 0 {
			return 0, ErrPolicy
		}
		seen[record.Outpoint] = true
		if record.ExpiresAt <= at || record.Spent || record.Swept || record.Unrolled || record.SpentBy != nil || record.ArkTxID != nil || record.SettledBy != nil {
			continue
		}
		if record.Amount < 0 || record.Amount > btcutil.MaxSatoshi {
			return 0, ErrPolicy
		}
		if len(record.Assets) > 0 {
			continue
		}
		if record.Amount > btcutil.MaxSatoshi-balance {
			return 0, ErrPolicy
		}
		balance += record.Amount
	}
	return balance, nil
}
