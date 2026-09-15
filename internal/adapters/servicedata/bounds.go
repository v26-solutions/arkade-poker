// Package servicedata bounds opaque service messages without taking ownership
// of wallet policy, PSBT admission or signature verification.
package servicedata

import (
	"arkade-poker/go/internal/ports"
	"errors"
)

const MaxBytes = 20 << 20
const MaxCheckpoints = 256

func Bundle(b ports.Bundle) error { return Strings(b.Ark, b.Checkpoints) }
func Strings(head string, items []string) error {
	if len(items) > MaxCheckpoints || len(head) > MaxBytes {
		return errors.New("service bundle exceeds limit")
	}
	size := len(head)
	for _, item := range items {
		if len(item) > MaxBytes-size {
			return errors.New("service bundle exceeds limit")
		}
		size += len(item)
	}
	return nil
}
