package covenant

import (
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

// Source adds authenticated source material to the upstream builder input.
// Admission must bind Vtxo's outpoint, amount and selected tree to PreviousTx.
// Live unspent/accepted evidence remains the wallet/indexer's responsibility.
type Source struct {
	Vtxo       offchain.VtxoInput
	PreviousTx *wire.MsgTx
}

type Funding struct {
	Inputs []Source
	Change *wire.TxOut // Exact positive remainder, or nil for an exact match.
}

type InputSpending struct {
	InputIndex uint32
	Path       SpendingPath // Main input's checkpoint-output path.
}

// Unsigned holds upstream PSBTs, one checkpoint for each main input in order.
// It does not assert signatures or service acceptance.
type Unsigned struct {
	Ark         *psbt.Packet
	Checkpoints []*psbt.Packet
	Spending    []InputSpending
}

type BetKind uint8

const (
	Check BetKind = iota + 1
	Call
	RaiseTo
)

type BettingAction struct {
	Kind   BetKind
	Amount int64 // Cumulative wager target, used only for RaiseTo.
}
