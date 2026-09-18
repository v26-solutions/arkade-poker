package ui

import (
	"fmt"

	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	tea "charm.land/bubbletea/v2"
)

type balanceMsg struct {
	update     client.BalanceUpdate
	closed     bool
	generation uint64
}

// These amounts mirror the driver's RequiredFunding projection. The wallet
// still authenticates and selects spendable outputs when executing the action.
func (m *Model) inputFunding(input game.Input) int64 {
	if terms, ok := setupTerms(input); ok {
		return terms.Stake + terms.Bond
	}
	if input.Kind == game.Bet {
		switch input.Bet.Kind {
		case covenant.Call:
			if m.snapshot.Choice != nil {
				return m.snapshot.Choice.CallAmount
			}
		case covenant.RaiseTo:
			mine, _ := m.wagers()
			return input.Bet.Amount - int64(mine)
		}
	}
	return 0
}

func setupTerms(input game.Input) (game.Terms, bool) {
	switch input.Kind {
	case game.StartSession:
		return input.Terms, true
	case game.JoinSession:
		if input.Invitation != nil {
			return input.Invitation.Terms, true
		}
	}
	return game.Terms{}, false
}

func (m *Model) fundingStatus(required int64) string {
	if required <= 0 {
		return ""
	}
	if m.balanceFailed {
		return "Balance unavailable; waiting for refresh"
	}
	if !m.balanceKnown {
		return "Checking wallet balance..."
	}
	if m.balance < required {
		return "More funds required: add " + formatSats(required-m.balance) + " sats"
	}
	return ""
}

func (m *Model) actionFunding(a action) int64 {
	if a.key == "r" {
		if r, ok := m.raiseRange(); ok {
			return r.theirs + r.min - r.mine
		}
	}
	return m.inputFunding(a.input)
}

func (m *Model) modalFunding() int64 {
	switch m.modal {
	case createModal:
		if input, errText := m.createInput(); errText == "" {
			return m.inputFunding(input)
		}
	case joinConfirmModal:
		return m.inputFunding(game.Input{Kind: game.JoinSession, Invitation: m.joinInvitation})
	case setupWarningModal:
		if m.pendingSetup != nil {
			return m.inputFunding(*m.pendingSetup)
		}
	case raiseModal:
		if r, ok := m.raiseRange(); ok && len(m.fields) > 0 {
			if n, err := amount(m.fields[0].Value()); err == nil && n >= r.min && n <= r.max {
				return r.theirs + n - r.mine
			}
		}
	case allInModal:
		if r, ok := m.raiseRange(); ok {
			return r.theirs + r.max - r.mine
		}
	}
	return 0
}

func (m *Model) balanceStatus() string {
	if m.busy || m.stopped || m.shuffling {
		return ""
	}
	if m.modal != noModal {
		return m.fundingStatus(m.modalFunding())
	}
	actions := m.actions()
	// Keep the cheapest blocked action's shortfall visible while navigating
	// available actions, including free actions such as Fold.
	var required int64
	for _, a := range actions {
		n := m.actionFunding(a)
		if m.fundingStatus(n) != "" && (required == 0 || n < required) {
			required = n
		}
	}
	return m.fundingStatus(required)
}

func (m *Model) setupWarning() string {
	if m.pendingSetup == nil {
		return "Session unavailable."
	}
	t, _ := setupTerms(*m.pendingSetup)
	deposit := t.Stake + t.Bond
	total := deposit + t.MaxWager
	heading := "Your wallet cannot cover a maximum ALL-IN call."
	balance := "Checking..."
	shortfall := "Unknown"
	if m.balanceFailed {
		heading = "The wallet balance is currently unavailable."
		balance = "Unavailable"
	} else if !m.balanceKnown {
		heading = "Checking whether your wallet can cover a maximum ALL-IN call."
	} else {
		balance = formatSats(m.balance) + " sats"
		shortfall = formatSats(max(int64(0), total-m.balance)) + " sats"
		if m.balance >= total {
			heading = "Your wallet now covers the deposit and maximum ALL-IN call."
		}
	}
	return fmt.Sprintf("%s\n\nWallet balance: %s\nDeposit:        %s sats\nMaximum wager:  %s sats\nTotal needed:   %s sats\nShortfall:      %s\n\nYou may need to add funds or fold if faced with an ALL-IN.\nProceed with this session?",
		heading, balance, formatSats(deposit), formatSats(t.MaxWager), formatSats(total), shortfall)
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
