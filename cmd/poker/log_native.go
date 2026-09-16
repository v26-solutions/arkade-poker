//go:build !js

package main

import "io"

func logOutput() io.Writer { return nil }
