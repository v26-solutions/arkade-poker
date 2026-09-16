package ui

import (
	"errors"
	"log/slog"

	tea "charm.land/bubbletea/v2"
)

type logsCopiedMsg struct{ err error }

func (m *Model) logShortcut(msg tea.KeyPressMsg) bool {
	if !m.helpOpen && (m.modal == walletModal || m.modal == createModal || m.modal == joinModal || m.modal == raiseModal) {
		return false
	}
	if msg.Mod & ^(tea.ModShift|tea.ModCapsLock) != 0 {
		return false
	}
	if msg.Code != 'l' && msg.Code != 'L' {
		return false
	}
	return true
}

func (m *Model) copyLogs() tea.Cmd {
	if m.copyingLogs {
		return nil
	}
	m.copyingLogs = true
	copyText, logs := m.host.CopyText, m.host.Logs
	slog.Info("Copying diagnostic logs")
	// Snapshot at the keypress so concurrent work cannot change this export.
	text := ""
	if logs != nil {
		text = logs()
	}
	return func() tea.Msg {
		if copyText == nil || logs == nil {
			return logsCopiedMsg{errors.New("logs or clipboard unavailable")}
		}
		return logsCopiedMsg{copyText(text)}
	}
}
