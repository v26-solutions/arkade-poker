// Package indexdata shares bounded indexer decoding across native and browser
// adapters. It has no RPC or HTTP dependencies and does not admit wallet spends.
package indexdata

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

const (
	PageSize = 100
	MaxPages = 100
	MaxBytes = 20 << 20
)

type Page struct{ Current, Next, Total int32 }

// The pinned service reports Next == Total on the last page (zero for an
// empty result). Reject moving totals, skipped pages and ambiguous truncation.
func (p *Page) Check(index, previousTotal, count int) error {
	if p == nil || p.Current != int32(index) || p.Total < 0 || p.Total > MaxPages ||
		(previousTotal >= 0 && p.Total != int32(previousTotal)) ||
		p.Next != min(int32(index+1), p.Total) || count > PageSize ||
		(p.Total == 0 && count != 0) || (p.Total > 0 && (count == 0 || int32(index) > p.Total)) {
		return errors.New("invalid or changing indexer pagination")
	}
	return nil
}

func CheckQuery(q ports.VtxoQuery) error {
	if (len(q.Script) == 0) == (len(q.Outpoints) == 0) || len(q.Outpoints) > 256 {
		return errors.New("indexer query requires one script or 1–256 outpoints")
	}
	if len(q.Script) > 0 && !txscript.IsPayToTaproot(q.Script) {
		return errors.New("indexer query requires a P2TR script")
	}
	seen := make(map[wire.OutPoint]bool, len(q.Outpoints))
	for _, point := range q.Outpoints {
		if seen[point] {
			return errors.New("duplicate query outpoint")
		}
		seen[point] = true
	}
	return nil
}

func Collect(ctx context.Context, q ports.VtxoQuery, fetch func(context.Context, int32) ([]ports.Vtxo, *Page, error)) ([]ports.Vtxo, error) {
	if err := CheckQuery(q); err != nil {
		return nil, err
	}
	var records []ports.Vtxo
	seen := make(map[wire.OutPoint]bool)
	previousTotal := -1
	for index := 1; index <= MaxPages; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, page, err := fetch(ctx, int32(index))
		if err != nil {
			return nil, err
		}
		if err := page.Check(index, previousTotal, len(items)); err != nil {
			return nil, err
		}
		for _, record := range items {
			if seen[record.Outpoint] ||
				(len(q.Script) > 0 && !bytes.Equal(q.Script, record.Script)) ||
				(len(q.Outpoints) > 0 && !slices.Contains(q.Outpoints, record.Outpoint)) {
				return nil, errors.New("duplicate or unrelated indexed output")
			}
			seen[record.Outpoint] = true
			records = append(records, record)
		}
		if index >= int(page.Total) {
			// Stable application ordering, independent of transport/DB ordering.
			slices.SortFunc(records, func(a, b ports.Vtxo) int {
				if n := bytes.Compare(a.Outpoint.Hash[:], b.Outpoint.Hash[:]); n != 0 {
					return n
				}
				if a.Outpoint.Index < b.Outpoint.Index {
					return -1
				}
				if a.Outpoint.Index > b.Outpoint.Index {
					return 1
				}
				return 0
			})
			return records, nil
		}
		previousTotal = int(page.Total)
	}
	return nil, errors.New("indexer pagination limit")
}

// Hash is deliberately stricter than chainhash.NewHashFromStr, which accepts
// short and odd-length strings. Service transaction identities must be 32 bytes.
func Hash(s string) (chainhash.Hash, error) {
	if len(s) != 64 {
		return chainhash.Hash{}, errors.New("invalid indexer transaction identity")
	}
	h, err := chainhash.NewHashFromStr(s)
	if err != nil {
		return chainhash.Hash{}, errors.New("invalid indexer transaction identity")
	}
	return *h, nil
}

