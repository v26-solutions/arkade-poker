package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/storage"
	"arkade-poker/go/internal/wallet"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func clickClearLabel(t *testing.T, m *Model, label string) tea.Cmd {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if i := strings.Index(line, label); i >= 0 {
			_, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: lipgloss.Width(line[:i]), Y: y})
			return cmd
		}
	}
	t.Fatalf("missing control %q", label)
	return nil
}

func TestClearAfterFailedWalletImport(t *testing.T) {
	for _, browser := range []bool{false, true} {
		t.Run(fmt.Sprintf("browser=%t", browser), func(t *testing.T) {
			key, err := wallet.ParseKey(fmt.Sprintf("%064x", 20), "")
			if err != nil {
				t.Fatal(err)
			}
			public := key.PublicKey()
			calls := 0
			m := New(t.Context(), Host{Browser: browser, InitialKey: key,
				ConnectSession: func(context.Context, *wallet.Key) (*client.Session, error) {
					return nil, errors.New("game: invalid protocol evidence: initial deposit packet or output")
				}, ClearSavedGame: func(_ context.Context, got [32]byte) (*client.Session, error) {
					calls++
					if got != public {
						t.Fatal("clear targeted another wallet")
					}
					return nil, nil
				}})
			cmd := m.connect()
			press(m, 'x', 0)
			if m.modal != noModal {
				t.Fatal("clear available during import")
			}
			m.Update(cmd())
			if key.PublicKey() != [32]byte{} || m.key != nil || m.clearPublic != public || calls != 0 {
				t.Fatal("failure must destroy key, retain public identity, and preserve history")
			}
			diagnostic := m.errorText
			clickClearLabel(t, m, "[X] Clear saved game")
			if m.modal != clearGameModal || m.clearConfirm {
				t.Fatal("clear confirmation must default to Cancel")
			}
			m.Update(tea.WindowSizeMsg{Width: 76, Height: 30})
			view := m.View().Content
			if lipgloss.Width(view) > 76 || lipgloss.Height(view) > 30 || !strings.Contains(view, "does not refund") {
				t.Fatal("clear confirmation overflows or omits consequences")
			}
			if cmd := press(m, tea.KeyEnter, 0); cmd != nil || m.modal != noModal || calls != 0 || m.errorText != diagnostic {
				t.Fatal("default Enter did not preserve failed game")
			}
			press(m, 'x', 0)
			press(m, tea.KeyEscape, 0)
			if m.errorText != diagnostic || m.modal != noModal {
				t.Fatal("Escape changed failed restore")
			}
			press(m, 'x', 0)
			press(m, tea.KeyRight, 0)
			cmd = press(m, tea.KeyEnter, 0)
			if cmd == nil || !m.clearing || calls != 0 {
				t.Fatal("confirmation must schedule one clear command")
			}
			press(m, 'a', 0)
			press(m, 'x', 0)
			if m.modal != noModal {
				t.Fatal("import or duplicate clear allowed while clearing")
			}
			m.Update(cmd())
			if calls != 1 || m.clearing || m.errorText != "" || m.canClearGame() || m.session != nil {
				t.Fatal("clear did not return to clean wallet entry")
			}
			press(m, 'a', 0)
			if m.modal != walletModal {
				t.Fatal("wallet cannot be imported again")
			}
		})
	}
}

