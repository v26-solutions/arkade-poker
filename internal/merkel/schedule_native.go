//go:build !js

package merkel

import "runtime"

func cooperate() { runtime.Gosched() }
