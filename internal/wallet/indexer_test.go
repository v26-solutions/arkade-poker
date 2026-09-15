package wallet

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// These fixtures test indexer evidence admission, not poker scripts. Script
// behavior must be checked with actual action PSBTs in the real emulator.
type evidenceIndexer struct {
	records map[wire.OutPoint]ports.Vtxo
	txs     map[chainhash.Hash]*wire.MsgTx
	err     error
	txCalls int
}

func (i *evidenceIndexer) Vtxos(ctx context.Context, q ports.VtxoQuery) ([]ports.Vtxo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if i.err != nil {
		return nil, i.err
	}
	var result []ports.Vtxo
	if len(q.Script) > 0 {
		for _, v := range i.records {
			if bytes.Equal(v.Script, q.Script) {
				result = append(result, v)
			}
		}
	} else {
		for _, p := range q.Outpoints {
			if v, ok := i.records[p]; ok {
				result = append(result, v)
			}
		}
	}
	return result, nil
}
func (i *evidenceIndexer) Transaction(ctx context.Context, h chainhash.Hash) (*wire.MsgTx, error) {
	i.txCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if i.err != nil {
		return nil, i.err
	}
	if tx := i.txs[h]; tx != nil {
		return tx.Copy(), nil
	}
	return nil, nil
}
func (*evidenceIndexer) Close() error { return nil }

type evidenceFixture struct {
	source, checkpoint, main *wire.MsgTx
}

func p2tr(b byte) []byte { return append([]byte{0x51, 0x20}, bytes.Repeat([]byte{b}, 32)...) }

func newEvidence() *evidenceFixture {
	source := wire.NewMsgTx(3)
	source.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}}, nil, nil))
	source.AddTxOut(wire.NewTxOut(1000, p2tr(2)))
	cp := wire.NewMsgTx(3)
	cp.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: source.TxHash()}, nil, nil))
	cp.AddTxOut(wire.NewTxOut(1000, p2tr(3)))
	cp.AddTxOut(wire.NewTxOut(0, []byte{0x51, 2, 0x4e, 0x73}))
	main := wire.NewMsgTx(3)
	main.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: cp.TxHash()}, nil, nil))
	main.AddTxOut(wire.NewTxOut(600, p2tr(4)))
	main.AddTxOut(wire.NewTxOut(400, p2tr(5)))
	main.AddTxOut(wire.NewTxOut(0, []byte{0x51, 2, 0x4e, 0x73}))
	return &evidenceFixture{source, cp, main}
}

func (f *evidenceFixture) index() *evidenceIndexer {
	f.checkpoint.TxIn[0].PreviousOutPoint.Hash = f.source.TxHash()
	for _, input := range f.main.TxIn {
		input.PreviousOutPoint.Hash = f.checkpoint.TxHash()
	}
	i := &evidenceIndexer{records: make(map[wire.OutPoint]ports.Vtxo), txs: make(map[chainhash.Hash]*wire.MsgTx)}
	for _, tx := range []*wire.MsgTx{f.source, f.checkpoint, f.main} {
		i.txs[tx.TxHash()] = tx.Copy()
	}
	source := wire.OutPoint{Hash: f.source.TxHash()}
	cpID, mainID := f.checkpoint.TxHash(), f.main.TxHash()
	i.records[source] = ports.Vtxo{Outpoint: source, Script: f.source.TxOut[0].PkScript, Amount: f.source.TxOut[0].Value,
		Spent: true, SpentBy: &cpID, ArkTxID: &mainID, ExpiresAt: 2_000_000_000}
	for vout, out := range f.main.TxOut {
		if len(out.PkScript) != 34 {
			continue
		}
		p := wire.OutPoint{Hash: mainID, Index: uint32(vout)}
		i.records[p] = ports.Vtxo{Outpoint: p, Script: out.PkScript, Amount: out.Value, Preconfirmed: true, ExpiresAt: 2_000_000_000}
	}
	return i
}

func TestFinalizationEvidenceBeforeBody(t *testing.T) {
	f := newEvidence()
	i := f.index()
	first := wire.OutPoint{Hash: f.main.TxHash()}
	for _, situation := range []string{"missing", "batch", "pending_other_output"} {
		t.Run(situation, func(t *testing.T) {
			i := f.index()
			switch situation {
			case "missing":
				delete(i.records, first)
			case "batch":
				v := i.records[first]
				v.Preconfirmed = false
				i.records[first] = v
			case "pending_other_output":
				delete(i.records, wire.OutPoint{Hash: first.Hash, Index: 1})
			}
			a, err := Accepted(context.Background(), i, f.main.TxHash())
			if err != nil || a != nil {
				t.Fatal("nonfinalized main admitted", a, err)
			}
			if situation != "pending_other_output" && i.txCalls != 0 {
				t.Fatal("queried unknown body before output evidence")
			}
		})
	}
	// Finalized outputs remain acceptance evidence after being spent/swept.
	v := i.records[first]
	v.Spent, v.Swept = true, true
	i.records[first] = v
	a, err := Accepted(context.Background(), i, f.main.TxHash())
	if err != nil || a == nil || len(a.Checkpoints) != 1 || len(a.Sources) != 1 {
		t.Fatal(a, err)
	}
	if a.Sources[0].Outpoint.Hash != f.source.TxHash() {
		t.Fatal("lost source identity")
	}
	byScript, err := AcceptedByScript(context.Background(), i, firstScript(i, first))
	if err != nil || len(byScript) != 1 {
		t.Fatal("historical discovery", byScript, err)
	}
}

