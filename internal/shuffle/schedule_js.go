//go:build js && wasm

package shuffle

import "time"

// A timer returns control to the JS event loop; Gosched alone does not. Proof
// work must run in a goroutine, never synchronously in a syscall/js callback.
func cooperate() { time.Sleep(time.Millisecond) }
