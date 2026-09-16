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

func (m *Model) adjustRaise(direction int64) {
	r, ok := m.raiseRange()
	step := m.snapshot.Terms.MinBet
	if !ok || step <= 0 || len(m.fields) == 0 {
		return
	}
	n, err := amount(m.fields[0].Value())
	if err != nil {
		n = r.min
	} else {
		n = max(r.min, min(r.max, n+direction*step))
	}
	m.fields[0].SetValue(strconv.FormatInt(n, 10))
	m.fields[0].CursorEnd()
	m.errorText = ""
}
