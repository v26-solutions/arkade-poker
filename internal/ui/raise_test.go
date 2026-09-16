package ui

import (
	"fmt"
	"strings"
	"testing"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestRaiseIncrementAndFundingPreview(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, role := range []covenant.Player{covenant.Player1, covenant.Player2} {
			for _, test := range []struct {
				name                            string
				mine, theirs, minimum, maximum  int64
				input, wantInput, target, added int64
			}{
				{"reported minimum", 20000, 20500, 21000, 100000, 0, 500, 21000, 1000},
				{"custom increase", 20000, 20500, 21000, 100000, 2000, 500, 22500, 2500},
				{"opening bet", 0, 0, 500, 100000, 0, 500, 500, 500},
				{"new street", 20000, 20000, 20500, 100000, 0, 500, 20500, 500},
				{"short all in", 99000, 99750, 100000, 100000, 0, 250, 100000, 1000},
			} {
				t.Run(fmt.Sprintf("browser=%t/player=%d/%s", browser, role, test.name), func(t *testing.T) {
					m := playing()
					m.host.Browser = browser
					m.snapshot.Role = role
					m.snapshot.State.Wagers = covenant.PerPlayer[uint64]{Player1: uint64(test.mine), Player2: uint64(test.theirs)}
					if role == covenant.Player2 {
						m.snapshot.State.Wagers.Player1, m.snapshot.State.Wagers.Player2 = uint64(test.theirs), uint64(test.mine)
					}
					m.snapshot.Choice.MinRaiseTo, m.snapshot.Choice.MaxRaiseTo = test.minimum, test.maximum
					m.snapshot.Choice.CallAmount = test.theirs - test.mine
					press(m, 'r', 0)
					if m.modal != raiseModal || m.fields[0].Value() != fmt.Sprint(test.wantInput) {
						t.Fatal("form did not default to the minimum raise increment")
					}
					increment := test.wantInput
					if test.input != 0 {
						increment = test.input
						m.fields[0].SetValue(fmt.Sprint(increment))
					}
					view := ansi.Strip(m.View().Content)
					for _, want := range []string{
						"RAISE BY",
						fmt.Sprintf("Minimum %s   Maximum %s", formatSats(test.wantInput), formatSats(test.maximum-test.theirs)),
						fmt.Sprintf("Call %s + raise %s = add %s sats", formatSats(test.theirs-test.mine), formatSats(increment), formatSats(test.added)),
						fmt.Sprintf("Your total bet after this move: %s sats", formatSats(test.target)),
					} {
						if !strings.Contains(view, want) {
							t.Fatalf("missing %q in form:\n%s", want, view)
						}
					}
					got := submitted(t, m, press(m, tea.KeyEnter, 0))
					if got.Kind != game.Bet || got.Bet.Kind != covenant.RaiseTo || got.Bet.Amount != test.target {
						t.Fatalf("wrong cumulative target: %+v", got)
					}
				})
			}
		}
	}
}

func TestRaiseIncrementRejectsInvalidAmounts(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "199", "1401", "9223372036854775807", "1.5"} {
		t.Run(value, func(t *testing.T) {
			m := playing() // Raise by 200..1400 above the opponent's 600.
			press(m, 'r', 0)
			m.fields[0].SetValue(value)
			if cmd := press(m, tea.KeyEnter, 0); cmd != nil || m.errorText == "" || m.modal != raiseModal || m.busy {
				t.Fatal("invalid raise submitted")
			}
			if strings.Contains(m.modalBody(), "Your total bet after this move:") {
				t.Fatal("invalid amount showed an executable funding preview")
			}
		})
	}
}

