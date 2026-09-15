//go:build !js

package grpc

import (
	"context"
	"errors"

	"arkade-poker/go/internal/adapters/indexdata"
	"arkade-poker/go/internal/adapters/subscription"
	"arkade-poker/go/internal/ports"
	arkv1 "github.com/arkade-os/arkd/api-spec/protobuf/gen/ark/v1"
)

var _ ports.ScriptSubscriber = (*Indexer)(nil)

// Use the generated RPC because client-lib's quiet-time READY is not proof of
// attachment. Empty id + initial filter creates one listener, released by the
// pinned server when this stream is cancelled. Queries stay on client-lib.
func (a *Indexer) Subscribe(ctx context.Context, scripts [][]byte) (ports.ScriptSubscription, error) {
	return a.watches.Subscribe(ctx, scripts, func(ctx context.Context, filter []string) (subscription.Receiver, error) {
		stream, err := arkv1.NewIndexerServiceClient(a.streamConn).GetSubscription(ctx, &arkv1.GetSubscriptionRequest{
			Filter: &arkv1.SubscriptionFilter{Scripts: &arkv1.ScriptFilter{Add: filter}},
		})
		if err != nil {
			return nil, err
		}
		return &nativeSubscription{stream}, nil
	})
}

type nativeSubscription struct {
	arkv1.IndexerService_GetSubscriptionClient
}

func (s *nativeSubscription) Close() error { return s.CloseSend() }
func (s *nativeSubscription) Recv() (indexdata.SubscriptionFrame, error) {
	r, err := s.IndexerService_GetSubscriptionClient.Recv()
	if err != nil {
		return indexdata.SubscriptionFrame{}, err
	}
	if r == nil {
		return indexdata.SubscriptionFrame{}, errors.New("missing subscription frame")
	}
	f := indexdata.SubscriptionFrame{}
	switch data := r.Data.(type) {
	case *arkv1.GetSubscriptionResponse_Heartbeat:
		f.Heartbeat = data != nil && data.Heartbeat != nil
	case *arkv1.GetSubscriptionResponse_SubscriptionStarted:
		if data != nil && data.SubscriptionStarted != nil {
			f.Started = data.SubscriptionStarted.SubscriptionId
		}
	case *arkv1.GetSubscriptionResponse_Event:
		if data == nil || data.Event == nil {
			return f, errors.New("missing subscription transaction")
		}
		convert := func(records []*arkv1.IndexerVtxo) ([]indexdata.EventVtxo, error) {
			if len(records) > indexdata.MaxEventVtxos {
				return nil, errors.New("subscription outpoint limit")
			}
			out := make([]indexdata.EventVtxo, len(records))
			for i, v := range records {
				if v == nil || v.Outpoint == nil {
					return nil, errors.New("missing subscription outpoint")
				}
				out[i] = indexdata.EventVtxo{TxID: v.Outpoint.Txid, Vout: v.Outpoint.Vout, Script: v.Script}
			}
			return out, nil
		}
		created, err := convert(data.Event.NewVtxos)
		if err != nil {
			return f, err
		}
		spent, err := convert(data.Event.SpentVtxos)
		if err != nil {
			return f, err
		}
		f.Event = &indexdata.TransactionEvent{TxID: data.Event.Txid, New: created, Spent: spent}
	}
	return f, nil
}
