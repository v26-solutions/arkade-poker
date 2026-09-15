package indexdata

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

func TestCompletePaginationOrError(t *testing.T) {
	script := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{3}, 32)...)
	q := ports.VtxoQuery{Script: script}
	for _, fault := range []string{"", "repeated", "unrelated", "moving", "skipped", "missing", "empty", "too_many", "failure"} {
		t.Run(fault, func(t *testing.T) {
			calls := 0
			records, err := Collect(context.Background(), q, func(_ context.Context, index int32) ([]ports.Vtxo, *Page, error) {
				calls++
				h := chainhash.Hash{byte(index)}
				items := []ports.Vtxo{{Outpoint: wire.OutPoint{Hash: h}, Script: script}}
				p := &Page{Current: index, Next: 2, Total: 2}
				if index == 2 {
					switch fault {
					case "repeated":
						items[0].Outpoint.Hash = chainhash.Hash{1}
					case "unrelated":
						items[0].Script = []byte{0x51}
					case "moving":
						p.Total, p.Next = 3, 3
					case "skipped":
						p.Current = 3
					case "missing":
						p = nil
					case "empty":
						items = nil
					case "too_many":
						p.Total, p.Next = MaxPages+1, 3
					case "failure":
						return nil, nil, errors.New("connection lost")
					}
				}
				return items, p, nil
			})
			if calls != 2 {
				t.Fatal("pagination did not reach second page", calls)
			}
			if fault == "" {
				if err != nil || len(records) != 2 {
					t.Fatal(records, err)
				}
			} else if err == nil || records != nil {
				t.Fatal("returned partial/invalid evidence", records, err)
			}
		})
	}
	if records, err := Collect(context.Background(), q, func(context.Context, int32) ([]ports.Vtxo, *Page, error) {
		return nil, &Page{Current: 1}, nil
	}); err != nil || len(records) != 0 {
		t.Fatal("empty query", records, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Collect(ctx, q, func(context.Context, int32) ([]ports.Vtxo, *Page, error) {
		t.Fatal("query after cancellation")
		return nil, nil, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestQueryScope(t *testing.T) {
	p := wire.OutPoint{Hash: chainhash.Hash{1}}
	for _, q := range []ports.VtxoQuery{{}, {Script: []byte{0x51}}, {Script: []byte{0x51}, Outpoints: []wire.OutPoint{p}}, {Outpoints: []wire.OutPoint{p, p}}} {
		if err := CheckQuery(q); err == nil {
			t.Fatal("invalid query admitted", q)
		}
	}
	_, err := Collect(context.Background(), ports.VtxoQuery{Outpoints: []wire.OutPoint{p}}, func(context.Context, int32) ([]ports.Vtxo, *Page, error) {
		return []ports.Vtxo{{Outpoint: wire.OutPoint{Hash: chainhash.Hash{2}}}}, &Page{Current: 1, Next: 1, Total: 1}, nil
	})
	if err == nil {
		t.Fatal("unrelated outpoint admitted")
	}
}

func TestRecordDecoding(t *testing.T) {
	valid := Record{TxID: strings.Repeat("01", 32), Script: "5120" + strings.Repeat("03", 32), Amount: 1000,
		CreatedAt: 1, ExpiresAt: 2000, SpentBy: strings.Repeat("02", 32)}
	v, err := valid.Decode()
	if err != nil || v.Amount != 1000 || v.SpentBy == nil || v.ArkTxID != nil {
		t.Fatal(v, err)
	}
	for _, mutate := range []func(*Record){
		func(r *Record) { r.TxID = "1" },
		func(r *Record) { r.SpentBy = "1" },
		func(r *Record) { r.Script = "xx" },
		func(r *Record) { r.Amount = btcutil.MaxSatoshi + 1 },
		func(r *Record) { r.ExpiresAt = -1 },
		func(r *Record) { r.CommitmentTxIDs = []string{r.TxID, r.TxID} },
		func(r *Record) { r.Assets = []ports.Asset{{}} },
	} {
		r := valid
		mutate(&r)
		if _, err := r.Decode(); err == nil {
			t.Fatal("malformed VTXO accepted", r)
		}
	}
}

func TestIndexedTransactionIdentityAndFullDecoding(t *testing.T) {
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(1000, append([]byte{0x51, 0x20}, bytes.Repeat([]byte{3}, 32)...)))
	var raw bytes.Buffer
	if err := tx.Serialize(&raw); err != nil {
		t.Fatal(err)
	}
	packet, err := psbt.NewFromUnsignedTx(tx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := packet.B64Encode()
	if err != nil {
		t.Fatal(err)
	}
	page := &Page{Current: 1, Next: 1, Total: 1}
	for _, text := range []string{hex.EncodeToString(raw.Bytes()), encoded} {
		decoded, err := Transaction(tx.TxHash(), []string{text}, page)
		if err != nil || decoded.TxHash() != tx.TxHash() {
			t.Fatal("valid unsigned indexed transaction", err)
		}
		if _, err := Transaction(chainhash.Hash{9}, []string{text}, page); err == nil {
			t.Fatal("wrong txid admitted")
		}
	}
	psbtBytes, _ := base64.StdEncoding.DecodeString(encoded)
	for _, text := range []string{"xx", encoded[:len(encoded)-4], hex.EncodeToString(raw.Bytes()) + "00", base64.StdEncoding.EncodeToString(append(psbtBytes, 0))} {
		if _, err := Transaction(tx.TxHash(), []string{text}, page); err == nil {
			t.Fatal("malformed/trailing transaction admitted")
		}
	}
	if _, err := Transaction(tx.TxHash(), []string{encoded, encoded}, page); err == nil {
		t.Fatal("duplicate transaction identity admitted")
	}
	if _, err := Transaction(tx.TxHash(), []string{encoded}, nil); err == nil {
		t.Fatal("missing page accepted")
	}
}
