package http

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"

	"arkade-poker/go/internal/adapters/indexdata"
	"arkade-poker/go/internal/adapters/subscription"
	"arkade-poker/go/internal/ports"
)

var _ ports.ScriptSubscriber = (*Indexer)(nil)

func (a *Indexer) Subscribe(ctx context.Context, scripts [][]byte) (ports.ScriptSubscription, error) {
	return a.watches.Subscribe(ctx, scripts, func(ctx context.Context, filter []string) (subscription.Receiver, error) {
		query := url.Values{"filter.scripts.add": filter}
		req, err := http.NewRequestWithContext(ctx, "GET", a.base+"/v1/indexer/subscription?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		// Retain transport/redirect policy but remove the unary request's whole
		// lifetime timeout. The owner bounds attachment and cancels Fetch/Recv.
		client := *a.client
		client.Timeout = 0
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, HTTPStatusError(resp.StatusCode)
		}
		media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if err != nil || media != "text/event-stream" {
			resp.Body.Close()
			return nil, errors.New("subscription requires an SSE response")
		}
		return newSSE(bindBody(ctx, resp.Body), indexdata.MaxBytes), nil
	})
}

func decodeSubscription(data []byte) (indexdata.SubscriptionFrame, error) {
	var fields map[string]json.RawMessage
	f := indexdata.SubscriptionFrame{}
	if err := decodeProtoFields(data, &fields); err != nil {
		return f, err
	}
	kinds := 0
	for _, name := range []string{"subscriptionstarted", "heartbeat", "event"} {
		if _, ok := fields[name]; ok {
			kinds++
		}
	}
	if kinds != 1 {
		return f, errors.New("invalid subscription frame kind")
	}
	if _, ok := fields["error"]; ok {
		return f, errors.New("subscription service error")
	}
	if raw, ok := fields["subscriptionstarted"]; ok {
		var started struct {
			ID string `json:"subscriptionId"`
		}
		if err := decodeProtoFields(raw, &started); err != nil {
			return f, err
		}
		f.Started = started.ID
	}
	if raw, ok := fields["heartbeat"]; ok {
		var heartbeat struct{}
		if err := decodeObject(raw, &heartbeat); err != nil {
			return f, err
		}
		f.Heartbeat = true
	}
	if raw, ok := fields["event"]; ok {
		var event struct {
			TxID  string        `json:"txid"`
			New   []indexerVtxo `json:"newVtxos"`
			Spent []indexerVtxo `json:"spentVtxos"`
		}
		if err := decodeProtoFields(raw, &event); err != nil {
			return f, err
		}
		convert := func(records []indexerVtxo) ([]indexdata.EventVtxo, error) {
			if len(records) > indexdata.MaxEventVtxos {
				return nil, errors.New("subscription outpoint limit")
			}
			out := make([]indexdata.EventVtxo, len(records))
			for i, v := range records {
				if v.Outpoint == nil {
					return nil, errors.New("missing subscription outpoint")
				}
				out[i] = indexdata.EventVtxo{TxID: v.Outpoint.TxID, Vout: v.Outpoint.Vout, Script: v.Script}
			}
			return out, nil
		}
		created, err := convert(event.New)
		if err != nil {
			return f, err
		}
		spent, err := convert(event.Spent)
		if err != nil {
			return f, err
		}
		f.Event = &indexdata.TransactionEvent{TxID: event.TxID, New: created, Spent: spent}
	}
	return f, nil
}
