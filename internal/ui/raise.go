package ui

import (
	"strconv"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	tea "charm.land/bubbletea/v2"
)

type raiseRange struct {
	mine, theirs int64
	min, max     int64
}

// The form accepts an increase over the opponent's wager. The game still
// receives a cumulative target, including wagers from previous streets.
func (m *Model) raiseRange() (raiseRange, bool) {
	s, c := m.snapshot, m.snapshot.Choice
	if s.State == nil || !allowed(c, game.Bet) || c.MinRaiseTo <= 0 || c.MaxRaiseTo < c.MinRaiseTo {
		return raiseRange{}, false
	}
	mine, theirs := s.State.Wagers.Player1, s.State.Wagers.Player2
	switch s.Role {
	case covenant.Player1:
	case covenant.Player2:
		mine, theirs = theirs, mine
	default:
		return raiseRange{}, false
	}
	if mine > theirs || theirs >= uint64(c.MinRaiseTo) {
		return raiseRange{}, false
	}
	return raiseRange{int64(mine), int64(theirs), c.MinRaiseTo - int64(theirs), c.MaxRaiseTo - int64(theirs)}, true
}

func (m *Model) openRaise() tea.Cmd {
	r, ok := m.raiseRange()
	if !ok {
		return nil
	}
	return m.openForm(raiseModal, strconv.FormatInt(r.min, 10))
}
