package ui

import (
	"fmt"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
)

// The display follows the accepted state's absolute deadline. Clock ticks never
// authorize a claim; the driver still owns that choice and source observation.
func (m *Model) countdown() string {
	s := m.snapshot
	if m.session == nil || m.stopped || s.State == nil || s.Outcome != nil || s.State.Deadline == 0 ||
		(s.Role != covenant.Player1 && s.Role != covenant.Player2) || m.now.Unix() < 0 {
		return ""
	}
	actor, err := s.State.Phase.RequiredActor()
	if err != nil || actor == 0 {
		return ""
	}
	var remaining uint64
	if deadline, now := uint64(s.State.Deadline), uint64(m.now.Unix()); deadline > now {
		remaining = deadline - now
	}
	if remaining == 0 {
		if actor == s.Role {
			return "Your move: 00:00 | Opponent can claim timeout"
		}
		if allowed(s.Choice, game.ClaimTimeout) && !m.busy && !m.shuffling {
			return "Opponent's move: 00:00 | Claim timeout available"
		}
		return "Opponent's move: 00:00 | Time expired"
	}
	clock := fmt.Sprintf("%02d:%02d", remaining/60, remaining%60)
	if remaining >= 3600 {
		clock = fmt.Sprintf("%d:%02d:%02d", remaining/3600, remaining/60%60, remaining%60)
	}
	if actor == s.Role {
		return "Your move: " + clock + " remaining"
	}
	return "Opponent's move: " + clock + " until timeout"
}
