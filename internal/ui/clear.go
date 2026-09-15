package ui

import (
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/wallet"
	tea "charm.land/bubbletea/v2"
)

type clearedGameMsg struct{ err error }

func (m *Model) canClearGame() bool {
	return m.host.ClearSavedGame != nil && m.clearPublic != [32]byte{} && !m.connecting && !m.clearing
}

func (m *Model) openClearGame() {
	m.modal, m.clearConfirm = clearGameModal, false
}

func (m *Model) clearGame() tea.Cmd {
	if !m.canClearGame() {
		return nil
	}
	ctx, clearSaved, public := m.ctx, m.host.ClearSavedGame, m.clearPublic
	key, session := m.key, m.session
	// Ignore pending driver, balance and action results from the session being
	// closed. The host joins its workers and releases storage before clearing.
	m.generation++
	m.clearing = true
	m.modal = noModal
	m.key, m.session = nil, nil
	m.receive = wallet.Receive{}
	m.snapshot = game.Snapshot{}
	m.balance, m.balanceKnown, m.balanceFailed = 0, false, false
	m.busy, m.stopped, m.shuffling = false, false, false
	m.selected = 0
	m.copyNotice, m.copyNoticeUntil = "", 0
	m.input.Reset()
	m.input.Blur()
	m.clearForm()
	m.errorText, m.status = "", "Clearing saved game..."
	return func() tea.Msg {
		if session == nil {
			defer key.Destroy()
		}
		return clearedGameMsg{err: clearSaved(ctx, public)}
	}
}
