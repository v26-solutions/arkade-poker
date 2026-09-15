// Package websocket uses coder/websocket on both native and browser hosts.
package websocket

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"unicode/utf8"

	"arkade-poker/go/internal/ports"
	"github.com/coder/websocket"
)

const MaxMessage = 20 << 20

type socket struct {
	conn      *websocket.Conn
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func Dial(ctx context.Context, endpoint string) (ports.Socket, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid relay URL")
	}
	c, err := dial(ctx, u.String())
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(MaxMessage)
	return &socket{conn: c, closed: make(chan struct{})}, nil
}
func (s *socket) Read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-s.closed:
		return nil, net.ErrClosed
	default:
	}
	kind, data, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageText {
		return nil, errors.New("relay sent a non-text message")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("relay sent invalid UTF-8")
	}
	return data, nil
}
func (s *socket) Write(ctx context.Context, data []byte) error {
	// coder/websocket's browser Write is nonblocking and ignores ctx. Check it
	// here as well so already-cancelled work never reaches the browser socket.
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-s.closed:
		return net.ErrClosed
	default:
	}
	if len(data) > MaxMessage {
		return errors.New("relay message exceeds byte limit")
	}
	if !utf8.Valid(data) {
		return errors.New("relay message is not UTF-8")
	}
	return s.conn.Write(ctx, websocket.MessageText, data)
}
func (s *socket) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.closeErr = s.close()
		if errors.Is(s.closeErr, net.ErrClosed) {
			s.closeErr = nil
		}
	})
	return s.closeErr
}
