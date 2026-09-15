package wallet

import (
	"bytes"
	"errors"
	"github.com/btcsuite/btcd/wire"
)

const maxRecordedScriptBytes = 10000

var errTransactionEncoding = errors.New("invalid bounded transaction encoding")

// Preflight only the native Bitcoin framing/counts before wire allocates its
// object graph. wire remains the authority for transaction decoding/encoding.
func transactionBounds(b []byte) error {
	r := bytes.NewReader(b)
	skip := func(n int64) bool {
		if n < 0 || n > int64(r.Len()) {
			return false
		}
		_, err := r.Seek(n, 1)
		return err == nil
	}
	if !skip(4) {
		return errTransactionEncoding
	}
	n, err := wire.ReadVarInt(r, 0)
	if err != nil {
		return errTransactionEncoding
	}
	witness := false
	if n == 0 {
		flag, err := r.ReadByte()
		if err != nil || flag != 1 {
			return errTransactionEncoding
		}
		witness = true
		n, err = wire.ReadVarInt(r, 0)
		if err != nil {
			return errTransactionEncoding
		}
	}
	if n > 256 {
		return errTransactionEncoding
	}
	inputs := n
	for range inputs {
		if !skip(36) {
			return errTransactionEncoding
		}
		if _, err := wire.ReadVarBytes(r, 0, maxRecordedScriptBytes, "script"); err != nil {
			return errTransactionEncoding
		}
		if !skip(4) {
			return errTransactionEncoding
		}
	}
	n, err = wire.ReadVarInt(r, 0)
	if err != nil || n > 256 {
		return errTransactionEncoding
	}
	for range n {
		if !skip(8) {
			return errTransactionEncoding
		}
		if _, err := wire.ReadVarBytes(r, 0, maxRecordedScriptBytes, "script"); err != nil {
			return errTransactionEncoding
		}
	}
	if witness {
		for range inputs {
			n, err := wire.ReadVarInt(r, 0)
			if err != nil || n > 256 {
				return errTransactionEncoding
			}
			for range n {
				if _, err := wire.ReadVarBytes(r, 0, maxRecordedScriptBytes, "witness"); err != nil {
					return errTransactionEncoding
				}
			}
		}
	}
	if !skip(4) || r.Len() != 0 {
		return errTransactionEncoding
	}
	return nil
}

// DecodeRecordedTransaction preflights resource bounds, then delegates decoding
// to wire. This performs no signature, acceptance or poker-rule verification.
func DecodeRecordedTransaction(b []byte) (*wire.MsgTx, error) {
	if len(b) > maxBundleBytes {
		return nil, errTransactionEncoding
	}
	if err := transactionBounds(b); err != nil {
		return nil, err
	}
	tx := wire.NewMsgTx(2)
	r := bytes.NewReader(b)
	if err := tx.Deserialize(r); err != nil {
		return nil, err
	}
	if r.Len() != 0 {
		return nil, errTransactionEncoding
	}
	return tx, nil
}
