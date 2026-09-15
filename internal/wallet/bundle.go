package wallet

import (
	"bytes"
	"encoding/base64"
	"errors"
	"sort"

	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

const maxBundleBytes = 4 * 1024 * 1024

type psbtField struct{ key, value []byte }

// readPSBTMap is bounded framing, not signature parsing. Script signatures are
// opaque: PSBT's general parser otherwise enforces signature sizes before the
// FSM could apply its approved signature-verification boundary.
func readPSBTMap(r *bytes.Reader) ([]psbtField, error) {
	var fields []psbtField
	seen := make(map[string]bool)
	for {
		n, err := wire.ReadVarInt(r, 0)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return fields, nil
		}
		if n > uint64(r.Len()) || n > maxBundleBytes {
			return nil, errors.New("PSBT key length")
		}
		key := make([]byte, int(n))
		_, _ = r.Read(key)
		n, err = wire.ReadVarInt(r, 0)
		if err != nil || n > uint64(r.Len()) || n > maxBundleBytes {
			return nil, errors.New("PSBT value length")
		}
		value := make([]byte, int(n))
		_, _ = r.Read(value)
		if seen[string(key)] {
			return nil, errors.New("duplicate PSBT key")
		}
		seen[string(key)] = true
		fields = append(fields, psbtField{key, value})
	}
}
func writePSBTMap(w *bytes.Buffer, fields []psbtField, signatures bool) {
	sort.Slice(fields, func(i, j int) bool { return bytes.Compare(fields[i].key, fields[j].key) < 0 })
	for _, f := range fields {
		// 0x14 is PSBT_IN_TAP_SCRIPT_SIG. No signature presence/count/key shape,
		// sighash/type/content check is performed here or in the game reducer.
		if signatures && f.key[0] == byte(psbt.TaprootScriptSpendSignatureType) {
			continue
		}
		_ = wire.WriteVarBytes(w, 0, f.key)
		_ = wire.WriteVarBytes(w, 0, f.value)
	}
	_ = w.WriteByte(0)
}
func unsignedPSBT(encoded string) (*psbt.Packet, []byte, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(maxBundleBytes) {
		return nil, nil, errors.New("PSBT length")
	}
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, nil, err
	}
	if base64.StdEncoding.EncodeToString(b) != encoded || len(b) < 5 || !bytes.Equal(b[:5], []byte{'p', 's', 'b', 't', 0xff}) {
		return nil, nil, errors.New("PSBT encoding")
	}
	r := bytes.NewReader(b[5:])
	global, err := readPSBTMap(r)
	if err != nil {
		return nil, nil, err
	}
	var tx *wire.MsgTx
	for _, f := range global {
		if len(f.key) == 1 && f.key[0] == byte(psbt.UnsignedTxType) {
			tx, err = DecodeRecordedTransaction(f.value)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	if tx == nil || len(tx.TxIn) == 0 || len(tx.TxIn) > 256 || len(tx.TxOut) > 256 {
		return nil, nil, errors.New("PSBT transaction shape")
	}
	var normalized bytes.Buffer
	normalized.Write(b[:5])
	writePSBTMap(&normalized, global, false)
	for range tx.TxIn {
		m, err := readPSBTMap(r)
		if err != nil {
			return nil, nil, err
		}
		writePSBTMap(&normalized, m, true)
	}
	for range tx.TxOut {
		m, err := readPSBTMap(r)
		if err != nil {
			return nil, nil, err
		}
		writePSBTMap(&normalized, m, false)
	}
	if r.Len() != 0 {
		return nil, nil, errors.New("PSBT trailing bytes")
	}
	packet, err := psbt.NewFromRawBytes(bytes.NewReader(normalized.Bytes()), false)
	if err != nil {
		return nil, nil, err
	}
	return packet, normalized.Bytes(), nil
}

// BundleTransactions parses non-signature PSBT metadata through the upstream
// library and returns owned transaction bodies. It establishes no acceptance.
func BundleTransactions(bundle ports.Bundle) (*wire.MsgTx, []*wire.MsgTx, error) {
	if err := bundleBound(bundle); err != nil {
		return nil, nil, err
	}
	if len(bundle.Checkpoints) > 256 {
		return nil, nil, errors.New("checkpoint count")
	}
	p, _, err := unsignedPSBT(bundle.Ark)
	if err != nil {
		return nil, nil, err
	}
	if len(bundle.Checkpoints) != len(p.UnsignedTx.TxIn) {
		return nil, nil, errors.New("checkpoint count")
	}
	cps := make([]*wire.MsgTx, 0, len(bundle.Checkpoints))
	for _, b := range bundle.Checkpoints {
		cp, _, err := unsignedPSBT(b)
		if err != nil {
			return nil, nil, err
		}
		cps = append(cps, cp.UnsignedTx.Copy())
	}
	return p.UnsignedTx.Copy(), cps, nil
}

// CompareBundles compares every transaction and PSBT metadata field, allowing
// only input Taproot script-signature changes. It does not establish signature
// validity/completeness or service acceptance. Original strings remain untouched
// for durable exact-work retries.
func CompareBundles(prepared, signed ports.Bundle) error {
	if err := bundleBound(prepared); err != nil {
		return err
	}
	if err := bundleBound(signed); err != nil {
		return err
	}
	if len(prepared.Checkpoints) != len(signed.Checkpoints) || len(prepared.Checkpoints) > 256 {
		return errors.New("signed checkpoint count")
	}
	a := append([]string{prepared.Ark}, prepared.Checkpoints...)
	b := append([]string{signed.Ark}, signed.Checkpoints...)
	for i := range a {
		_, left, err := unsignedPSBT(a[i])
		if err != nil {
			return err
		}
		_, right, err := unsignedPSBT(b[i])
		if err != nil {
			return err
		}
		if !bytes.Equal(left, right) {
			return errors.New("signed bundle changed prepared transaction or metadata")
		}
	}
	return nil
}

func bundleBound(bundle ports.Bundle) error {
	size := len(bundle.Ark)
	if size > maxBundleBytes {
		return errors.New("bundle length")
	}
	for _, p := range bundle.Checkpoints {
		if len(p) > maxBundleBytes-size {
			return errors.New("bundle length")
		}
		size += len(p)
	}
	return nil
}
