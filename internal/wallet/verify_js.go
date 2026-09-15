//go:build js && wasm

package wallet

import "arkade-poker/go/internal/ports"

// The browser delegates signature verification to Arkd/emulator submission and
// finalization. Identity/metadata merging above remains common to both hosts.
func verifyServiceSignatures(_, _ ports.Bundle, _ [32]byte) error { return nil }
