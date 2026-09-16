package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"arkade-poker/go/internal/diagnostics"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

func TestLogShortcutAcrossHostsAndStates(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, state := range []modal{noModal, exitModal, tokenModal, joinConfirmModal, clearGameModal, allInModal, payoutModal, menuModal, abortSetupModal} {
			for _, key := range []tea.KeyPressMsg{{Code: 'l'}, {Code: 'L'}, {Code: 'l', Mod: tea.ModShift, Text: "L"}} {
				t.Run(fmt.Sprintf("browser=%t/modal=%d/key=%s", browser, state, key), func(t *testing.T) {
					l := diagnostics.New(nil)
					l.Logger().Info("before keypress", "error", errors.New(strings.Repeat("ab", 32)))
					var copied string
					m := New(context.Background(), Host{Browser: browser, Logs: l.Snapshot,
						CopyText: func(text string) error { copied = text; return nil }})
					m.modal = state
					m.busy, m.stopped, m.shuffling, m.clearing = true, true, true, true
					m.errorText = "Existing game error"
					_, cmd := m.Update(key)
					if cmd == nil {
						t.Fatal("shortcut unavailable")
					}
					l.Logger().Info("after keypress")
					m.Update(cmd())
					if !strings.Contains(copied, "before keypress") || strings.Contains(copied, "after keypress") || strings.Contains(copied, strings.Repeat("ab", 32)) {
						t.Fatal("clipboard did not receive the current redacted snapshot")
					}
					if m.modal != state || !m.busy || !m.stopped || !m.shuffling || !m.clearing || m.errorText != "Existing game error" {
						t.Fatal("copy changed application state")
					}
					if !strings.Contains(m.View().Content, "Logs copied") {
						t.Fatal("copy feedback hidden")
					}
				})
			}
		}
	}
}

func TestLogShortcutLeavesTextAndPasteAlone(t *testing.T) {
	for _, state := range []modal{walletModal, createModal, joinModal, raiseModal} {
		m := New(context.Background(), Host{Logs: func() string { t.Fatal("read logs while typing"); return "" }})
		m.modal = state
		m.input.Focus()
		field := textinput.New()
		field.Focus()
		m.fields = []textinput.Model{field}
		for _, text := range []string{"l", "L"} {
			m.Update(tea.KeyPressMsg{Code: rune(text[0]), Text: text})
		}
		input := m.input.Value()
		if state != walletModal {
			input = m.fields[0].Value()
		}
		if input != "lL" || m.copyingLogs {
			t.Fatalf("modal %d lost typed letters: %q", state, input)
		}
	}
	m := New(context.Background(), Host{})
	m.openWallet()
	m.Update(tea.PasteMsg{Content: "lL wallet input"})
	if m.input.Value() != "lL wallet input" || m.copyingLogs {
		t.Fatal("paste intercepted")
	}
	for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModAlt, tea.ModMeta, tea.ModSuper} {
		m.modal = noModal
		if m.logShortcut(tea.KeyPressMsg{Code: 'l', Mod: mod}) {
			t.Fatal("modified shortcut intercepted")
		}
	}
}

func TestLogCopyFailureAndRepeatedPresses(t *testing.T) {
	copies := 0
	m := New(context.Background(), Host{Logs: func() string { return "logs" }, CopyText: func(string) error { copies++; return errors.New("denied") }})
	cmd := press(m, 'l', 0)
	if again := press(m, 'l', 0); again != nil {
		t.Fatal("duplicate in-flight copy")
	}
	m.Update(cmd())
	if copies != 1 || !strings.Contains(m.View().Content, "Could not copy logs") {
		t.Fatal("failure hidden")
	}
	m.width, m.height = 50, 12
	if !strings.Contains(m.View().Content, "Could not copy logs") {
		t.Fatal("failure hidden in small window")
	}
	m.host.CopyText = func(string) error { return nil }
	m.Update(press(m, 'l', 0)())
	if m.copyingLogs || !strings.Contains(m.View().Content, "Logs copied") {
		t.Fatal("retry failed")
	}
}