func firstScript(i *evidenceIndexer, point wire.OutPoint) []byte { return i.records[point].Script }

func TestRejectInvalidAcceptedEvidence(t *testing.T) {
	for _, fault := range []string{"output_amount", "output_script", "output_preconfirmed", "source_amount", "source_script", "source_unspent", "wrong_main", "wrong_checkpoint", "missing_source", "missing_checkpoint", "missing_source_body", "checkpoint_anchor", "checkpoint_version", "checkpoint_locktime", "checkpoint_index", "duplicate_source", "unbalanced", "negative", "overflow"} {
		t.Run(fault, func(t *testing.T) {
			f := newEvidence()
			switch fault {
			case "checkpoint_anchor":
				f.checkpoint.TxOut[1].Value = 1
			case "checkpoint_version":
				f.checkpoint.Version = 2
			case "checkpoint_locktime":
				f.checkpoint.LockTime = 1
			case "checkpoint_index":
				f.main.TxIn[0].PreviousOutPoint.Index = 1
			case "duplicate_source":
				f.main.AddTxIn(wire.NewTxIn(&wire.OutPoint{}, nil, nil))
			case "unbalanced":
				f.main.TxOut[0].Value--
			case "negative":
				f.main.TxOut[2].Value = -1
			case "overflow":
				f.main.TxOut[2].Value = btcutil.MaxSatoshi
			}
			i := f.index()
			first, source := wire.OutPoint{Hash: f.main.TxHash()}, wire.OutPoint{Hash: f.source.TxHash()}
			out, src := i.records[first], i.records[source]
			bad := chainhash.Hash{99}
			switch fault {
			case "output_amount":
				out.Amount++
			case "output_script":
				out.Script = p2tr(99)
			case "output_preconfirmed":
				p := wire.OutPoint{Hash: f.main.TxHash(), Index: 1}
				v := i.records[p]
				v.Preconfirmed = false
				i.records[p] = v
			case "source_amount":
				src.Amount++
			case "source_script":
				src.Script = p2tr(99)
			case "source_unspent":
				src.Spent = false
			case "wrong_main":
				src.ArkTxID = &bad
			case "wrong_checkpoint":
				src.SpentBy = &bad
			case "missing_checkpoint":
				delete(i.txs, f.checkpoint.TxHash())
			case "missing_source_body":
				delete(i.txs, f.source.TxHash())
			}
			i.records[first], i.records[source] = out, src
			if fault == "missing_source" {
				delete(i.records, source)
			}
			a, err := Accepted(context.Background(), i, f.main.TxHash())
			if err == nil || a != nil {
				t.Fatal("invalid evidence admitted", a, err)
			}
		})
	}
}

func TestSpendObservationStates(t *testing.T) {
	for _, state := range []SourceState{SourceMissing, SourceUnspent, SourcePending, SourceAccepted, SourceUnavailable} {
		f := newEvidence()
		i := f.index()
		p := wire.OutPoint{Hash: f.source.TxHash()}
		v := i.records[p]
		switch state {
		case SourceMissing:
			delete(i.records, p)
		case SourceUnspent:
			v.Spent, v.SpentBy, v.ArkTxID = false, nil, nil
			i.records[p] = v
		case SourcePending:
			delete(i.records, wire.OutPoint{Hash: f.main.TxHash()})
		case SourceUnavailable:
			v.ArkTxID, v.Swept = nil, true
			i.records[p] = v
		}
		o, err := InspectSpend(context.Background(), i, p)
		if err != nil || o.State != state || (o.Accepted != nil) != (state == SourceAccepted) {
			t.Fatal(state, o, err)
		}
	}
	f := newEvidence()
	i := f.index()
	i.err = errors.New("indexer unavailable")
	o, err := InspectSpend(context.Background(), i, wire.OutPoint{Hash: f.source.TxHash()})
	if err == nil || o.State != SourceUnknown {
		t.Fatal("query failure became usable source", o, err)
	}
	i = f.index()
	p := wire.OutPoint{Hash: f.source.TxHash()}
	v := i.records[p]
	v.ArkTxID = nil
	i.records[p] = v
	if _, err := InspectSpend(context.Background(), i, p); err == nil {
		t.Fatal("incomplete spent record became unspent")
	}
}

func TestCompetingSpendWithLeadingDataOutput(t *testing.T) {
	f := newEvidence()
	f.main.TxOut = append([]*wire.TxOut{wire.NewTxOut(0, []byte{0x6a})}, f.main.TxOut...)
	i := f.index()
	o, err := InspectSpend(context.Background(), i, wire.OutPoint{Hash: f.source.TxHash()})
	if err != nil || o.State != SourceAccepted {
		t.Fatal(o, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Accepted(ctx, i, f.main.TxHash()); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation", err)
	}
}
