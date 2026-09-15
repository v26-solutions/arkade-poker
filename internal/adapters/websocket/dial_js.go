//go:build js && wasm

package websocket

import (
	"context"
	"net"

	"github.com/coder/websocket"
)

// Bound the caller independently of browser teardown. A successful connection
// arriving after cancellation gets a best-effort normal close.
func dial(ctx context.Context, endpoint string) (*websocket.Conn, error) {
	type result struct {
		conn *websocket.Conn
		err  error
	}
	done := make(chan result)
	go func() {
		c, _, err := websocket.Dial(ctx, endpoint, nil)
		select {
		case done <- result{c, err}:
		case <-ctx.Done():
			if c != nil {
				_ = c.Close(websocket.StatusNormalClosure, "")
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-done:
		return r.conn, r.err
	}
}

func (s *socket) read(ctx context.Context) (websocket.MessageType, []byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		kind websocket.MessageType
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		kind, data, err := s.conn.Read(ctx)
		done <- result{kind, data, err}
	}()
	// Upstream read cancellation can wait behind an in-progress Close. Local
	// session teardown must not wait for the browser's close handshake.
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case <-s.closed:
		return 0, nil, net.ErrClosed
	case r := <-done:
		return r.kind, r.data, r.err
	}
}

func (s *socket) close() error {
	// Browser CloseNow uses code 1001, which the JS API rejects. Request normal
	// closure instead; remote cleanup is best effort after local session exit.
	go func() { _ = s.conn.Close(websocket.StatusNormalClosure, "") }()
	return nil
}
