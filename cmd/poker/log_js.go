//go:build js && wasm

package main

import (
	"io"
	"strings"
	"syscall/js"
)

func logOutput() (io.WriteCloser, error) { return browserConsole{}, nil }

type browserConsole struct{}

func (browserConsole) Close() error { return nil }

// diagnostics calls this only after redaction; never expose live Go objects.
func (browserConsole) Write(p []byte) (int, error) {
	js.Global().Get("console").Call("log", strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}
