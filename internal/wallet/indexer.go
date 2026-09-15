package wallet

import (
	"bytes"
	"context"
	"errors"

	"arkade-poker/go/internal/ports"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// AcceptedTransaction has finalized indexed P2TR outputs and checked logical
// source -> checkpoint -> main linkage and values. Checkpoints and Sources
// follow main input order. This is evidence from the configured Ark indexer,
// not local consensus/script or signature verification. Poker-specific action
// admission remains the covenant/game's responsibility.
type AcceptedTransaction struct {
	Transaction *wire.MsgTx
	Checkpoints []*wire.MsgTx
	Sources     []ports.Vtxo
}

type SourceState uint8

const (
	SourceUnknown     SourceState = iota // only returned with an error
	SourceMissing                        // missing does not authorize retry
	SourceUnspent                        // explicit unspent record; no successor evidence
	SourcePending                        // spent, but no finalized successor yet
	SourceAccepted                       // validated finalized Ark successor
	SourceUnavailable                    // settled, swept or unrolled; no live Ark source
)

type SpendObservation struct {
	State    SourceState
	Source   *ports.Vtxo
	Accepted *AcceptedTransaction
}

// Accepted looks for a poker main, whose first output is P2TR. Probe its
// finalized output before its body: the pinned service may return Internal for
// unknown bodies, and a saved transaction not yet submitted is normal here.
func Accepted(ctx context.Context, indexer ports.Indexer, id chainhash.Hash) (*AcceptedTransaction, error) {
	first, err := indexedOutput(ctx, indexer, wire.OutPoint{Hash: id})
	if err != nil || first == nil {
		return nil, err
	}
	if !first.Preconfirmed {
		return nil, nil
	}
	tx, err := requiredTransaction(ctx, indexer, id)
	if err != nil {
		return nil, err
	}
	return admitAccepted(ctx, indexer, tx, *first)
}

// InspectSpend never conflates query failure, missing records or a pending
// finalization with an explicitly unspent output. The durable driver uses this
// distinction before deciding whether its exact saved work may be retried.
func InspectSpend(ctx context.Context, indexer ports.Indexer, source wire.OutPoint) (SpendObservation, error) {
	record, err := indexedOutput(ctx, indexer, source)
	if err != nil {
		return SpendObservation{}, err
	}
	if record == nil {
		return SpendObservation{State: SourceMissing}, nil
	}
	previous, err := requiredTransaction(ctx, indexer, source.Hash)
	if err != nil {
		return SpendObservation{}, err
	}
	if err := checkOutput(*record, previous); err != nil {
		return SpendObservation{}, err
	}
	observation := SpendObservation{Source: record}
	if record.ArkTxID == nil {
		if record.SettledBy != nil || record.Swept || record.Unrolled {
			observation.State = SourceUnavailable
			return observation, nil
		}
		if record.Spent || record.SpentBy != nil {
			return SpendObservation{}, errors.New("incomplete indexed spent source")
		}
		observation.State = SourceUnspent
		return observation, nil
	}
	if !record.Spent || record.SpentBy == nil {
		return SpendObservation{}, errors.New("incomplete indexed Ark successor")
	}
	observation.State = SourcePending
	tx, err := indexer.Transaction(ctx, *record.ArkTxID)
	if err != nil {
		return SpendObservation{}, err
	}
	if tx == nil {
		return observation, nil
	}
	if tx.TxHash() != *record.ArkTxID {
		return SpendObservation{}, errors.New("indexed successor identity")
	}
	if len(tx.TxIn) == 0 || len(tx.TxIn) > 256 || len(tx.TxOut) > 256 {
		return SpendObservation{}, errors.New("indexed successor shape")
	}
	// A competing ordinary funding spend may have a leading data output.
	// Locate its actual first P2TR output instead of assuming output zero.
	for vout, out := range tx.TxOut {
		if !txscript.IsPayToTaproot(out.PkScript) {
			continue
		}
		first, err := indexedOutput(ctx, indexer, wire.OutPoint{Hash: tx.TxHash(), Index: uint32(vout)})
		if err != nil {
			return SpendObservation{}, err
		}
		if first == nil {
			return observation, nil
		}
		accepted, err := admitAccepted(ctx, indexer, tx, *first)
		if err != nil {
			return SpendObservation{}, err
		}
		if accepted == nil {
			return observation, nil
		}
		links := 0
		for _, cp := range accepted.Checkpoints {
			if cp.TxIn[0].PreviousOutPoint == source {
				if cp.TxHash() != *record.SpentBy {
					return SpendObservation{}, errors.New("unrelated logical successor")
				}
				links++
			}
		}
		if links != 1 {
			return SpendObservation{}, errors.New("unrelated logical successor")
		}
		observation.State, observation.Accepted = SourceAccepted, accepted
		return observation, nil
	}
	return observation, nil
}

// AcceptedByScript includes historical outputs spent onward. It is used to
// discover deposits and to catch up after a gap in a script subscription.
func AcceptedByScript(ctx context.Context, indexer ports.Indexer, script []byte) ([]*AcceptedTransaction, error) {
	if !txscript.IsPayToTaproot(script) {
		return nil, errors.New("accepted discovery requires P2TR")
	}
	records, err := indexer.Vtxos(ctx, ports.VtxoQuery{Script: script})
	if err != nil {
		return nil, err
	}
	var result []*AcceptedTransaction
	seen := make(map[chainhash.Hash]*AcceptedTransaction)
	for _, record := range records {
		if !bytes.Equal(record.Script, script) {
			return nil, errors.New("unrelated script query result")
		}
		if !record.Preconfirmed {
			continue
		}
		id := record.Outpoint.Hash
		if accepted, found := seen[id]; found {
			if accepted != nil {
				if err := checkOutput(record, accepted.Transaction); err != nil {
					return nil, err
				}
			}
			continue
		}
		tx, err := requiredTransaction(ctx, indexer, id)
		if err != nil {
			return nil, err
		}
		accepted, err := admitAccepted(ctx, indexer, tx, record)
		if err != nil {
			return nil, err
		}
		seen[id] = accepted
		if accepted != nil {
			result = append(result, accepted)
		}
	}
	return result, nil
}

func indexedOutput(ctx context.Context, indexer ports.Indexer, point wire.OutPoint) (*ports.Vtxo, error) {
	records, err := indexer.Vtxos(ctx, ports.VtxoQuery{Outpoints: []wire.OutPoint{point}})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	if len(records) != 1 || records[0].Outpoint != point {
		return nil, errors.New("indexed outpoint identity")
	}
	return &records[0], nil
}

func requiredTransaction(ctx context.Context, indexer ports.Indexer, id chainhash.Hash) (*wire.MsgTx, error) {
	tx, err := indexer.Transaction(ctx, id)
	if err != nil {
		return nil, err
	}
	if tx == nil || tx.TxHash() != id {
		return nil, errors.New("missing or unrelated indexed transaction")
	}
	return tx, nil
}

func checkOutput(record ports.Vtxo, tx *wire.MsgTx) error {
	if record.Outpoint.Hash != tx.TxHash() || uint64(record.Outpoint.Index) >= uint64(len(tx.TxOut)) ||
		record.Amount < 0 || record.Amount > btcutil.MaxSatoshi {
		return errors.New("indexed output identity or amount")
	}
	out := tx.TxOut[record.Outpoint.Index]
	if record.Amount != out.Value || !bytes.Equal(record.Script, out.PkScript) {
		return errors.New("indexed output does not match transaction")
	}
	return nil
}

func admitAccepted(ctx context.Context, indexer ports.Indexer, tx *wire.MsgTx, first ports.Vtxo) (*AcceptedTransaction, error) {
	if len(tx.TxIn) == 0 || len(tx.TxIn) > 256 || len(tx.TxOut) > 256 {
		return nil, errors.New("accepted transaction shape")
	}
	if err := checkOutput(first, tx); err != nil {
		return nil, err
	}
	if !first.Preconfirmed || !txscript.IsPayToTaproot(first.Script) {
		return nil, errors.New("accepted main output is not preconfirmed P2TR")
	}
	id := tx.TxHash()
	var outputTotal int64
	for vout, out := range tx.TxOut {
		if out.Value < 0 || out.Value > btcutil.MaxSatoshi-outputTotal {
			return nil, errors.New("accepted output amount overflow")
		}
		outputTotal += out.Value
		if uint32(vout) == first.Outpoint.Index || !txscript.IsPayToTaproot(out.PkScript) {
			continue
		}
		record, err := indexedOutput(ctx, indexer, wire.OutPoint{Hash: id, Index: uint32(vout)})
		if err != nil || record == nil {
			return nil, err
		}
		if err := checkOutput(*record, tx); err != nil {
			return nil, err
		}
		if !record.Preconfirmed {
			return nil, errors.New("accepted output is not preconfirmed")
		}
	}
	accepted := &AcceptedTransaction{Transaction: tx}
	seenSources := make(map[wire.OutPoint]bool)
	anchor := txutils.AnchorOutput()
	var inputTotal int64
	for _, input := range tx.TxIn {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cp, err := requiredTransaction(ctx, indexer, input.PreviousOutPoint.Hash)
		if err != nil {
			return nil, err
		}
		if input.PreviousOutPoint.Index != 0 || len(cp.TxIn) != 1 || len(cp.TxOut) != 2 ||
			cp.Version != 3 || cp.LockTime != 0 || !txscript.IsPayToTaproot(cp.TxOut[0].PkScript) ||
			cp.TxOut[1].Value != anchor.Value || !bytes.Equal(cp.TxOut[1].PkScript, anchor.PkScript) {
			return nil, errors.New("invalid logical checkpoint edge")
		}
		source := cp.TxIn[0].PreviousOutPoint
		if seenSources[source] {
			return nil, errors.New("duplicate logical source")
		}
		seenSources[source] = true
		record, err := indexedOutput(ctx, indexer, source)
		if err != nil {
			return nil, err
		}
		if record == nil || !record.Spent || record.ArkTxID == nil || *record.ArkTxID != id ||
			record.SpentBy == nil || *record.SpentBy != cp.TxHash() || record.Amount != cp.TxOut[0].Value {
			return nil, errors.New("source checkpoint main linkage mismatch")
		}
		previous, err := requiredTransaction(ctx, indexer, source.Hash)
		if err != nil {
			return nil, err
		}
		if err := checkOutput(*record, previous); err != nil {
			return nil, err
		}
		if record.Amount > btcutil.MaxSatoshi-inputTotal {
			return nil, errors.New("accepted input amount overflow")
		}
		inputTotal += record.Amount
		accepted.Checkpoints = append(accepted.Checkpoints, cp)
		accepted.Sources = append(accepted.Sources, *record)
	}
	if inputTotal != outputTotal {
		return nil, errors.New("accepted transaction is not balanced")
	}
	return accepted, nil
}
