//go:build !js

package grpc

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"sync"

	"arkade-poker/go/internal/adapters/indexdata"
	"arkade-poker/go/internal/adapters/subscription"
	"arkade-poker/go/internal/ports"
	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	"github.com/arkade-os/arkd/pkg/client-lib/indexer"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	rpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type Indexer struct {
	client     clientlib.Indexer
	streamConn *rpc.ClientConn
	watches    subscription.Owner
	closeOnce  sync.Once
	closeErr   error
}

func NewIndexer(endpoint string) (*Indexer, error) {
	u, err := ports.Endpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("gRPC endpoint must not have a path")
	}
	c, err := indexer.NewClient(u.Scheme + "://" + u.Host)
	if err != nil {
		return nil, err
	}
	creds := insecure.NewCredentials()
	port := u.Port()
	if u.Scheme == "https" {
		creds = credentials.NewTLS(nil)
		if port == "" {
			port = "443"
		}
	}
	if port == "" {
		port = "80"
	}
	conn, err := rpc.NewClient(net.JoinHostPort(u.Hostname(), port), rpc.WithTransportCredentials(creds),
		rpc.WithDisableServiceConfig(), rpc.WithDefaultCallOptions(rpc.MaxCallRecvMsgSize(indexdata.MaxBytes)))
	if err != nil {
		c.Close()
		return nil, err
	}
	return &Indexer{client: c, streamConn: conn}, nil
}

func (a *Indexer) Vtxos(ctx context.Context, q ports.VtxoQuery) ([]ports.Vtxo, error) {
	return indexdata.Collect(ctx, q, func(ctx context.Context, index int32) ([]ports.Vtxo, *indexdata.Page, error) {
		opts := []clientlib.GetVtxosOption{clientlib.WithVtxosPage(&clientlib.PageRequest{Size: indexdata.PageSize, Index: index})}
		if len(q.Script) > 0 {
			opts = append(opts, clientlib.WithScripts([]string{hex.EncodeToString(q.Script)}))
		} else {
			points := make([]clientlib.Outpoint, len(q.Outpoints))
			for i, p := range q.Outpoints {
				points[i] = clientlib.Outpoint{Txid: p.Hash.String(), VOut: p.Index}
			}
			opts = append(opts, clientlib.WithOutpoints(points))
		}
		r, err := a.client.GetVtxos(ctx, opts...)
		if err != nil {
			return nil, nil, err
		}
		if r == nil {
			return nil, nil, errors.New("missing indexer VTXO response")
		}
		if len(r.Vtxos) > indexdata.PageSize {
			return nil, nil, errors.New("indexer page size exceeded")
		}
		var records []ports.Vtxo
		for _, v := range r.Vtxos {
			record, err := nativeVtxo(v)
			if err != nil {
				return nil, nil, err
			}
			records = append(records, record)
		}
		return records, nativePage(r.Page), nil
	})
}

func nativePage(p *clientlib.PageResponse) *indexdata.Page {
	if p == nil {
		return nil
	}
	return &indexdata.Page{Current: p.Current, Next: p.Next, Total: p.Total}
}

func nativeVtxo(v clientlib.Vtxo) (ports.Vtxo, error) {
	var assets []ports.Asset
	for _, a := range v.Assets {
		assets = append(assets, ports.Asset{ID: a.AssetId, Amount: a.Amount})
	}
	return (indexdata.Record{TxID: v.Txid, Vout: v.VOut, Script: v.Script, Amount: v.Amount,
		CreatedAt: v.CreatedAt.Unix(), ExpiresAt: v.ExpiresAt.Unix(), Preconfirmed: v.Preconfirmed,
		Spent: v.Spent, Swept: v.Swept, Unrolled: v.Unrolled, SpentBy: v.SpentBy, SettledBy: v.SettledBy,
		ArkTxID: v.ArkTxid, CommitmentTxIDs: v.CommitmentTxids, Assets: assets}).Decode()
}

func (a *Indexer) Transaction(ctx context.Context, id chainhash.Hash) (*wire.MsgTx, error) {
	r, err := a.client.GetVirtualTxs(ctx, []string{id.String()}, clientlib.WithPage(&clientlib.PageRequest{Size: indexdata.PageSize, Index: 1}))
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("missing indexer transaction response")
	}
	return indexdata.Transaction(id, r.Txs, nativePage(r.Page))
}

func (a *Indexer) Close() error {
	a.closeOnce.Do(func() { _ = a.watches.Close(); a.closeErr = a.streamConn.Close(); a.client.Close() })
	return a.closeErr
}

var _ ports.Indexer = (*Indexer)(nil)
