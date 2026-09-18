package ui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func hitFor(m *Model, id string) (hitRegion, bool) {
	for _, hit := range m.layout().hits {
		if hit.id == id {
			return hit, true
		}
	}
	return hitRegion{}, false
}

func clickAt(m *Model, hit hitRegion) tea.Cmd {
	_, cmd := m.Update(tea.MouseClickMsg{X: hit.rect.Min.X, Y: hit.rect.Min.Y, Button: tea.MouseLeft})
	return cmd
}

func setBalance(m *Model, sats int64) {
	m.Update(balanceMsg{generation: m.generation, update: client.BalanceUpdate{Sats: sats}})
}

func TestFundingActionsRequireBalance(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, role := range []covenant.Player{covenant.Player1, covenant.Player2} {
			for _, test := range []struct {
				key      rune
				required int64
			}{{'c', 200}, {'r', 400}, {'a', 1600}} {
				t.Run(fmt.Sprintf("browser=%t/player=%d/%c", browser, role, test.key), func(t *testing.T) {
					m := playing()
					m.host.Browser = browser
					m.snapshot.Role = role
					if role == covenant.Player1 {
						m.snapshot.State.Wagers.Player1, m.snapshot.State.Wagers.Player2 = 400, 600
					}
					id := "action:" + string(test.key)
					hit, ok := hitFor(m, id)
					if !ok {
						t.Fatal("funded action missing")
					}
					for _, balance := range []struct{ sats, shortfall int64 }{{0, 200}, {test.required - 1, 1}} {
						setBalance(m, balance.sats)
						if _, ok := hitFor(m, id); ok {
							t.Fatal("unaffordable action has a mouse target")
						}
						selected := m.selected
						if clickAt(m, hit) != nil || press(m, test.key, 0) != nil || m.selected != selected || m.busy || m.modal != noModal {
							t.Fatal("disabled action activated")
						}
						view := ansi.Strip(m.View().Content)
						if !strings.Contains(view, "["+strings.ToUpper(string(test.key))+"]") || !strings.Contains(view, "More funds required: add "+formatSats(balance.shortfall)+" sats") {
							t.Fatalf("action or shortfall missing:\n%s", view)
						}
					}
					setBalance(m, test.required)
					if _, ok := hitFor(m, id); !ok {
						t.Fatal("exact balance did not enable action")
					}
					cmd := press(m, test.key, 0)
					if test.key != 'c' {
						if m.busy || m.modal == noModal {
							t.Fatal("funded raise/all-in skipped review")
						}
						cmd = press(m, tea.KeyEnter, 0)
					}
					input := submitted(t, m, cmd)
					if input.Kind != game.Bet || m.inputFunding(input) != test.required {
						t.Fatal("wrong wallet contribution submitted", input)
					}
				})
			}
		}
	}
}

func TestActionNavigationSkipsUnaffordableWagers(t *testing.T) {
	for _, test := range []struct {
		balance int64
		keys    string
	}{
		{0, "f"}, {199, "f"}, {200, "fc"}, {399, "fc"},
		{400, "fcr"}, {1599, "fcr"}, {1600, "fcra"},
	} {
		for _, nav := range []struct {
			code rune
			mod  tea.KeyMod
			step int
		}{
			{tea.KeyLeft, 0, -1}, {tea.KeyUp, 0, -1}, {tea.KeyTab, tea.ModShift, -1},
			{tea.KeyRight, 0, 1}, {tea.KeyDown, 0, 1}, {tea.KeyTab, 0, 1},
		} {
			t.Run(fmt.Sprintf("balance=%d/key=%d/mod=%d", test.balance, nav.code, nav.mod), func(t *testing.T) {
				m := playing()
				setBalance(m, test.balance)
				status := m.balanceStatus()
				if test.balance == 199 && status != "More funds required: add 1 sats" {
					t.Fatal("status did not use the call shortfall", status)
				}
				position := 0
				for range 8 {
					position = (position + nav.step + len(test.keys)) % len(test.keys)
					if cmd := press(m, nav.code, nav.mod); cmd != nil || m.modal != noModal || m.busy {
						t.Fatal("navigation activated an action")
					}
					if m.selected < 0 || m.actions()[m.selected].key != string(test.keys[position]) {
						t.Fatalf("selected %d, want action %c", m.selected, test.keys[position])
					}
					if got := m.balanceStatus(); got != status {
						t.Fatalf("navigation changed shortfall: %q -> %q", status, got)
					}
				}
			})
		}
	}
	// Enter activates the funded action reached after skipping disabled wagers.
	m := playing()
	setBalance(m, 200)
	press(m, tea.KeyLeft, 0)
	if input := submitted(t, m, press(m, tea.KeyEnter, 0)); input.Bet.Kind != covenant.Call {
		t.Fatal("Enter did not activate the selected call", input)
	}
}

