package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"arkade-poker/go/internal/game"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func clickHelpControl(t *testing.T, m *Model, id string) tea.Cmd {
	t.Helper()
	for _, r := range m.layout().hits {
		if r.id == id {
			_, cmd := m.Update(tea.MouseClickMsg{X: r.rect.Min.X, Y: r.rect.Min.Y, Button: tea.MouseLeft})
			return cmd
		}
	}
	t.Fatalf("missing control %q", id)
	return nil
}

func TestHelpPreservesCoveredFormsAndBlocksActions(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, form := range []modal{walletModal, createModal, joinModal, raiseModal, exitModal, clearGameModal} {
			t.Run(fmt.Sprintf("browser=%t/form=%d", browser, form), func(t *testing.T) {
				m := playing()
				m.host.Browser = browser
				m.openForm(createModal, "123", "456", "789", "1000", "wss://relay")
				m.input.SetValue("wallet input")
				m.modal = form
				m.errorText = "Existing error"
				m.previousModal = walletModal
				m.exitConfirm, m.clearConfirm = true, true
				before := ansi.Strip(m.View().Content)
				clickHelpControl(t, m, "help")
				if !m.helpOpen {
					t.Fatal("Help did not open")
				}
				for _, key := range []rune{'a', 'c', 'f', 'r', tea.KeyTab} {
					if cmd := press(m, key, 0); cmd != nil {
						t.Fatal("covered action produced a command")
					}
				}
				m.Update(tea.PasteMsg{Content: "unwanted paste"})
				for _, r := range m.layout().hits {
					if strings.HasPrefix(r.id, "action:") || r.id == "confirm" || r.id == "wallet" {
						t.Fatalf("covered control remains clickable: %s", r.id)
					}
				}
				press(m, tea.KeyEscape, 0)
				if m.helpOpen || m.modal != form || m.previousModal != walletModal || !m.exitConfirm || !m.clearConfirm || ansi.Strip(m.View().Content) != before {
					t.Fatal("Help changed the covered form or confirmation")
				}
			})
		}
	}
}

func TestHelpScrollingAndPinnedControls(t *testing.T) {
	m := New(context.Background(), Host{Logs: func() string { return "diagnostics" }, CopyText: func(s string) error {
		if s != "diagnostics" {
			t.Fatal("wrong log contents")
		}
		return nil
	}})
	m.Update(tea.KeyPressMsg{Code: '/', Mod: tea.ModShift, Text: "?"})
	if !m.helpOpen {
		t.Fatal("shifted question mark did not open Help")
	}
	press(m, tea.KeyEnd, 0)
	end := m.helpScroll
	if end == 0 || !strings.Contains(ansi.Strip(m.View().Content), "Copy Logs above") {
		t.Fatal("end of instructions is unreachable")
	}
	press(m, tea.KeyDown, 0)
	if m.helpScroll != end {
		t.Fatal("scrolled beyond end")
	}
	clickHelpControl(t, m, "help-up")
	if m.helpScroll >= end {
		t.Fatal("scroll control did not move instructions")
	}
	press(m, tea.KeyHome, 0)
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.helpScroll == 0 {
		t.Fatal("mouse wheel did not scroll")
	}
	press(m, tea.KeyPgDown, 0)
	cmd := clickHelpControl(t, m, "logs")
	if cmd == nil {
		t.Fatal("pinned log control is unavailable")
	}
	m.Update(cmd())
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "[L] Copy Logs") || !strings.Contains(view, "Logs copied") {
		t.Fatal("log control or feedback scrolled away")
	}
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	if m.helpScroll != 0 || !strings.Contains(ansi.Strip(m.View().Content), "GET STARTED") {
		t.Fatal("resize did not clamp scroll position")
	}
	clickHelpControl(t, m, "help-close")
	if m.helpOpen {
		t.Fatal("close control did not dismiss Help")
	}
}

func TestHelpAvailableDuringWaitsAndSmallWindows(t *testing.T) {
	for _, size := range [][2]int{{20, 8}, {50, 12}, {76, 30}, {120, 44}} {
		m := New(context.Background(), Host{})
		m.width, m.height = size[0], size[1]
		m.busy, m.clearing, m.stopped, m.shuffling = true, true, true, true
		for i := 0; i < 2; i++ {
			v := ansi.Strip(m.View().Content)
			if !strings.HasPrefix(strings.Split(v, "\n")[size[1]-2], "│ [?] Help │") {
				t.Fatalf("Help missing from far-left status bar at %v", size)
			}
			if lipgloss.Width(v) != size[0] || lipgloss.Height(v) != size[1] {
				t.Fatalf("layout exceeds %v", size)
			}
			clickHelpControl(t, m, "help")
		}
		if m.helpOpen {
			t.Fatal("Help could not close during wait")
		}
	}
}

func TestHelpDoesNotRestoreStaleBet(t *testing.T) {
	m := playing()
	m.openRaise()
	press(m, '?', 0)
	next := m.snapshot
	next.Stage, next.Choice = game.StageBettingOpponent, nil
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
	press(m, tea.KeyEscape, 0)
	if m.modal != noModal || len(m.fields) != 0 || press(m, tea.KeyEnter, 0) != nil {
		t.Fatal("Help restored a stale bet")
	}
}
