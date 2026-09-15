//go:build !js

package websocket

import (
	"context"
	"github.com/coder/websocket"
)

func dial(ctx context.Context, endpoint string) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, endpoint, nil)
	return c, err
}

func (s *socket) read(ctx context.Context) (websocket.MessageType, []byte, error) {
	return s.conn.Read(ctx)
}

func (s *socket) close() error { return s.conn.CloseNow() }
