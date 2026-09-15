//go:build !js

package grpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	web "arkade-poker/go/internal/adapters/http"
	"arkade-poker/go/internal/ports"
	arkv1 "github.com/arkade-os/arkd/api-spec/protobuf/gen/ark/v1"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/meshapi/grpc-api-gateway/gateway"
	rpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Use the real upstream gRPC client, generated gateway and ProtoJSON marshaler
// against one fixture service. This exercises transport translation, pagination
// and withheld-signature PSBT decoding, independently of service acceptance.
type fixtureIndexerService struct {
	arkv1.UnimplementedIndexerServiceServer
	t          *testing.T
	vtxos      []*arkv1.IndexerVtxo
	txID, psbt string
	failPage   atomic.Int32
}

func (s *fixtureIndexerService) GetVtxos(_ context.Context, q *arkv1.GetVtxosRequest) (*arkv1.GetVtxosResponse, error) {
	if q.Page == nil || q.Page.Size != 100 || q.Page.Index < 1 ||
		q.SpendableOnly || q.SpentOnly || q.RecoverableOnly || q.PendingOnly || q.RenewableOnly {
		s.t.Error("adapter lost explicit pagination or filtered recovery evidence")
		return nil, status.Error(codes.InvalidArgument, "query")
	}
	if q.Page.Index == s.failPage.Load() {
		return nil, status.Error(codes.Unavailable, "test failure")
	}
	var matches []*arkv1.IndexerVtxo
	for _, v := range s.vtxos {
		point := wire.OutPoint{Hash: mustHash(s.t, v.Outpoint.Txid), Index: v.Outpoint.Vout}
		if slices.Contains(q.Scripts, v.Script) || slices.Contains(q.Outpoints, point.String()) {
			matches = append(matches, v)
		}
	}
	total := int32((len(matches) + 99) / 100)
	start := min(len(matches), int(q.Page.Index-1)*100)
	return &arkv1.GetVtxosResponse{Vtxos: matches[start:min(start+100, len(matches))],
		Page: &arkv1.IndexerPageResponse{Current: q.Page.Index, Next: min(q.Page.Index+1, total), Total: total}}, nil
}

func mustHash(t *testing.T, text string) chainhash.Hash {
	t.Helper()
	h, err := chainhash.NewHashFromStr(text)
	if err != nil {
		t.Fatal(err)
	}
	return *h
}

func (s *fixtureIndexerService) GetVirtualTxs(_ context.Context, q *arkv1.GetVirtualTxsRequest) (*arkv1.GetVirtualTxsResponse, error) {
	if q.Page == nil || q.Page.Index != 1 || len(q.Txids) != 1 {
		return nil, status.Error(codes.InvalidArgument, "query")
	}
	if q.Txids[0] != s.txID {
		return nil, status.Error(codes.NotFound, "missing")
	}
	return &arkv1.GetVirtualTxsResponse{Txs: []string{s.psbt}, Page: &arkv1.IndexerPageResponse{Current: 1, Next: 1, Total: 1}}, nil
}

func TestIndexerTransportParity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	script := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{4}, 32)...)
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}}, nil, nil))
	for range 201 {
		tx.AddTxOut(wire.NewTxOut(1000, script))
	}
	packet, err := psbt.NewFromUnsignedTx(tx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := packet.B64Encode()
	if err != nil {
		t.Fatal(err)
	}
	service := &fixtureIndexerService{t: t, txID: tx.TxHash().String(), psbt: encoded}
	for i := range 201 {
		service.vtxos = append(service.vtxos, &arkv1.IndexerVtxo{
			Outpoint: &arkv1.IndexerOutpoint{Txid: service.txID, Vout: uint32(i)}, Script: hex.EncodeToString(script),
			Amount: 1000, CreatedAt: 123, ExpiresAt: 2_000_000_000, IsPreconfirmed: i%2 == 0,
			IsSpent: i%3 == 0, IsSwept: i%5 == 0, IsUnrolled: i%7 == 0,
			SpentBy: chainhash.Hash{2}.String(), ArkTxid: chainhash.Hash{3}.String(), SettledBy: chainhash.Hash{4}.String(),
			CommitmentTxids: []string{chainhash.Hash{5}.String()}, Assets: []*arkv1.IndexerAsset{{AssetId: "test-asset", Amount: ^uint64(0)}},
		})
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := rpc.NewServer()
	arkv1.RegisterIndexerServiceServer(server, service)
	go server.Serve(listener)
	defer server.Stop()
	conn, err := rpc.NewClient(listener.Addr().String(), rpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mux := gateway.NewServeMux()
	arkv1.RegisterIndexerServiceHandler(ctx, mux, conn)
	HTTP := httptest.NewServer(mux)
	defer HTTP.Close()
	native, err := NewIndexer("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	browser, err := web.NewIndexer(HTTP.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	q := ports.VtxoQuery{Script: script}
	n, err := native.Vtxos(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	w, err := browser.Vtxos(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 201 || !reflect.DeepEqual(n, w) {
		t.Fatal("transport records differ or pagination truncated", len(n), len(w))
	}
	if n[0].Amount != 1000 || n[0].Assets[0].Amount != ^uint64(0) || n[0].ExpiresAt != 2_000_000_000 || n[0].ArkTxID == nil {
		t.Fatal("lost record values", n[0])
	}
	for name, indexer := range map[string]ports.Indexer{"native": native, "http": browser} {
		t.Run(name, func(t *testing.T) {
			points := []wire.OutPoint{n[0].Outpoint, n[200].Outpoint}
			v, err := indexer.Vtxos(ctx, ports.VtxoQuery{Outpoints: points})
			if err != nil || len(v) != 2 || v[0].Outpoint != points[0] || v[1].Outpoint != points[1] {
				t.Fatal("outpoint batch", v, err)
			}
			body, err := indexer.Transaction(ctx, tx.TxHash())
			if err != nil || body == nil || body.TxHash() != tx.TxHash() {
				t.Fatal("transaction body", body, err)
			}
			absent, err := indexer.Transaction(ctx, chainhash.Hash{99})
			if err != nil || absent != nil {
				t.Fatal("absent transaction", absent, err)
			}
			service.failPage.Store(2)
			v, err = indexer.Vtxos(ctx, q)
			service.failPage.Store(0)
			if err == nil || v != nil {
				t.Fatal("partial evidence escaped after query failure", v, err)
			}
		})
	}
}