func TestActionSelectionTracksBalanceAndChoices(t *testing.T) {
	m := playing()
	press(m, tea.KeyLeft, 0) // All in is initially affordable.
	if m.actions()[m.selected].key != "a" {
		t.Fatal("funded All in was not selectable")
	}
	setBalance(m, 199)
	if m.actions()[m.selected].key != "f" {
		t.Fatal("balance drop left a disabled wager selected")
	}
	setBalance(m, 1600)
	press(m, tea.KeyLeft, 0)
	if m.actions()[m.selected].key != "a" {
		t.Fatal("funding did not restore All in navigation")
	}
	m.Update(balanceMsg{closed: true})
	if m.actions()[m.selected].key != "f" {
		t.Fatal("balance failure left a disabled wager selected")
	}

	// No enabled action must leave Enter inert and navigation bounded.
	next := m.snapshot
	next.Choice = &game.Choice{Allowed: []game.InputKind{game.Bet}, CanCall: true, CallAmount: 200}
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
	for _, key := range []rune{tea.KeyLeft, tea.KeyRight, tea.KeyTab, tea.KeyEnter} {
		if cmd := press(m, key, 0); cmd != nil || m.selected != -1 || m.busy {
			t.Fatal("all-disabled actions retained a selection or activated")
		}
	}
	setBalance(m, 200)
	if m.selected != 0 {
		t.Fatal("balance recovery did not restore an available selection")
	}
	if input := submitted(t, m, press(m, tea.KeyEnter, 0)); input.Bet.Kind != covenant.Call {
		t.Fatal("recovered selection did not submit the call", input)
	}

	// A new turn starts at the first available action after an opponent wait.
	m = playing()
	next = m.snapshot
	waiting := next
	waiting.Stage, waiting.Choice = game.StageBettingOpponent, nil
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: waiting}})
	if m.selected != -1 {
		t.Fatal("opponent wait retained an action selection")
	}
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
	if m.selected != 0 {
		t.Fatal("new turn did not select the first available action")
	}
}

func TestBalanceUnavailableBlocksFundingAndRecovers(t *testing.T) {
	for _, state := range []string{"loading", "failed", "closed"} {
		t.Run(state, func(t *testing.T) {
			m := playing()
			switch state {
			case "loading":
				m.balanceKnown = false
			case "failed":
				m.Update(balanceMsg{update: client.BalanceUpdate{Err: errors.New("offline")}})
			case "closed":
				m.Update(balanceMsg{closed: true})
			}
			for _, key := range []rune{'c', 'r', 'a'} {
				if _, ok := hitFor(m, "action:"+string(key)); ok || press(m, key, 0) != nil || m.modal != noModal || m.busy {
					t.Fatal("unknown balance allowed funding")
				}
			}
			want := "Balance unavailable"
			if state == "loading" {
				want = "Checking wallet balance"
			}
			if !strings.Contains(ansi.Strip(m.View().Content), want) {
				t.Fatal("missing balance status")
			}
			setBalance(m, 1600)
			if _, ok := hitFor(m, "action:a"); !ok || m.balanceStatus() != "" {
				t.Fatal("balance recovery did not enable funding")
			}
		})
	}
}

