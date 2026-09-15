package ui

import (
	"arkade-poker/go/internal/client"
	tea "charm.land/bubbletea/v2"
)

type balanceMsg struct {
	update     client.BalanceUpdate
	closed     bool
	generation uint64
}

func (m *Model) waitBalance() tea.Cmd {
	if m.session == nil || m.session.Balances == nil {
		return nil
	}
	s, ctx := m.session, m.ctx
	generation := m.generation
	return func() tea.Msg {
		select {
		case update, ok := <-s.Balances:
			return balanceMsg{update: update, closed: !ok, generation: generation}
		case <-ctx.Done():
			return balanceMsg{closed: true, generation: generation}
		}
	}
}