func OptionalHash(s string) (*chainhash.Hash, error) {
	if s == "" {
		return nil, nil
	}
	h, err := Hash(s)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// Record is the common input to native-result and gateway-DTO conversion.
type Record struct {
	TxID, Script                         string
	Vout                                 uint32
	Amount                               uint64
	CreatedAt, ExpiresAt                 int64
	Preconfirmed, Spent, Swept, Unrolled bool
	SpentBy, SettledBy, ArkTxID          string
	CommitmentTxIDs                      []string
	Assets                               []ports.Asset
}

func (r Record) Decode() (ports.Vtxo, error) {
	var v ports.Vtxo
	var err error
	if r.Amount > btcutil.MaxSatoshi || r.CreatedAt < 0 || r.ExpiresAt < 0 ||
		len(r.Script) == 0 || len(r.Script) > 2*txscript.MaxScriptSize || len(r.CommitmentTxIDs) > 256 || len(r.Assets) > 256 {
		return v, errors.New("invalid indexed output values")
	}
	v.Outpoint.Hash, err = Hash(r.TxID)
	if err != nil {
		return v, err
	}
	v.Outpoint.Index = r.Vout
	v.Script, err = hex.DecodeString(r.Script)
	if err != nil {
		return v, errors.New("invalid indexed output script")
	}
	for _, target := range []struct {
		src string
		dst **chainhash.Hash
	}{
		{r.SpentBy, &v.SpentBy}, {r.SettledBy, &v.SettledBy}, {r.ArkTxID, &v.ArkTxID},
	} {
		*target.dst, err = OptionalHash(target.src)
		if err != nil {
			return v, err
		}
	}
	for _, id := range r.CommitmentTxIDs {
		h, err := Hash(id)
		if err != nil {
			return v, err
		}
		if slices.Contains(v.CommitmentTxIDs, h) {
			return v, errors.New("duplicate commitment identity")
		}
		v.CommitmentTxIDs = append(v.CommitmentTxIDs, h)
	}
	for _, asset := range r.Assets {
		if asset.ID == "" || len(asset.ID) > 256 {
			return v, errors.New("invalid indexed asset identity")
		}
	}
	v.Amount, v.CreatedAt, v.ExpiresAt = int64(r.Amount), r.CreatedAt, r.ExpiresAt
	v.Preconfirmed, v.Spent, v.Swept, v.Unrolled = r.Preconfirmed, r.Spent, r.Swept, r.Unrolled
	v.Assets = slices.Clone(r.Assets)
	return v, nil
}

// Transaction accepts the stored PSBTs returned by the pinned indexer, and raw
// consensus hex. It checks complete decoding and txid but never requires or
// verifies signatures: the indexer may deliberately withhold those fields.
func Transaction(id chainhash.Hash, encoded []string, page *Page) (*wire.MsgTx, error) {
	if err := page.Check(1, -1, len(encoded)); err != nil {
		return nil, err
	}
	if len(encoded) > 1 || page.Total > 1 {
		return nil, errors.New("indexer transaction count")
	}
	if len(encoded) == 0 {
		return nil, nil
	}
	s := encoded[0]
	if len(s) > MaxBytes {
		return nil, errors.New("indexer transaction exceeds byte limit")
	}
	var tx *wire.MsgTx
	if strings.HasPrefix(s, "cHNidP") {
		data, err := base64.StdEncoding.Strict().DecodeString(s)
		if err != nil {
			return nil, errors.New("invalid indexed PSBT encoding")
		}
		r := bytes.NewReader(data)
		packet, err := psbt.NewFromRawBytes(r, false)
		if err != nil || r.Len() != 0 {
			return nil, errors.New("invalid indexed PSBT")
		}
		tx = packet.UnsignedTx
	} else {
		data, err := hex.DecodeString(s)
		if err != nil {
			return nil, errors.New("invalid indexed transaction hex")
		}
		r := bytes.NewReader(data)
		tx = new(wire.MsgTx)
		if err := tx.Deserialize(r); err != nil || r.Len() != 0 {
			return nil, errors.New("invalid indexed transaction")
		}
	}
	if tx == nil || tx.TxHash() != id {
		return nil, errors.New("indexed transaction identity mismatch")
	}
	return tx, nil
}
