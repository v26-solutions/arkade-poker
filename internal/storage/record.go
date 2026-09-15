package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
)

// The storage frame detects damage independently of the game's event codec. It
// binds each record to its index and wallet, and is identical on both hosts.
const recordHeader = "arkade-poker/log\x00\x01\x00"
const frameOverhead = len(recordHeader) + 32 + 8 + 4 + sha256.Size

func frame(wallet [32]byte, index uint64, record []byte) ([]byte, error) {
	if len(record) == 0 || len(record) > MaxRecordBytes {
		return nil, ErrRecord
	}
	b := make([]byte, 0, len(record)+frameOverhead)
	b = append(b, recordHeader...)
	b = append(b, wallet[:]...)
	b = binary.LittleEndian.AppendUint64(b, index)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(record)))
	b = append(b, record...)
	h := sha256.Sum256(b)
	return append(b, h[:]...), nil
}

func unframe(wallet [32]byte, index uint64, b []byte) ([]byte, error) {
	if len(b) <= frameOverhead || len(b) > MaxRecordBytes+frameOverhead || !bytes.HasPrefix(b, []byte(recordHeader)) {
		return nil, ErrCorrupt
	}
	n := len(recordHeader)
	if !bytes.Equal(b[n:n+32], wallet[:]) || binary.LittleEndian.Uint64(b[n+32:n+40]) != index ||
		uint64(binary.LittleEndian.Uint32(b[n+40:n+44])) != uint64(len(b)-frameOverhead) {
		return nil, ErrCorrupt
	}
	h := sha256.Sum256(b[:len(b)-sha256.Size])
	if !bytes.Equal(h[:], b[len(b)-sha256.Size:]) {
		return nil, ErrCorrupt
	}
	return bytes.Clone(b[n+44 : len(b)-sha256.Size]), nil
}
