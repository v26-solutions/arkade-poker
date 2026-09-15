package ports

import (
	"context"
	"fmt"
	"io"
)

// PeerSession supplies only the independent session transport secret. The
// imported wallet key never crosses this boundary. Open must own its copy before
// returning; the caller clears its temporary buffer. Reopening must preserve the
// recorded identity and subscribe to stored messages from the start of the game.
type PeerSession struct {
	RelayURL        string
	SessionID       [32]byte
	Role            uint8
	TransportSecret [32]byte
	PeerPublicKey   *[32]byte
}

func (PeerSession) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "<private peer session>") }

type PeerRequest struct {
	SessionID     [32]byte
	Role          uint8 // sender for Prepare; expected sender for Receive
	Sequence      uint64
	PeerPublicKey *[32]byte
}

// PreparedMessage binds the exact application payload to an opaque signed
// carrier. Prepare must compare its own encoding to Payload before returning.
// The game retains both without parsing or verifying carrier signatures.
type PreparedMessage struct {
	Payload []byte
	Carrier []byte
}
type PeerDelivery struct {
	Identity [32]byte // authenticated by the transport's signature library
	Payload  []byte
}

type PeerTransport interface {
	Open(context.Context, PeerSession) error
	Prepare(context.Context, PeerRequest, []byte, int64) (PreparedMessage, error)
	Publish(context.Context, []byte) error                       // exact persisted carrier; await matching ACK
	Receive(context.Context, PeerRequest) (*PeerDelivery, error) // nil on bounded quiet wait
	Close() error
}
