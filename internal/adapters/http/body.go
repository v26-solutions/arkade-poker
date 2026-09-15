package http

import (
	"context"
	"io"
	"sync"
)

// Go's browser Fetch transport stops observing the request context once it has
// returned response headers. Explicitly cancel the ReadableStream too, so a
// blocked body read cannot prevent stream Close or adapter shutdown.
type contextBody struct {
	io.ReadCloser
	once sync.Once
	stop func() bool
	err  error
}

func bindBody(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	b := &contextBody{ReadCloser: body}
	b.stop = context.AfterFunc(ctx, b.close)
	return b
}
func (b *contextBody) close()       { b.once.Do(func() { b.err = b.ReadCloser.Close() }) }
func (b *contextBody) Close() error { b.stop(); b.close(); return b.err }
