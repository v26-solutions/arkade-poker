//go:build !js

package shuffle

import "runtime"

func cooperate() { runtime.Gosched() }