func TestFreeActionsDoNotRequireBalance(t *testing.T) {
	for _, test := range []struct {
		name   string
		choice game.Choice
		key    rune
		kind   game.InputKind
	}{
		{"check", game.Choice{Allowed: []game.InputKind{game.Bet}, CanCheck: true}, 'c', game.Bet},
		{"fold", game.Choice{Allowed: []game.InputKind{game.Concede}}, 'f', game.Concede},
		{"reveal", game.Choice{Allowed: []game.InputKind{game.RevealShowdown}}, 's', game.RevealShowdown},
		{"timeout", game.Choice{Allowed: []game.InputKind{game.ClaimTimeout}}, 'd', game.ClaimTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := playing()
			m.balance, m.balanceKnown, m.balanceFailed = 0, false, true
			m.snapshot.Choice = &test.choice
			if input := submitted(t, m, press(m, test.key, 0)); input.Kind != test.kind || m.inputFunding(input) != 0 {
				t.Fatal("free action unavailable or changed", input)
			}
		})
	}
}

func TestOpenBetConfirmationsTrackBalanceAndAmount(t *testing.T) {
	for _, key := range []rune{'r', 'a'} {
		t.Run(string(key), func(t *testing.T) {
			m := playing()
			press(m, key, 0)
			required := int64(1600)
			if key == 'r' {
				m.fields[0].SetValue("600") // Call 200 + raise 600.
				required = 800
			}
			hit, ok := hitFor(m, "confirm")
			if !ok {
				t.Fatal("confirmation missing")
			}
			setBalance(m, required-1)
			if _, ok := hitFor(m, "confirm"); ok || clickAt(m, hit) != nil || press(m, tea.KeyEnter, 0) != nil || m.busy {
				t.Fatal("confirmation used a stale balance")
			}
			if !strings.Contains(ansi.Strip(m.View().Content), "More funds required: add 1 sats") {
				t.Fatal("confirmation shortfall missing")
			}
			if key == 'r' {
				m.fields[0].SetValue("200")
				if _, ok := hitFor(m, "confirm"); !ok {
					t.Fatal("lowering the raise did not re-enable confirmation")
				}
				m.fields[0].SetValue("600")
			}
			m.Update(balanceMsg{update: client.BalanceUpdate{Err: errors.New("offline")}})
			if press(m, tea.KeyEnter, 0) != nil || m.busy {
				t.Fatal("balance failure did not block confirmation")
			}
			setBalance(m, required)
			if input := submitted(t, m, clickAt(m, hit)); m.inputFunding(input) != required {
				t.Fatal("confirmation used the wrong contribution")
			}
		})
	}
}

func setupBalanceModel(t *testing.T, inv game.Invitation, join bool, balance int64) *Model {
	t.Helper()
	m := playing()
	m.snapshot = game.Snapshot{Stage: game.StageInit, Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession, game.JoinSession}}}
	m.host.DefaultTerms = inv.Terms
	setBalance(m, balance)
	if !join {
		press(m, 'c', 0)
		return m
	}
	token, err := game.EncodeInvitation(inv)
	if err != nil {
		t.Fatal(err)
	}
	press(m, 'j', 0)
	m.Update(tea.PasteMsg{Content: token})
	press(m, tea.KeyEnter, 0)
	if m.modal != joinConfirmModal {
		t.Fatal("join did not allow invitation review")
	}
	return m
}

func TestSetupDepositGateAndAllInWarning(t *testing.T) {
	inv := invitationWithTerms(t, "wss://relay.example", game.Terms{Stake: 5000, Bond: 5000, MinBet: 500, MaxWager: 100000})
	for _, browser := range []bool{false, true} {
		for _, join := range []bool{false, true} {
			for _, balance := range []int64{0, 9999, 10000, 109999, 110000} {
				t.Run(fmt.Sprintf("browser=%t/join=%t/balance=%d", browser, join, balance), func(t *testing.T) {
					m := setupBalanceModel(t, inv, join, balance)
					m.host.Browser = browser
					m.width, m.height = 76, 30
					initialModal := m.modal
					_, clickable := hitFor(m, "confirm")
					if clickable != (balance >= 10000) {
						t.Fatal("deposit confirmation availability incorrect")
					}
					cmd := press(m, tea.KeyEnter, 0)
					if balance < 10000 {
						if cmd != nil || m.modal != initialModal || m.busy || !strings.Contains(ansi.Strip(m.View().Content), "More funds required") {
							t.Fatal("insufficient deposit was not blocked")
						}
						return
					}
					if balance < 110000 {
						if cmd != nil || m.modal != setupWarningModal || m.busy {
							t.Fatal("setup skipped the all-in warning")
						}
						view := ansi.Strip(m.View().Content)
						for _, want := range []string{"ALL-IN BALANCE WARNING", "Wallet balance:", "Deposit:        10,000 sats", "Maximum wager:  100,000 sats", "Total needed:   110,000 sats", "Shortfall:      " + formatSats(110000-balance) + " sats", "add funds or fold", "Proceed with this session?"} {
							if !strings.Contains(view, want) {
								t.Fatalf("warning missing %q:\n%s", want, view)
							}
						}
						if lipgloss.Width(view) != 76 || lipgloss.Height(view) != 30 {
							t.Fatal("warning overflowed minimum grid")
						}
						cmd = press(m, tea.KeyEnter, 0)
					}
					input := submitted(t, m, cmd)
					terms, ok := setupTerms(input)
					if !ok || terms != inv.Terms || (join && !reflect.DeepEqual(input.Invitation, &inv)) {
						t.Fatal("setup changed agreed terms/invitation", input)
					}
					if m.pendingSetup != nil || m.joinInvitation != nil || len(m.fields) != 0 {
						t.Fatal("submission retained confirmation data")
					}
				})
			}
		}
	}
}

