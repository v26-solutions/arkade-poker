//go:build js && wasm

package merkel

import "time"

// Gosched alone does not return control to the JS event loop. Yield through a
// timer periodically so the terminal renderer and progress spinner keep running.
func cooperate() { time.Sleep(time.Millisecond) }
