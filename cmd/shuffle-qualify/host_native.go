//go:build !js

package main

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"fmt"
	"time"
)

const isBrowser = false

func startCommand() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return startMsg{} })
}
func finishCommand() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return tea.Quit() })
}
func installStart(func()) {}
func publish(report)      {}
func finished(r report)   { b, _ := json.Marshal(r); fmt.Printf("\nQUALIFICATION %s\n", b) }
