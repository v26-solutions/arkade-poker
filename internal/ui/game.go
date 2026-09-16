package ui

import (
	"errors"
	"reflect"
	"strconv"
	"strings"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type driverMsg struct {
	update     game.Update
	closed     bool
	generation uint64
}
type sentMsg struct {
	err        error
	generation uint64
}
type action struct {
	key, label string
	input      game.Input
}

func (m *Model) waitUpdate() tea.Cmd {
	if m.session == nil {
		return nil
	}
	s, ctx := m.session, m.ctx
	generation := m.generation
	return func() tea.Msg {
		select {
		case u, ok := <-s.Updates:
			return driverMsg{update: u, closed: !ok, generation: generation}
		case <-ctx.Done():
			return driverMsg{closed: true, generation: generation}
		}
	}
}

func (m *Model) driverUpdate(msg driverMsg) tea.Cmd {
	if msg.generation != m.generation {
		return nil
	}
	if msg.closed {
		m.shuffling = false
		m.stopped = true
		return nil
	}
	u := msg.update
	// A final host error carries no snapshot; keep the last known table visible.
	if u.Err == nil || u.Snapshot.Stage != game.StageInit || u.Snapshot.Choice != nil {
		prior, next := m.snapshot, u.Snapshot
		prior.Choice, next.Choice = nil, nil
		if !reflect.DeepEqual(prior, next) {
			m.busy = false
			// A raise increment is relative to this position. Close a stale form
			// before its input can be applied to a different opponent wager.
			if m.modal == raiseModal || m.modal == allInModal {
				m.modal = noModal
				m.clearForm()
			}
			if m.modal == exitModal && (m.previousModal == raiseModal || m.previousModal == allInModal) {
				m.previousModal = noModal
				m.clearForm()
			}
		}
		m.snapshot = u.Snapshot
	}
	m.shuffling = u.Shuffling
	if u.Err != nil {
		m.errorText = u.Err.Error()
		m.busy = false
		if !errors.Is(u.Err, game.ErrInput) && !errors.Is(u.Err, game.ErrAmount) {
			m.stopped = true
		}
	}
	m.status = m.gameStatus()
	if m.stopped {
		m.status = "Game stopped. Saved work retained; restart to reconcile."
	}
	if m.selected >= len(m.actions()) {
		m.selected = 0
	}
	return m.waitUpdate()
}

func allowed(c *game.Choice, kind game.InputKind) bool {
	if c != nil {
		for _, k := range c.Allowed {
			if k == kind {
				return true
			}
		}
	}
	return false
}

func (m *Model) actions() []action {
	var setupActions []action
	if m.canAbortSetup() {
		setupActions = append(setupActions, action{key: "b", label: "Abort setup"})
	}
	if m.session == nil || m.busy || m.stopped || m.shuffling {
		return setupActions
	}
	s, c := m.snapshot, m.snapshot.Choice
	if s.Outcome != nil {
		a := []action{{key: "n", label: "New game"}}
		if s.Outcome.Transaction != nil {
			a = append(a, action{key: "p", label: "Payout details"})
		}
		return a
	}
	a := []action{}
	if allowed(c, game.StartSession) {
		a = append(a, action{key: "c", label: "Create session"})
	}
	if allowed(c, game.JoinSession) {
		a = append(a, action{key: "j", label: "Join session"})
	}
	if s.Invitation != nil && s.State == nil {
		a = append(a, action{key: "t", label: "Invitation"})
	}
	if allowed(c, game.Concede) {
		key, label := "f", "Fold"
		if allowed(c, game.RevealShowdown) {
			key, label = "m", "Muck"
		}
		a = append(a, action{key, label, game.Input{Kind: game.Concede}})
	}
	if allowed(c, game.Bet) {
		if c.CanCheck {
			a = append(a, action{"c", "Check", game.Input{Kind: game.Bet, Bet: covenant.BettingAction{Kind: covenant.Check}}})
		}
		if c.CanCall {
			a = append(a, action{"c", "Call " + formatSats(c.CallAmount), game.Input{Kind: game.Bet, Bet: covenant.BettingAction{Kind: covenant.Call}}})
		}
		if c.MinRaiseTo > 0 && c.MaxRaiseTo >= c.MinRaiseTo {
			a = append(a, action{key: "r", label: "Raise"}, action{"a", "All in", game.Input{Kind: game.Bet, Bet: covenant.BettingAction{Kind: covenant.RaiseTo, Amount: c.MaxRaiseTo}}})
		}
	}
	if allowed(c, game.RevealShowdown) {
		a = append(a, action{"s", "Reveal hand", game.Input{Kind: game.RevealShowdown}})
	}
	if allowed(c, game.ClaimTimeout) {
		a = append(a, action{"d", "Claim timeout", game.Input{Kind: game.ClaimTimeout}})
	}
	return append(a, setupActions...)
}

func (m *Model) submit(input game.Input, fresh bool) tea.Cmd {
	if m.session == nil || m.busy || m.stopped {
		return nil
	}
	if !fresh && (input.Kind == game.StartSession || input.Kind == game.JoinSession) && m.session.ValidateSetup != nil {
		if err := m.session.ValidateSetup(input); err != nil {
			m.errorText = "The invitation, relay or terms do not match this wallet's service configuration."
			return nil
		}
	}
	m.busy, m.errorText = true, ""
	m.modal = noModal
	m.clearForm()
	s, ctx := m.session, m.ctx
	generation := m.generation
	return func() tea.Msg {
		if fresh {
			select {
			case s.NewGame <- struct{}{}:
				return sentMsg{generation: generation}
			case <-s.Done():
				return sentMsg{err: game.ErrDriverStopped, generation: generation}
			case <-ctx.Done():
				return sentMsg{err: ctx.Err(), generation: generation}
			}
		}
		select {
		case s.Inputs <- input:
			return sentMsg{generation: generation}
		case <-s.Done():
			return sentMsg{err: game.ErrDriverStopped, generation: generation}
		case <-ctx.Done():
			return sentMsg{err: ctx.Err(), generation: generation}
		}
	}
}

func (m *Model) clearForm() {
	for i := range m.fields {
		m.fields[i].Reset()
		m.fields[i].Blur()
	}
	m.fields, m.joinInvitation = nil, nil
	m.field = 0
}
func (m *Model) openForm(kind modal, values ...string) tea.Cmd {
	m.clearForm()
	m.modal, m.errorText = kind, ""
	for _, value := range values {
		i := textinput.New()
		i.CharLimit = 4096
		if kind == joinModal {
			i.CharLimit = 16384
		} // Above codec limit: overlong paste stays invalid.
		i.SetWidth(max(10, min(46, m.width-26)))
		i.SetValue(value)
		m.fields = append(m.fields, i)
	}
	if len(m.fields) != 0 {
		return m.fields[0].Focus()
	}
	return nil
}

func (m *Model) gameKey(key string) (tea.Cmd, bool) {
	if m.modal == menuModal {
		switch key {
		case "enter", "r":
			m.modal = noModal
		case "x":
			if m.canClearGame() {
				m.openClearGame()
			}
		case "b":
			m.openAbortSetup()
		case "q":
			if !m.host.Browser {
				m.previousModal, m.modal, m.exitConfirm = menuModal, exitModal, false
			}
		}
		return nil, true
	}
	if m.modal == payoutModal {
		return nil, true
	}
	if m.modal == allInModal {
		if key == "enter" {
			for _, a := range m.actions() {
				if a.key == "a" {
					return m.submit(a.input, false), true
				}
			}
			m.errorText = "All in is no longer available."
		}
		return nil, true
	}
	if m.modal == createModal || m.modal == joinModal || m.modal == raiseModal {
		if key == "tab" || key == "shift+tab" || key == "up" || key == "down" {
			step := 1
			if key == "shift+tab" || key == "up" {
				step = -1
			}
			m.fields[m.field].Blur()
			m.field = (m.field + step + len(m.fields)) % len(m.fields)
			return m.fields[m.field].Focus(), true
		}
		if key == "enter" {
			return m.submitForm(), true
		}
		return nil, false
	}
	if m.modal == joinConfirmModal {
		if key == "enter" && m.joinInvitation != nil {
			inv := *m.joinInvitation
			return m.submit(game.Input{Kind: game.JoinSession, Invitation: &inv}, false), true
		}
		return nil, true
	}
	if m.modal == tokenModal {
		if key == "y" || key == "enter" {
			inv := m.snapshot.Invitation
			if inv == nil {
				return nil, true
			}
			token, err := game.EncodeInvitation(*inv)
			if err != nil {
				m.errorText = "Invitation unavailable"
				return nil, true
			}
			copyText := m.host.CopyText
			m.copyNotice = "Invitation copied"
			return func() tea.Msg {
				if copyText == nil {
					return copiedMsg{errors.New("clipboard unavailable")}
				}
				return copiedMsg{copyText(token)}
			}, true
		}
		return nil, true
	}
	if m.modal != noModal {
		return nil, false
	}
	a := m.actions()
	if len(a) == 0 {
		return nil, false
	}
	if m.selected >= len(a) {
		m.selected = 0
	}
	switch key {
	case "left", "up", "shift+tab":
		m.selected = (m.selected + len(a) - 1) % len(a)
		return nil, true
	case "right", "down", "tab":
		m.selected = (m.selected + 1) % len(a)
		return nil, true
	case "enter":
		key = a[m.selected].key
	}
	for i, action := range a {
		if key != action.key {
			continue
		}
		m.selected = i
		if key == "a" {
			m.modal, m.errorText = allInModal, ""
			return nil, true
		}
		if action.input.Kind != 0 {
			return m.submit(action.input, false), true
		}
		switch key {
		case "b":
			m.openAbortSetup()
			return nil, true
		case "c":
			t := m.host.DefaultTerms
			return m.openForm(createModal, strconv.FormatInt(t.Stake, 10), strconv.FormatInt(t.Bond, 10),
				strconv.FormatInt(t.MinBet, 10), strconv.FormatInt(t.MaxWager, 10), m.host.RelayURL), true
		case "j":
			return m.openForm(joinModal, ""), true
		case "t":
			return m.openForm(tokenModal), true
		case "r":
			return m.openRaise(), true
		case "p":
			m.modal = payoutModal
			return nil, true
		case "n":
			return m.submit(game.Input{}, true), true
		}
	}
	return nil, false
}

func amount(text string) (int64, error) {
	if text == "" {
		return 0, game.ErrAmount
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, game.ErrAmount
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n <= 0 || n > 21_000_000*100_000_000 {
		return 0, game.ErrAmount
	}
	return n, nil
}
func (m *Model) submitForm() tea.Cmd {
	switch m.modal {
	case createModal:
		var v [4]int64
		for i := range v {
			n, err := amount(m.fields[i].Value())
			if err != nil {
				m.errorText = "Enter positive whole satoshi amounts."
				return nil
			}
			v[i] = n
		}
		terms := game.Terms{Stake: v[0], Bond: v[1], MinBet: v[2], MaxWager: v[3]}
		if err := terms.Validate(); err != nil {
			m.errorText = "Check minimum bet, maximum wager and total amounts."
			return nil
		}
		relay := m.fields[4].Value()
		if !strings.HasPrefix(relay, "ws://") && !strings.HasPrefix(relay, "wss://") {
			m.errorText = "Relay must begin with ws:// or wss://."
			return nil
		}
		return m.submit(game.Input{Kind: game.StartSession, Terms: terms, RelayURL: relay}, false)
	case joinModal:
		inv, err := game.DecodeInvitation(strings.TrimSpace(m.fields[0].Value()))
		if err != nil {
			m.errorText = "Invalid Go poker invitation."
			return nil
		}
		cmd := m.openForm(joinConfirmModal)
		m.joinInvitation = &inv
		return cmd
	case raiseModal:
		r, ok := m.raiseRange()
		n, err := amount(m.fields[0].Value())
		if err != nil || !ok || n < r.min || n > r.max {
			m.errorText = "Raise amount must be within the displayed range."
			return nil
		}
		return m.submit(game.Input{Kind: game.Bet, Bet: covenant.BettingAction{Kind: covenant.RaiseTo, Amount: r.theirs + n}}, false)
	}
	return nil
}

func (m *Model) gameStatus() string {
	if m.snapshot.Outcome != nil {
		return "Hand complete | P: Payout details"
	}
	if m.snapshot.State != nil && m.snapshot.State.Phase.Kind == covenant.ShowdownEvaluation {
		if m.snapshot.Stage == game.StageEvaluateShowdown {
			return "Cards revealed | Evaluating hands"
		}
		return "Cards revealed | Payout pending"
	}
	if m.snapshot.Choice != nil && len(m.snapshot.Choice.Allowed) > 0 {
		return "Your action | Arrows select | Enter activates | Esc menu"
	}
	switch m.snapshot.Stage {
	case game.StageInit:
		return "Restoring saved game..."
	case game.StageSessionPrepared, game.StageAwaitOpponentKeys:
		return "Waiting for opponent | T: Invitation"
	case game.StageAwaitInitialShuffle, game.StageAwaitFinalShuffle:
		return "Waiting for opponent's shuffle"
	case game.StageInitialDeposit, game.StagePlayer1Funding:
		return "Funding game..."
	case game.StageAwaitInitialDeposit, game.StageAwaitPlayer1Funding:
		return "Waiting for opponent funding"
	case game.StageAwaitPlayer2Opening:
		return "Waiting for Player 2's opening move"
	case game.StageBettingOpponent, game.StageAllInOpponent:
		return "Opponent's turn"
	case game.StageBoardRevealOpponent, game.StageShowdownOpponent, game.StageAllInRevealOpponent:
		return "Waiting for opponent's reveal"
	case game.StageTransactionPrepared, game.StageTransactionSigned, game.StageAwaitTransactionAcceptance:
		return "Waiting for accepted transaction"
	default:
		return "Advancing game..."
	}
}
