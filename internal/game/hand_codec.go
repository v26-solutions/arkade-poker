package game

import (
	"bytes"
	"encoding/base64"
	"reflect"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/merkel"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/shuffle"
	"arkade-poker/go/internal/wallet"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

func checkTxSyntax(tx *wire.MsgTx) error {
	if tx == nil || len(tx.TxIn) > 256 || len(tx.TxOut) > 256 {
		return ErrEncoding
	}
	for _, in := range tx.TxIn {
		if in == nil || len(in.SignatureScript) > maxScriptBytes || len(in.Witness) > 256 {
			return ErrEncoding
		}
		for _, v := range in.Witness {
			if len(v) > maxScriptBytes {
				return ErrEncoding
			}
		}
	}
	for _, out := range tx.TxOut {
		if out == nil || len(out.PkScript) > maxScriptBytes {
			return ErrEncoding
		}
	}
	return nil
}
func encodeTx(w *encoder, tx *wire.MsgTx) {
	if err := checkTxSyntax(tx); err != nil {
		w.err = err
		return
	}
	var b bytes.Buffer
	if err := tx.Serialize(&b); err != nil {
		w.err = err
		return
	}
	w.blob(b.Bytes(), MaxEventBytes)
}

func decodeTx(r *decoder) *wire.MsgTx {
	b := r.blob(MaxEventBytes)
	tx, err := wallet.DecodeRecordedTransaction(b)
	if err != nil {
		r.err = err
		return nil
	}
	if err := checkTxSyntax(tx); err != nil {
		r.err = err
		return nil
	}
	return tx
}
func encodeOutpoint(w *encoder, p wire.OutPoint) { w.raw(p.Hash[:]); w.u32(p.Index) }
func decodeOutpoint(r *decoder) wire.OutPoint {
	var p wire.OutPoint
	copy(p.Hash[:], r.raw(32))
	p.Index = r.u32()
	return p
}
func encodeFunding(w *encoder, f covenant.Funding) {
	if len(f.Inputs) > 255 {
		w.err = ErrEncoding
		return
	}
	w.u32(uint32(len(f.Inputs)))
	for _, s := range f.Inputs {
		v := s.Vtxo
		if v.Outpoint == nil || v.Tapscript == nil || v.Tapscript.ControlBlock == nil || v.Tapscript.ControlBlock.InternalKey == nil {
			w.err = ErrEncoding
			return
		}
		encodeOutpoint(w, *v.Outpoint)
		w.u64(uint64(v.Amount))
		encodeTx(w, s.PreviousTx)
		t := v.Tapscript
		w.u8(byte(t.Type))
		cb, err := t.ControlBlock.ToBytes()
		if err != nil {
			w.err = err
			return
		}
		w.blob(cb, maxScriptBytes)
		w.blob(t.RevealedScript, maxScriptBytes)
		if len(t.Leaves) > 256 {
			w.err = ErrEncoding
			return
		}
		w.u32(uint32(len(t.Leaves)))
		for _, leaf := range t.Leaves {
			w.u8(byte(leaf.LeafVersion))
			w.blob(leaf.Script, maxScriptBytes)
		}
		w.blob(t.RootHash, 32)
		if t.FullOutputKey == nil {
			w.u8(0)
		} else {
			w.u8(1)
			w.raw(t.FullOutputKey.SerializeCompressed())
		}
		if len(v.RevealedTapscripts) > 256 {
			w.err = ErrEncoding
			return
		}
		w.u32(uint32(len(v.RevealedTapscripts)))
		for _, s := range v.RevealedTapscripts {
			w.str(s, 2*maxScriptBytes)
		}
	}
	if f.Change == nil {
		w.u8(0)
	} else {
		w.u8(1)
		w.u64(uint64(f.Change.Value))
		w.blob(f.Change.PkScript, maxScriptBytes)
	}
}
func count(r *decoder, max, minBytes int) int {
	n := r.u32()
	if r.err != nil || uint64(n) > uint64(max) || uint64(n)*uint64(minBytes) > uint64(len(r.data)-r.pos) {
		r.err = ErrEncoding
		return 0
	}
	return int(n)
}
func decodeFunding(r *decoder) covenant.Funding {
	var f covenant.Funding
	n := count(r, 255, 50)
	for range n {
		op := decodeOutpoint(r)
		v := offchain.VtxoInput{Outpoint: &op, Amount: int64(r.u64())}
		tx := decodeTx(r)
		t := &waddrmgr.Tapscript{Type: waddrmgr.TapscriptType(r.u8())}
		cb, err := txscript.ParseControlBlock(r.blob(maxScriptBytes))
		if err != nil {
			r.err = err
		}
		t.ControlBlock = cb
		t.RevealedScript = r.blob(maxScriptBytes)
		for range count(r, 256, 5) {
			t.Leaves = append(t.Leaves, txscript.TapLeaf{LeafVersion: txscript.TapscriptLeafVersion(r.u8()), Script: r.blob(maxScriptBytes)})
		}
		t.RootHash = r.blob(32)
		switch r.u8() {
		case 0:
		case 1:
			key, err := btcec.ParsePubKey(r.raw(33))
			if err != nil {
				r.err = err
			}
			t.FullOutputKey = key
		default:
			r.err = ErrEncoding
		}
		for range count(r, 256, 4) {
			v.RevealedTapscripts = append(v.RevealedTapscripts, r.str(2*maxScriptBytes))
		}
		v.Tapscript = t
		f.Inputs = append(f.Inputs, covenant.Source{Vtxo: v, PreviousTx: tx})
	}
	switch r.u8() {
	case 0:
	case 1:
		f.Change = wire.NewTxOut(int64(r.u64()), r.blob(maxScriptBytes))
	default:
		r.err = ErrEncoding
	}
	return f
}
func encodeCardReveal(w *encoder, r covenant.CardReveal) { w.crypto(r.Share); w.crypto(r.Proof) }
func decodeCardReveal(r *decoder) covenant.CardReveal {
	share, err := shuffle.DecodeRevealToken(r.raw(33))
	if err != nil {
		r.err = err
	}
	proof, err := shuffle.DecodeRevealProof(r.raw(98))
	if err != nil {
		r.err = err
	}
	return covenant.CardReveal{Share: share, Proof: proof}
}
func encodeReveals(w *encoder, v covenant.RevealWitness) {
	w.u8(byte(v.Kind))
	fields := revealFields(&v)
	if len(fields) == 0 {
		w.err = ErrEncoding
		return
	}
	for _, p := range fields {
		encodeCardReveal(w, *p)
		*p = covenant.CardReveal{}
	}
	v.Kind = 0
	if !reflect.ValueOf(v).IsZero() {
		w.err = ErrEncoding
	}
}
func decodeReveals(r *decoder) covenant.RevealWitness {
	w := covenant.RevealWitness{Kind: covenant.RevealKind(r.u8())}
	fields := revealFields(&w)
	if len(fields) == 0 {
		r.err = ErrEncoding
		return w
	}
	for _, p := range fields {
		*p = decodeCardReveal(r)
	}
	return w
}
func encodeShowdown(w *encoder, s covenant.ShowdownWitness) {
	cards := dealOrder(s.Cards)
	w.raw(cards[:])
	for _, p := range []merkel.HandProof{s.RankProofs.Player1, s.RankProofs.Player2} {
		w.u16(p.Rank)
		w.crypto(p.Proof)
	}
}
func decodeShowdown(r *decoder) covenant.ShowdownWitness {
	cards := [9]byte(r.raw(9))
	var proofs [2]merkel.HandProof
	for i := range proofs {
		proofs[i].Rank = r.u16()
		p, err := merkel.DecodeProof(r.raw(merkel.TreeDepth * 32))
		if err != nil {
			r.err = err
		}
		proofs[i].Proof = p
	}
	return covenant.ShowdownWitness{Cards: dealFrom(cards), RankProofs: covenant.PerPlayer[merkel.HandProof]{Player1: proofs[0], Player2: proofs[1]}}
}
func encodeAction(w *encoder, a PreparedAction) {
	mask := 0
	if a.Funding != nil {
		mask |= 1
	}
	if a.Reveals != nil {
		mask |= 2
	}
	if a.Showdown != nil {
		mask |= 4
	}
	expected := 0
	switch a.Kind {
	case ActionInitialDeposit:
		expected = 1
	case ActionPlayer1Funding, ActionPlayer2Opening, ActionAllInCall:
		expected = 3
	case ActionBetting:
		expected = 1
	case ActionBoardReveal, ActionShowdownReveal, ActionAllInReveal:
		expected = 2
	case ActionShowdown:
		expected = 4
	case ActionConcession, ActionTimeout:
	default:
		w.err = ErrEncoding
		return
	}
	bet := a.Kind == ActionPlayer2Opening || a.Kind == ActionBetting
	timed := a.Kind == ActionTimeout || a.Kind == ActionShowdown
	if mask != expected || !bet && a.Bet != (covenant.BettingAction{}) || !timed && a.ObservedAt != 0 {
		w.err = ErrEncoding
		return
	}
	w.u8(byte(a.Kind))
	if bet {
		if a.Bet.Kind < covenant.Check || a.Bet.Kind > covenant.RaiseTo || a.Bet.Kind != covenant.RaiseTo && a.Bet.Amount != 0 {
			w.err = ErrEncoding
			return
		}
		w.u8(byte(a.Bet.Kind))
		w.u64(uint64(a.Bet.Amount))
	}
	if a.Funding != nil {
		encodeFunding(w, *a.Funding)
	}
	if a.Reveals != nil {
		encodeReveals(w, *a.Reveals)
	}
	if a.Showdown != nil {
		encodeShowdown(w, *a.Showdown)
	}
	if timed {
		w.u64(uint64(a.ObservedAt))
	}
}
func decodeAction(r *decoder) PreparedAction {
	a := PreparedAction{Kind: ActionKind(r.u8())}
	if a.Kind == ActionPlayer2Opening || a.Kind == ActionBetting {
		a.Bet = covenant.BettingAction{Kind: covenant.BetKind(r.u8()), Amount: int64(r.u64())}
	}
	switch a.Kind {
	case ActionInitialDeposit, ActionPlayer1Funding, ActionPlayer2Opening, ActionAllInCall, ActionBetting:
		f := decodeFunding(r)
		a.Funding = &f
	}
	switch a.Kind {
	case ActionPlayer1Funding, ActionPlayer2Opening, ActionAllInCall, ActionBoardReveal, ActionShowdownReveal, ActionAllInReveal:
		v := decodeReveals(r)
		a.Reveals = &v
	}
	if a.Kind == ActionShowdown {
		v := decodeShowdown(r)
		a.Showdown = &v
	}
	if a.Kind == ActionTimeout || a.Kind == ActionShowdown {
		a.ObservedAt = covenant.UnixSeconds(r.u64())
	}
	return a
}
func encodeBundle(w *encoder, b ports.Bundle) {
	if len(b.Checkpoints) > 256 {
		w.err = ErrEncoding
		return
	}
	for _, s := range append([]string{b.Ark}, b.Checkpoints...) {
		if len(s) > base64.StdEncoding.EncodedLen(MaxEventBytes) {
			w.err = ErrEncoding
			return
		}
	}
	// Keep exact base64 bytes, including signature fields that an upstream PSBT
	// parser would otherwise reject. Non-signature parsing is delegated below.
	if _, _, err := wallet.BundleTransactions(b); err != nil {
		w.err = err
		return
	}
	w.str(b.Ark, MaxEventBytes)
	w.u32(uint32(len(b.Checkpoints)))
	for _, p := range b.Checkpoints {
		w.str(p, MaxEventBytes)
	}
}
func decodeBundle(r *decoder) ports.Bundle {
	b := ports.Bundle{Ark: r.str(MaxEventBytes)}
	for range count(r, 256, 4) {
		b.Checkpoints = append(b.Checkpoints, r.str(MaxEventBytes))
	}
	return b
}
func encodeSaved(w *encoder, s SavedSpend) {
	w.u8(byte(s.Route))
	if s.Source == nil {
		w.u8(0)
	} else {
		w.u8(1)
		encodeOutpoint(w, *s.Source)
	}
	if len(s.WalletSources) > 255 {
		w.err = ErrEncoding
		return
	}
	w.u32(uint32(len(s.WalletSources)))
	for _, op := range s.WalletSources {
		encodeOutpoint(w, op)
	}
	encodeAction(w, s.Action)
	encodeBundle(w, s.Prepared)
	if s.Signed == nil {
		w.u8(0)
	} else {
		w.u8(1)
		encodeBundle(w, *s.Signed)
	}
}
func decodeSaved(r *decoder) SavedSpend {
	s := SavedSpend{Route: wallet.Route(r.u8())}
	switch r.u8() {
	case 0:
	case 1:
		op := decodeOutpoint(r)
		s.Source = &op
	default:
		r.err = ErrEncoding
	}
	for range count(r, 255, 36) {
		s.WalletSources = append(s.WalletSources, decodeOutpoint(r))
	}
	s.Action = decodeAction(r)
	s.Prepared = decodeBundle(r)
	switch r.u8() {
	case 0:
	case 1:
		b := decodeBundle(r)
		s.Signed = &b
	default:
		r.err = ErrEncoding
	}
	return s
}
func encodeAccepted(w *encoder, a AcceptedTransaction) {
	encodeTx(w, a.Transaction)
	if len(a.Checkpoints) > 256 {
		w.err = ErrEncoding
		return
	}
	w.u32(uint32(len(a.Checkpoints)))
	for _, tx := range a.Checkpoints {
		encodeTx(w, tx)
	}
}
func decodeAccepted(r *decoder) AcceptedTransaction {
	a := AcceptedTransaction{Transaction: decodeTx(r)}
	for range count(r, 256, 4) {
		a.Checkpoints = append(a.Checkpoints, decodeTx(r))
	}
	return a
}
func encodeReceipt(w *encoder, p SubmissionReceipt) {
	if p.Recovery {
		w.u8(1)
	} else {
		w.u8(0)
	}
	if p.TxID == nil {
		w.u8(0)
	} else {
		w.u8(1)
		w.raw(p.TxID[:])
	}
}
func decodeReceipt(r *decoder) SubmissionReceipt {
	var p SubmissionReceipt
	switch r.u8() {
	case 0:
	case 1:
		p.Recovery = true
	default:
		r.err = ErrEncoding
	}
	switch r.u8() {
	case 0:
	case 1:
		id := chainhash.Hash(r.raw(32))
		p.TxID = &id
	default:
		r.err = ErrEncoding
	}
	return p
}
