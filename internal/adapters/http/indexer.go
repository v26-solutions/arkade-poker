package http

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"arkade-poker/go/internal/adapters/indexdata"
	"arkade-poker/go/internal/adapters/subscription"
	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

type Indexer struct {
	*Client
	watches subscription.Owner
}

func NewIndexer(endpoint string) (*Indexer, error) {
	c, err := New(endpoint)
	if err != nil {
		return nil, err
	}
	return &Indexer{Client: c}, nil
}

func (a *Indexer) Close() error { _ = a.watches.Close(); return a.Client.Close() }

// ProtoJSON's uint64 is distinct from signed amounts/time fields. Preserve
// unsigned asset values without float conversion or narrowing them to int64.
type unsigned uint64

func (n *unsigned) UnmarshalJSON(data []byte) error {
	s := string(data)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" {
		return errors.New("missing unsigned decimal")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return errors.New("invalid unsigned decimal")
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return errors.New("unsigned decimal out of range")
	}
	*n = unsigned(v)
	return nil
}

type indexerVtxo struct {
	Outpoint *struct {
		TxID string `json:"txid"`
		Vout uint32 `json:"vout"`
	} `json:"outpoint"`
	Script        string         `json:"script"`
	Amount        unsigned       `json:"amount"`
	CreatedAt     decimal        `json:"createdAt"`
	ExpiresAt     decimal        `json:"expiresAt"`
	Preconfirmed  bool           `json:"isPreconfirmed"`
	Spent         bool           `json:"isSpent"`
	Swept         bool           `json:"isSwept"`
	Unrolled      bool           `json:"isUnrolled"`
	SpentBy       string         `json:"spentBy"`
	SettledBy     string         `json:"settledBy"`
	ArkTxID       string         `json:"arkTxid"`
	CommitmentIDs []string       `json:"commitmentTxids"`
	Assets        []indexerAsset `json:"assets"`
}

type indexerAsset struct {
	ID     string   `json:"assetId"`
	Amount unsigned `json:"amount"`
}

// ProtoJSON permits original snake_case and lowerCamelCase field names.
// Silently ignoring is_spent/ark_txid would turn evidence of a spent source
// into an apparently unspent one. Normalize keys, preserving exact number
// tokens and rejecting duplicate aliases even when their values agree.
func decodeProtoFields(data []byte, out any) error {
	var fields map[string]json.RawMessage
	if err := decodeObject(data, &fields); err != nil {
		return err
	}
	normalized := make(map[string]json.RawMessage, len(fields))
	for name, value := range fields {
		key := strings.ToLower(strings.ReplaceAll(name, "_", ""))
		if _, exists := normalized[key]; exists {
			return errors.New("duplicate protobuf JSON field")
		}
		normalized[key] = value
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

func (v *indexerVtxo) UnmarshalJSON(data []byte) error {
	type plain indexerVtxo
	return decodeProtoFields(data, (*plain)(v))
}

func (a *indexerAsset) UnmarshalJSON(data []byte) error {
	type plain indexerAsset
	return decodeProtoFields(data, (*plain)(a))
}

func (v indexerVtxo) decode() (ports.Vtxo, error) {
	if v.Outpoint == nil {
		return ports.Vtxo{}, errors.New("missing indexed outpoint")
	}
	var assets []ports.Asset
	for _, a := range v.Assets {
		assets = append(assets, ports.Asset{ID: a.ID, Amount: uint64(a.Amount)})
	}
	return (indexdata.Record{TxID: v.Outpoint.TxID, Vout: v.Outpoint.Vout, Script: v.Script,
		Amount: uint64(v.Amount), CreatedAt: int64(v.CreatedAt), ExpiresAt: int64(v.ExpiresAt),
		Preconfirmed: v.Preconfirmed, Spent: v.Spent, Swept: v.Swept, Unrolled: v.Unrolled,
		SpentBy: v.SpentBy, SettledBy: v.SettledBy, ArkTxID: v.ArkTxID,
		CommitmentTxIDs: v.CommitmentIDs, Assets: assets}).Decode()
}

func (a *Indexer) Vtxos(ctx context.Context, q ports.VtxoQuery) ([]ports.Vtxo, error) {
	return indexdata.Collect(ctx, q, func(ctx context.Context, index int32) ([]ports.Vtxo, *indexdata.Page, error) {
		query := url.Values{"page.size": {strconv.Itoa(indexdata.PageSize)}, "page.index": {strconv.Itoa(int(index))}}
		if len(q.Script) > 0 {
			query.Set("scripts", hex.EncodeToString(q.Script))
		}
		for _, p := range q.Outpoints {
			query.Add("outpoints", p.String())
		}
		var response struct {
			Vtxos []indexerVtxo   `json:"vtxos"`
			Page  *indexdata.Page `json:"page"`
		}
		if err := a.request(ctx, "GET", "/v1/indexer/vtxos?"+query.Encode(), nil, &response); err != nil {
			return nil, nil, err
		}
		if len(response.Vtxos) > indexdata.PageSize {
			return nil, nil, errors.New("indexer page size exceeded")
		}
		var records []ports.Vtxo
		for _, v := range response.Vtxos {
			record, err := v.decode()
			if err != nil {
				return nil, nil, err
			}
			records = append(records, record)
		}
		return records, response.Page, nil
	})
}

func (a *Indexer) Transaction(ctx context.Context, id chainhash.Hash) (*wire.MsgTx, error) {
	var response struct {
		Txs  []string        `json:"txs"`
		Page *indexdata.Page `json:"page"`
	}
	err := a.request(ctx, "GET", "/v1/indexer/virtualTx/"+id.String()+"?page.size=100&page.index=1", nil, &response)
	var status HTTPStatusError
	if errors.As(err, &status) && status == 404 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return indexdata.Transaction(id, response.Txs, response.Page)
}

var _ ports.Indexer = (*Indexer)(nil)
