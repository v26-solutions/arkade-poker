package ports

import (
	"context"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// Vtxo is indexer evidence, not a promise that an output is spendable. In
// particular, Spent/ArkTxID can appear before the successor is finalized.
// Optional transaction identities are nil when absent. Times are Unix seconds.
type Vtxo struct {
	Outpoint                             wire.OutPoint
	Script                               []byte
	Amount                               int64
	CreatedAt, ExpiresAt                 int64
	Preconfirmed, Spent, Swept, Unrolled bool
	SpentBy, SettledBy, ArkTxID          *chainhash.Hash
	CommitmentTxIDs                      []chainhash.Hash
	Assets                               []Asset
}

type Asset struct {
	ID     string
	Amount uint64
}

// VtxoQuery selects one P2TR script OR specific outpoints. Queries include
// spent, swept and otherwise terminal records, which recovery must observe.
type VtxoQuery struct {
	Script    []byte
	Outpoints []wire.OutPoint
}

type Indexer interface {
	// Vtxos returns the complete bounded result or an error, never a partial
	// list. An absent record is different from an explicitly unspent record.
	Vtxos(context.Context, VtxoQuery) ([]Vtxo, error)
	// Transaction returns a txid-matched transaction or nil if absent. Body
	// availability alone does not establish Ark acceptance or finalization.
	Transaction(context.Context, chainhash.Hash) (*wire.MsgTx, error)
	Close() error
}