func TestSetupWarningBackAndLiveBalance(t *testing.T) {
	inv := invitation(t, "wss://relay.example") // Deposit 2,000; total 3,000.
	for _, join := range []bool{false, true} {
		t.Run(fmt.Sprint(join), func(t *testing.T) {
			m := setupBalanceModel(t, inv, join, 2000)
			initialModal := m.modal
			press(m, tea.KeyEnter, 0)
			press(m, 'c', tea.ModCtrl)
			press(m, tea.KeyEscape, 0)
			if m.modal != setupWarningModal || m.pendingSetup == nil {
				t.Fatal("exit cancellation lost warning")
			}
			press(m, tea.KeyEscape, 0)
			if m.modal != initialModal || m.pendingSetup != nil {
				t.Fatal("back did not return to setup review")
			}
			if join {
				if !reflect.DeepEqual(m.joinInvitation, &inv) {
					t.Fatal("back changed invitation")
				}
			} else {
				input, errText := m.createInput()
				if errText != "" || input.Terms != inv.Terms {
					t.Fatal("back lost form values")
				}
				m.fields[3].SetValue("2000")
			}
			press(m, tea.KeyEnter, 0)
			if m.modal != setupWarningModal {
				t.Fatal("back incorrectly acknowledged the warning")
			}
			hit, ok := hitFor(m, "confirm")
			if !ok {
				t.Fatal("warning confirm missing")
			}
			setBalance(m, 1999)
			if _, ok := hitFor(m, "confirm"); ok || clickAt(m, hit) != nil || press(m, tea.KeyEnter, 0) != nil || m.busy {
				t.Fatal("warning allowed an unaffordable deposit")
			}
			m.Update(balanceMsg{update: client.BalanceUpdate{Err: errors.New("offline")}})
			if press(m, tea.KeyEnter, 0) != nil || !strings.Contains(ansi.Strip(m.View().Content), "Balance unavailable") {
				t.Fatal("warning did not handle unavailable balance")
			}
			setBalance(m, 4000)
			if !strings.Contains(ansi.Strip(m.View().Content), "now covers") {
				t.Fatal("warning retained stale shortfall after top-up")
			}
			input := submitted(t, m, clickAt(m, hit))
			terms, _ := setupTerms(input)
			if !join && terms.MaxWager != 2000 {
				t.Fatal("confirmation used terms from before Back")
			}
		})
	}
}

func TestSetupFormsRecoverFromUnknownBalance(t *testing.T) {
	inv := invitation(t, "wss://relay.example")
	for _, join := range []bool{false, true} {
		m := setupBalanceModel(t, inv, join, 3000)
		m.balanceKnown = false
		hit := hitRegion{}
		if _, ok := hitFor(m, "confirm"); ok || press(m, tea.KeyEnter, 0) != nil || m.busy {
			t.Fatal("setup confirmation allowed unknown balance")
		}
		setBalance(m, 3000)
		var ok bool
		if hit, ok = hitFor(m, "confirm"); !ok {
			t.Fatal("top-up did not enable setup confirmation")
		}
		submitted(t, m, clickAt(m, hit))
	}
}