func TestRaiseArrowKeys(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, test := range []struct {
			name, input, want string
			key               rune
		}{
			{"increase by minimum bet", "200", "300", tea.KeyUp},
			{"decrease by minimum bet", "400", "300", tea.KeyDown},
			{"custom amount", "325", "425", tea.KeyUp},
			{"minimum", "200", "200", tea.KeyDown},
			{"maximum", "1400", "1400", tea.KeyUp},
			{"clamp decrease", "250", "200", tea.KeyDown},
			{"clamp increase", "1350", "1400", tea.KeyUp},
			{"below range", "1", "200", tea.KeyUp},
			{"above range", "2000", "1400", tea.KeyDown},
			{"empty", "", "200", tea.KeyUp},
			{"invalid", "1.5", "200", tea.KeyDown},
			{"overflow", "9223372036854775807", "200", tea.KeyUp},
		} {
			t.Run(fmt.Sprintf("browser=%t/%s", browser, test.name), func(t *testing.T) {
				m := playing() // Minimum bet is 100; legal raise is 200..1400.
				m.host.Browser = browser
				press(m, 'r', 0)
				m.fields[0].SetValue(test.input)
				m.errorText = "Raise amount must be within the displayed range."
				if cmd := press(m, test.key, 0); cmd != nil || m.busy || m.modal != raiseModal {
					t.Fatal("arrow key submitted or closed the form")
				}
				if got := m.fields[0].Value(); got != test.want {
					t.Fatalf("amount = %s, want %s", got, test.want)
				}
				if !m.fields[0].Focused() || m.fields[0].Position() != len(test.want) || m.errorText != "" {
					t.Fatal("adjustment lost input focus/cursor or retained stale error")
				}
				n, _ := amount(test.want)
				view := ansi.Strip(m.View().Content)
				if !strings.Contains(view, "Up/Down: Adjust by 100 sats") || !strings.Contains(view, "Call 200 + raise "+formatSats(n)) {
					t.Fatal("arrow hint or updated funding preview missing")
				}
				got := submitted(t, m, press(m, tea.KeyEnter, 0))
				if got.Kind != game.Bet || got.Bet.Kind != covenant.RaiseTo || got.Bet.Amount != 600+n {
					t.Fatalf("wrong cumulative target: %+v", got)
				}
			})
		}
	}
}

func TestRaiseArrowKeysShortAllIn(t *testing.T) {
	m := playing()
	m.snapshot.Choice.MinRaiseTo, m.snapshot.Choice.MaxRaiseTo = 650, 650
	press(m, 'r', 0)
	for _, key := range []rune{tea.KeyUp, tea.KeyDown} {
		press(m, key, 0)
		if m.fields[0].Value() != "50" {
			t.Fatal("arrow key changed the only legal short all-in amount")
		}
	}
}

func TestRaiseFormClosesWhenPositionChanges(t *testing.T) {
	m := playing()
	press(m, 'r', 0)
	m.fields[0].SetValue("600")
	// Routine driver observations temporarily withhold choices but do not
	// change the accepted position or the user's typed amount.
	next := m.snapshot
	next.Choice = nil
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
	if cmd := press(m, tea.KeyEnter, 0); cmd != nil || m.modal != raiseModal {
		t.Fatal("unavailable choice submitted or cleared the form")
	}
	press(m, tea.KeyUp, 0)
	press(m, tea.KeyDown, 0)
	if m.fields[0].Value() != "600" {
		t.Fatal("arrow keys changed the amount while choices were unavailable")
	}
	next.Choice = playing().snapshot.Choice
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
	if m.modal != raiseModal || m.fields[0].Value() != "600" {
		t.Fatal("repeated observation lost typed amount")
	}
	state := *next.State
	state.Wagers.Player1 = 800
	next.State = &state
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
	if m.modal != noModal || len(m.fields) != 0 {
		t.Fatal("stale raise amount retained for a different position")
	}
}

func TestRaisePreviewFitsMinimumGrid(t *testing.T) {
	m := playing()
	m.Update(tea.WindowSizeMsg{Width: 76, Height: 30})
	press(m, 'r', 0)
	m.errorText = strings.Repeat("service error ", 100)
	view := m.View().Content
	if lipgloss.Height(view) > 30 || lipgloss.Width(view) > 76 {
		t.Fatalf("raise preview overflows 76x30: %dx%d", lipgloss.Width(view), lipgloss.Height(view))
	}
}