func TestClearFailureRetryAndStaleMessages(t *testing.T) {
	m := playing()
	m.stopped = true
	key, err := wallet.ParseKey(fmt.Sprintf("%064x", 20), "")
	if err != nil {
		t.Fatal(err)
	}
	defer key.Destroy()
	m.key = key
	m.clearPublic = key.PublicKey()
	m.balance, m.balanceKnown = 123456, true
	receive := m.receive
	fresh := &client.Session{Receive: receive}
	calls := 0
	m.host.ClearSavedGame = func(context.Context, [32]byte) (*client.Session, error) {
		calls++
		if calls == 1 {
			return nil, storage.ErrLocked
		}
		return fresh, nil
	}
	old := m.snapshot
	press(m, 'x', 0)
	cmd := clickClearLabel(t, m, "Confirm clear")
	if cmd == nil {
		t.Fatal("mouse confirmation did not clear")
	}
	m.Update(cmd())
	if !strings.Contains(m.errorText, storage.ErrLocked.Error()) || !m.canClearGame() {
		t.Fatal("failed clear must expose error and allow retry")
	}
	// These commands were already pending when clear detached the old session.
	for _, msg := range []tea.Msg{
		driverMsg{update: game.Update{Snapshot: old}}, driverMsg{closed: true},
		balanceMsg{update: client.BalanceUpdate{Sats: 999}}, balanceMsg{closed: true},
		sentMsg{err: game.ErrDriverStopped},
	} {
		m.Update(msg)
	}
	if m.snapshot.State != nil || m.stopped || !m.balanceKnown || m.balanceFailed || m.balance != 123456 {
		t.Fatal("stale session result repopulated cleared UI")
	}
	if m.key != key || key.PublicKey() != m.clearPublic || m.receive.Address != receive.Address {
		t.Fatal("clear failure unloaded the wallet")
	}
	press(m, 'x', 0)
	m.Update(tea.WindowSizeMsg{Width: 65, Height: 20})
	if !strings.Contains(m.View().Content, "Confirm clear") {
		t.Fatal("small terminal hid clear confirmation")
	}
	press(m, tea.KeyRight, 0)
	m.Update(press(m, tea.KeyEnter, 0)())
	if calls != 2 || m.errorText != "" || m.canClearGame() {
		t.Fatal("retry did not complete clear")
	}
	if m.key != key || m.session != fresh || m.receive.Address != receive.Address || m.balance != 123456 {
		t.Fatal("clear did not preserve the loaded wallet")
	}
	freshSnapshot := game.Snapshot{Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession, game.JoinSession}}}
	m.Update(driverMsg{generation: m.generation, update: game.Update{Snapshot: freshSnapshot}})
	m.Update(balanceMsg{generation: m.generation, update: client.BalanceUpdate{Sats: 654321}})
	if len(m.actions()) != 2 || m.balance != 654321 {
		t.Fatal("new session did not resume actions and balance updates")
	}
}

func TestClearGameAvailability(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*Model)
		want  bool
	}{
		{"active hand", func(*Model) {}, false},
		{"waiting for opponent", func(m *Model) { m.snapshot = game.Snapshot{Stage: game.StageAwaitOpponentKeys} }, false},
		{"fresh wallet", func(m *Model) {
			m.snapshot = game.Snapshot{Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession, game.JoinSession}}}
		}, false},
		{"completed hand", func(m *Model) {
			m.snapshot.Outcome = &game.Outcome{Kind: game.Won}
			m.snapshot.Stage = game.StageFinished
		}, false},
		{"aborted setup", func(m *Model) {
			m.snapshot.Outcome = &game.Outcome{Kind: game.Aborted}
			m.snapshot.Stage = game.StageAborted
		}, false},
		{"stopped driver", func(m *Model) { m.stopped = true; m.snapshot = game.Snapshot{} }, true},
		{"restoring", func(m *Model) { m.snapshot = game.Snapshot{} }, false},
		{"connecting", func(m *Model) { m.connecting = true }, false},
		{"clearing", func(m *Model) { m.clearing = true }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := playing()
			m.width = 120
			m.clearPublic = [32]byte{2}
			m.host.ClearSavedGame = func(context.Context, [32]byte) (*client.Session, error) { t.Fatal("unexpected clear"); return nil, nil }
			test.setup(m)
			if got := strings.Contains(m.View().Content, "[X] Clear saved game"); got != test.want {
				t.Fatalf("header clear availability = %t, want %t", got, test.want)
			}
			press(m, 'x', 0)
			if (m.modal == clearGameModal) != test.want {
				t.Fatal("keyboard disagrees with header availability")
			}
			m.modal = menuModal
			if got := strings.Contains(m.View().Content, "[X] CLEAR SAVED GAME"); got != test.want {
				t.Fatal("menu disagrees with header availability")
			}
		})
	}
}
