package wallet

import (
	"bytes"
	"encoding/base64"
	"testing"

	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

func comparisonBundle(t testing.TB) ports.Bundle {
	t.Helper()
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{Sequence: wire.MaxTxInSequenceNum})
	tx.AddTxOut(wire.NewTxOut(100, append([]byte{0x51, 0x20}, make([]byte, 32)...)))
	cp, err := psbt.NewFromUnsignedTx(tx)
	if err != nil {
		t.Fatal(err)
	}
	mainTx := tx.Copy()
	mainTx.TxIn[0].PreviousOutPoint = wire.OutPoint{Hash: tx.TxHash()}
	main, err := psbt.NewFromUnsignedTx(mainTx)
	if err != nil {
		t.Fatal(err)
	}
	main.Inputs[0].WitnessUtxo = wire.NewTxOut(100, append([]byte{0x51, 0x20}, make([]byte, 32)...))
	main.Unknowns = []*psbt.Unknown{{Key: []byte{0xfc, 1}, Value: []byte{1, 2, 3}}}
	a, err := main.B64Encode()
	if err != nil {
		t.Fatal(err)
	}
	b, err := cp.B64Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ports.Bundle{Ark: a, Checkpoints: []string{b}}
}
func appendScriptSignatures(t testing.TB, s string, fields []psbtField) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data[5:])
	if _, err := readPSBTMap(r); err != nil {
		t.Fatal(err)
	}
	input, err := readPSBTMap(r)
	if err != nil {
		t.Fatal(err)
	}
	offset := len(data) - r.Len() - 1
	var extra bytes.Buffer
	for _, f := range fields {
		_ = wire.WriteVarBytes(&extra, 0, f.key)
		_ = wire.WriteVarBytes(&extra, 0, f.value)
	}
	_ = input
	result := append(bytes.Clone(data[:offset]), extra.Bytes()...)
	result = append(result, data[offset:]...)
	return base64.StdEncoding.EncodeToString(result)
}
func TestCompareBundlesPreservesEverythingExceptScriptSignatures(t *testing.T) {
	prepared := comparisonBundle(t)
	for _, fields := range [][]psbtField{nil, {{[]byte{0x14}, nil}}, {{[]byte{0x14, 1}, []byte{0xff}}}, {{[]byte{0x14, 1}, bytes.Repeat([]byte{0}, 7)}, {[]byte{0x14, 2}, bytes.Repeat([]byte{0xff}, 89)}}} {
		signed := ports.Bundle{Ark: appendScriptSignatures(t, prepared.Ark, fields), Checkpoints: []string{appendScriptSignatures(t, prepared.Checkpoints[0], fields)}}
		if err := CompareBundles(prepared, signed); err != nil {
			t.Fatalf("FSM signature gate: %v", err)
		}
		tx, cps, err := BundleTransactions(signed)
		if err != nil || len(cps) != 1 || tx.TxHash() == (wire.OutPoint{}).Hash {
			t.Fatal("opaque signature parsing", err)
		}
		if len(fields) > 0 {
			raw, _ := base64.StdEncoding.DecodeString(signed.Ark)
			if _, err := psbt.NewFromRawBytes(bytes.NewReader(raw), false); err == nil {
				t.Fatal("invalid signature fixture was not invalid to upstream parser")
			}
		}
	}
	for _, tt := range []struct {
		name   string
		change func(*psbt.Packet)
	}{
		{"transaction", func(p *psbt.Packet) { p.UnsignedTx.TxOut[0].Value++ }},
		{"prevout", func(p *psbt.Packet) { p.Inputs[0].WitnessUtxo.Value++ }},
		{"metadata", func(p *psbt.Packet) { p.Unknowns[0].Value[0] ^= 1 }},
		{"selected path", func(p *psbt.Packet) { p.Inputs[0].TaprootMerkleRoot = bytes.Repeat([]byte{1}, 32) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, _, err := unsignedPSBT(prepared.Ark)
			if err != nil {
				t.Fatal(err)
			}
			tt.change(p)
			b, err := p.B64Encode()
			if err != nil {
				t.Fatal(err)
			}
			changed := ports.Bundle{Ark: b, Checkpoints: prepared.Checkpoints}
			if err := CompareBundles(prepared, changed); err == nil {
				t.Fatal("changed non-signature field")
			}
		})
	}
	if err := CompareBundles(prepared, ports.Bundle{Ark: prepared.Ark}); err == nil {
		t.Fatal("missing checkpoints")
	}
}
func TestBoundedTransactionCountsBeforeDecode(t *testing.T) {
	for _, witness := range []bool{false, true} {
		var b bytes.Buffer
		b.Write([]byte{3, 0, 0, 0})
		if witness {
			b.Write([]byte{0, 1})
		}
		_ = wire.WriteVarInt(&b, 0, 257)
		if _, err := DecodeRecordedTransaction(b.Bytes()); err == nil {
			t.Fatal("unbounded input count")
		}
	}
	for _, b := range [][]byte{nil, make([]byte, 10), make([]byte, maxBundleBytes+1)} {
		if _, err := DecodeRecordedTransaction(b); err == nil {
			t.Fatal("bad transaction bounds")
		}
	}
}
func FuzzBundleFraming(f *testing.F) {
	b := comparisonBundle(f)
	f.Add(b.Ark)
	f.Add("cHNidP8=")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > maxBundleBytes+1 {
			return
		}
		_, _, _ = unsignedPSBT(s)
	})
}
