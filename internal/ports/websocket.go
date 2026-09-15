package ports

import "context"

// Socket carries text messages with application size limits and explicit
// ownership. The caller owns contexts/deadlines; Close interrupts pending IO.
type Socket interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close() error
}
type DialSocket func(context.Context, string) (Socket, error)
