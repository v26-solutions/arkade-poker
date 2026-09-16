package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	tea "charm.land/bubbletea/v2"
)

func setupModel() *Model {
	m := playing()
	m.clearPublic = [32]byte{2}
	m.snapshot = game.Snapshot{Stage: game.StageAwaitOpponentKeys, Invitation: &game.Invitation{}}
	return m
}

func TestAbortSetupControls(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, role := range []covenant.Player{covenant.Player1, covenant.Player2} {
			t.Run(fmt.Sprintf("browser=%t/player=%d", browser, role), func(t *testing.T) {
				m := setupModel()
				m.host.Browser, m.snapshot.Role = browser, role
				m.balance, m.balanceKnown = 193500, true
				fresh := &client.Session{Receive: m.receive}
				calls := 0
				m.host.AbortSetup = func(_ context.Context, public [32]byte) (*client.Session, error) {
					calls++
					if public != m.clearPublic {
						t.Fatal("wrong wallet")
					}
					return fresh, nil
				}
				m.host.ClearSavedGame = func(context.Context, [32]byte) (*client.Session, error) {
					t.Fatal("abort bypassed the funding check")
					return nil, nil
				}
				clickClearLabel(t, m, "[B] ABORT SETUP")
				if m.modal != abortSetupModal || m.clearConfirm {
					t.Fatal("abort must default to Cancel")
				}
				if cmd := press(m, tea.KeyEnter, 0); cmd != nil || m.modal != noModal || calls != 0 {
					t.Fatal("default confirmation erased setup")
				}
				press(m, 'b', 0)
				press(m, tea.KeyEscape, 0)
				if calls != 0 || m.modal != noModal {
					t.Fatal("escape did not cancel abort")
				}
				press(m, tea.KeyEscape, 0)
				clickClearLabel(t, m, "[B] ABORT SETUP")
				m.Update(tea.WindowSizeMsg{Width: 76, Height: 30})
				view := m.View().Content
				if !strings.Contains(view, "wallet will stay loaded") || !strings.Contains(view, "opponent must abort") || !strings.Contains(view, "Confirm abort") {
					t.Fatal("minimum-size confirmation hides consequences or controls")
				}
				old := m.snapshot
				cmd := clickClearLabel(t, m, "Confirm abort")
				if cmd == nil || !m.clearing || calls != 0 || len(m.actions()) != 0 {
					t.Fatal("abort did not schedule one isolated cleanup")
				}
				press(m, 'b', 0)
				m.Update(driverMsg{update: game.Update{Snapshot: old}})
				m.Update(driverMsg{closed: true})
				m.Update(sentMsg{err: game.ErrDriverStopped})
				m.Update(cmd())
				m.Update(driverMsg{generation: m.generation, update: game.Update{Snapshot: game.Snapshot{
					Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession, game.JoinSession}},
				}}})
				if calls != 1 || m.session != fresh || m.clearing || m.stopped || m.errorText != "" || len(m.actions()) != 2 || m.balance != 193500 || m.snapshot.Invitation != nil {
					t.Fatal("abort did not return to Create/Join with wallet intact")
				}
			})
		}
	}
}

func TestAbortSetupWhileWorkingAndFundingBoundary(t *testing.T) {
	for _, shuffling := range []bool{false, true} {
		m := setupModel()
		m.snapshot = game.Snapshot{Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession, game.JoinSession}}}
		m.busy, m.shuffling, m.selected = !shuffling, shuffling, 1
		m.host.AbortSetup = func(context.Context, [32]byte) (*client.Session, error) { return nil, nil }
		press(m, tea.KeyEnter, 0)
		if m.modal != abortSetupModal {
			t.Fatal("preparing a new session hid abort or retained stale action selection")
		}
	}
	for stage := game.StageInit; stage <= game.StageFinished; stage++ {
		for _, working := range []string{"idle", "busy", "shuffling", "stopped"} {
			t.Run(fmt.Sprintf("stage=%d/%s", stage, working), func(t *testing.T) {
				m := setupModel()
				m.snapshot.Stage = stage
				m.busy, m.shuffling, m.stopped = working == "busy", working == "shuffling", working == "stopped"
				m.host.AbortSetup = func(context.Context, [32]byte) (*client.Session, error) { return nil, nil }
				want := stage >= game.StageSessionPrepared && stage <= game.StageAwaitInitialDeposit
				if got := strings.Contains(m.View().Content, "[B] ABORT SETUP"); got != want {
					t.Fatalf("abort visible = %t, want %t", got, want)
				}
				press(m, 'b', 0)
				if (m.modal == abortSetupModal) != want {
					t.Fatal("keyboard and visible action disagree")
				}
			})
		}
	}
	for _, lateUpdate := range []bool{false, true} {
		m := setupModel()
		calls := 0
		m.host.AbortSetup = func(context.Context, [32]byte) (*client.Session, error) {
			calls++
			return nil, client.ErrSetupFunded
		}
		press(m, 'b', 0)
		if lateUpdate {
			m.Update(driverMsg{update: game.Update{Snapshot: game.Snapshot{Stage: game.StageTransactionPrepared}}})
		}
		press(m, tea.KeyRight, 0)
		cmd := press(m, tea.KeyEnter, 0)
		if lateUpdate {
			if cmd != nil || calls != 0 || m.modal != noModal {
				t.Fatal("stale confirmation invoked abort after funding update")
			}
		} else {
			m.Update(cmd())
			if !m.stopped || m.clearing || calls != 1 || !strings.Contains(m.errorText, "saved game retained") || m.snapshot.Invitation == nil {
				t.Fatal("host funding rejection lost saved setup or hid error")
			}
		}
	}
}
