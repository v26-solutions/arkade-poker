package ports

import (
	"context"
	"errors"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

var ErrNotImplemented = errors.New("ports: not implemented")

type ScriptEventKind uint8

const (
	ScriptAttached ScriptEventKind = iota + 1
	ScriptChanged
	ScriptObservationGap
)

// ScriptEvent is a reconciliation hint, never evidence of an accepted spend.
// A gap requires indexer reconciliation before the driver resumes effects.
type ScriptEvent struct {
	Kind       ScriptEventKind
	Script     []byte
	TxID       chainhash.Hash
	NewVtxos   []wire.OutPoint
	SpentVtxos []wire.OutPoint
}

type ScriptSubscription interface {
	Next(context.Context) (ScriptEvent, error)
	Close() error
}

// ScriptSubscriber is the streaming boundary implemented by both indexer hosts.
// It is separate from the existing query port so read-only evidence consumers
// need not implement subscriptions. Subscribe must await an actual attachment
// frame, retaining any early transaction event. HTTP 200 or a timeout is not
// attachment; reconnect must report the observation gap and reattach explicitly.
type ScriptSubscriber interface {
	Subscribe(ctx context.Context, scripts [][]byte) (ScriptSubscription, error)
}
