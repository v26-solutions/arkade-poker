package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/wallet"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestCountdownFollowsAbsoluteDeadline(t *testing.T) {
	for _, role := range []covenant.Player{covenant.Player1, covenant.Player2} {
		t.Run(fmt.Sprint(role), func(t *testing.T) {
			m := playing()
			m.snapshot.Role = role
			m.snapshot.State.Phase.Actor = covenant.Player1
			m.snapshot.State.Deadline = 1060
			for _, step := range []struct {
				at             time.Time
				local, waiting string
			}{
				{time.Unix(1000, 0), "Your move: 01:00 remaining", "Opponent's move: 01:00 until timeout"},
				{time.Unix(1001, 0), "Your move: 00:59 remaining", "Opponent's move: 00:59 until timeout"},
				// A delayed tick catches up immediately; fractional seconds must not expire early.
				{time.Unix(1059, 999_000_000), "Your move: 00:01 remaining", "Opponent's move: 00:01 until timeout"},
				{time.Unix(1060, 0), "Your move: 00:00 | Opponent can claim timeout", "Opponent's move: 00:00 | Time expired"},
				{time.Unix(1080, 0), "Your move: 00:00 | Opponent can claim timeout", "Opponent's move: 00:00 | Time expired"},
			} {
				_, cmd := m.Update(tickMsg(step.at))
				want := step.local
				if role == covenant.Player2 {
					want = step.waiting
				}
				if cmd == nil || !strings.Contains(ansi.Strip(m.View().Content), want) {
					t.Fatalf("at %s: want %q, got %q", step.at, want, m.countdown())
				}
			}
			// A new accepted move changes the actor and adds to the old deadline.
			next := m.snapshot
			state := *next.State
			state.Phase.Actor = covenant.Player2
			state.Deadline += covenant.DeadlineInterval
			next.State = &state
			m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
			want := "Opponent's move: 00:40 until timeout"
			if role == covenant.Player2 {
				want = "Your move: 00:40 remaining"
			}
			if m.countdown() != want {
				t.Fatalf("new move reset the deadline: %q", m.countdown())
			}
			m.snapshot.State.Deadline = 4880
			if !strings.Contains(m.countdown(), "1:03:20") {
				t.Fatal("long countdown lost hours", m.countdown())
			}
		})
	}
}

func TestCountdownClaimRequiresDriverChoice(t *testing.T) {
	for _, input := range []string{"keyboard", "enter", "mouse"} {
		t.Run(input, func(t *testing.T) {
			m := playing()
			key, err := wallet.ParseKey(fmt.Sprintf("%064x", 1), "")
			if err != nil {
				t.Fatal(err)
			}
			defer key.Destroy()
			m.key = key
			m.snapshot.Stage = game.StageBettingOpponent
			m.snapshot.State.Phase.Actor = covenant.Player1
			m.snapshot.State.Deadline = 1001
			m.snapshot.Choice = nil
			m.Update(tickMsg(time.Unix(1000, 0)))
			m.Update(tickMsg(time.Unix(1001, 0)))
			if !strings.Contains(m.countdown(), "Time expired") || len(m.actions()) != 0 || press(m, 'd', 0) != nil {
				t.Fatal("clock tick authorized a claim")
			}
			next := m.snapshot
			next.Choice = &game.Choice{Allowed: []game.InputKind{game.ClaimTimeout}}
			m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
			view := ansi.Strip(m.View().Content)
			if !strings.Contains(view, "Claim timeout available") || !strings.Contains(view, "[D] CLAIM TIMEOUT") {
				t.Fatal("driver-authorized timeout is not visible")
			}
			var cmd tea.Cmd
			switch input {
			case "keyboard":
				cmd = press(m, 'd', 0)
			case "enter":
				cmd = press(m, tea.KeyEnter, 0)
			case "mouse":
				for y, line := range strings.Split(view, "\n") {
					if x := strings.Index(line, "[D] CLAIM TIMEOUT"); x >= 0 {
						_, cmd = m.Update(tea.MouseClickMsg{X: lipgloss.Width(line[:x]) + 2, Y: y, Button: tea.MouseLeft})
						break
					}
				}
			}
			if got := submitted(t, m, cmd); got.Kind != game.ClaimTimeout {
				t.Fatal("wrong timeout input", got)
			}
			m.Update(tickMsg(time.Unix(1002, 0)))
			m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
			if len(m.actions()) != 0 || strings.Contains(m.countdown(), "available") {
				t.Fatal("tick or stale observation unlocked duplicate claim")
			}
		})
	}
}

func TestCountdownVisibilityAndLayout(t *testing.T) {
	for _, form := range []modal{noModal, raiseModal, exitModal} {
		t.Run(fmt.Sprint(form), func(t *testing.T) {
			m := playing()
			m.snapshot.State.Deadline = 1060
			m.Update(tickMsg(time.Unix(1000, 0)))
			m.Update(tea.WindowSizeMsg{Width: 76, Height: 30})
			m.errorText = strings.Repeat("long service error ", 100)
			if form == raiseModal {
				m.openForm(form, "800")
			} else {
				m.modal = form
			}
			view := m.View().Content
			if !strings.Contains(ansi.Strip(view), "Your move: 01:00 remaining") {
				t.Fatal("form hid countdown")
			}
			if lipgloss.Width(view) > 76 || lipgloss.Height(view) > 30 {
				t.Fatalf("countdown overflowed 76x30: %dx%d", lipgloss.Width(view), lipgloss.Height(view))
			}
		})
	}
	for _, test := range []struct {
		name  string
		setup func(*Model)
	}{
		{"setup", func(m *Model) { m.snapshot.State = nil }},
		{"no deadline", func(m *Model) { m.snapshot.State.Deadline = 0 }},
		{"evaluation", func(m *Model) { m.snapshot.State.Phase = covenant.Phase{Kind: covenant.ShowdownEvaluation} }},
		{"complete", func(m *Model) { m.snapshot.Outcome = &game.Outcome{Kind: game.Won} }},
		{"stopped", func(m *Model) { m.stopped = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := playing()
			m.snapshot.State.Deadline = 1060
			m.Update(tickMsg(time.Unix(1000, 0)))
			test.setup(m)
			if m.countdown() != "" {
				t.Fatal("countdown shown without an active timed obligation", m.countdown())
			}
		})
	}
	// Funding/opening phases encode their actor in the phase kind, not Actor.
	m := playing()
	m.snapshot.State.Deadline = 1060
	m.snapshot.State.Phase = covenant.Phase{Kind: covenant.AwaitPlayer1FundingAndReveal}
	m.Update(tickMsg(time.Unix(1000, 0)))
	if m.countdown() != "Opponent's move: 01:00 until timeout" {
		t.Fatal("funding obligation has wrong actor", m.countdown())
	}
	m.snapshot.State.Phase = covenant.Phase{Kind: covenant.AwaitPlayer2RevealAndOpening}
	if m.countdown() != "Your move: 01:00 remaining" {
		t.Fatal("opening obligation has wrong actor", m.countdown())
	}
}
