// Package storage provides durable private session recording with one writer per
// local wallet store. It never accepts or derives an imported wallet private key.
package storage

import (
	"context"
	"errors"
)

var (
	ErrLocked      = errors.New("storage: wallet store already open")
	ErrClosed      = errors.New("storage: closed")
	ErrCorrupt     = errors.New("storage: damaged or unsupported log")
	ErrIndex       = errors.New("storage: record index mismatch")
	ErrRecord      = errors.New("storage: invalid record size")
	ErrUncertain   = errors.New("storage: failed append; close and reload before continuing")
	ErrUnavailable = errors.New("storage: required durable storage or writer lock unavailable")
)

const MaxRecordBytes = 4 << 20

// Log is the host storage boundary. Game owns the canonical event codec. Records
// may contain session secrets but must never contain the imported wallet key.
// Open must acquire exclusive local writer ownership before returning a Log.
// Milestone storage is unencrypted and offers no recovery after data loss.
type Log interface {
	// Load returns the complete intact record sequence, or an error.
	Load(context.Context) ([][]byte, error)
	// Append checks the expected zero-based record index and acknowledges only
	// after a durable write. Failure stops the driver's state advancement.
	Append(ctx context.Context, expectedIndex uint64, record []byte) error
	// Close releases writer ownership without removing session data.
	Close() error
}
