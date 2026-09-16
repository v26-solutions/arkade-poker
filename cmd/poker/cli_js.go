//go:build js && wasm

package main

import "io"

func runCLI([]string, io.Reader, io.Writer) (bool, error) { return false, nil }
