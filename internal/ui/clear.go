package ui

import (
	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/game"
	tea "charm.land/bubbletea/v2"
)

type clearedGameMsg struct {
	session *client.Session
	err     error
	abort   bool
}

func (m *Model) canAbortSetup() bool {
	if m.host.AbortSetup == nil || m.clearPublic == [32]byte{} || m.session == nil || m.connecting || m.clearing {
		return false
	}
	s := m.snapshot
	return s.State == nil && s.Outcome == nil && (s.Stage >= game.StageSessionPrepared && s.Stage <= game.StageAwaitInitialDeposit ||
		s.Stage == game.StageInit && s.Choice != nil && (m.busy || m.shuffling))
}

func (m *Model) openAbortSetup() {
	if m.canAbortSetup() {
		m.modal, m.clearConfirm = abortSetupModal, false
	}
}

func (m *Model) abortSetup() tea.Cmd {
	if !m.canAbortSetup() {
		m.modal = noModal
		return nil
	}
	return m.removeSavedGame(true)
}

func (m *Model) canClearGame() bool {
	if m.host.ClearSavedGame == nil || m.clearPublic == [32]byte{} || m.connecting || m.clearing {
		return false
	}
	// Clear is recovery for a stopped game or a failed restore/clear, including
	// failures before a session exists. Healthy games use their normal actions.
	return m.session == nil || m.stopped
}

func (m *Model) openClearGame() {
	if m.canClearGame() {
		m.modal, m.clearConfirm = clearGameModal, false
	}
}

func (m *Model) clearGame() tea.Cmd {
	if !m.canClearGame() {
		return nil
	}
	return m.removeSavedGame(false)
}

func (m *Model) removeSavedGame(abort bool) tea.Cmd {
	ctx, clearSaved, public := m.ctx, m.host.ClearSavedGame, m.clearPublic
	if abort {
		clearSaved = m.host.AbortSetup
	}
	// Ignore pending driver, balance and action results from the session being
	// closed. The host joins its workers and releases storage before clearing.
	m.generation++
	m.clearing = true
	m.modal = noModal
	m.session = nil
	if !abort {
		m.snapshot = game.Snapshot{}
	}
	m.busy, m.stopped, m.shuffling = false, false, false
	m.selected = 0
	m.copyNotice, m.copyNoticeUntil = "", 0
	m.input.Reset()
	m.input.Blur()
	m.clearForm()
	m.errorText, m.status = "", "Clearing saved game..."
	if abort {
		m.status = "Aborting setup..."
	}
	return func() tea.Msg {
		session, err := clearSaved(ctx, public)
		return clearedGameMsg{session: session, err: err, abort: abort}
	}
}
