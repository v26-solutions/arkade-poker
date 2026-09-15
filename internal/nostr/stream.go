package nostr

import (
	"bytes"
	"context"
	"sync"

	"arkade-poker/go/internal/ports"
)

// A lifetime reader keeps caller quiet waits from cancelling coder/websocket's
// Read (which would close the connection). Queue overflow fails the session;
// dropping public setup messages silently would break ordering/equivocation.
type relayStream struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	socket   ports.Socket
	frames   chan []byte
	done     chan struct{}
	once     sync.Once
	closeErr error
}

func newRelayStream(parent context.Context, socket ports.Socket) *relayStream {
	ctx, cancel := context.WithCancelCause(parent)
	s := &relayStream{ctx: ctx, cancel: cancel, socket: socket, frames: make(chan []byte, 8), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer s.closeSocket()
		stop := context.AfterFunc(ctx, s.closeSocket)
		defer stop()
		for {
			data, err := socket.Read(ctx)
			if err != nil {
				cancel(err)
				return
			}
			if len(data) > MaxCarrierBytes {
				cancel(ErrLimit)
				return
			}
			select {
			case s.frames <- bytes.Clone(data):
			case <-ctx.Done():
				return
			default:
				cancel(ErrLimit)
				return
			}
		}
	}()
	return s
}
func (s *relayStream) closeSocket() { s.once.Do(func() { s.closeErr = s.socket.Close() }) }
func (s *relayStream) close() error {
	s.cancel(ErrClosed)
	<-s.done
	s.closeSocket()
	return s.closeErr
}
