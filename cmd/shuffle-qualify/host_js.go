//go:build js && wasm

package main

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"syscall/js"
)

const isBrowser = true

func startCommand() tea.Cmd  { return nil }
func finishCommand() tea.Cmd { return nil }

var startCallback js.Func

func installStart(start func()) {
	startCallback = js.FuncOf(func(js.Value, []js.Value) any { go start(); return nil })
	js.Global().Set("startShuffleQualification", startCallback)
}
func publish(r report) {
	b, _ := json.Marshal(r)
	js.Global().Call("finishShuffleQualification", string(b))
}
func finished(report) {}
