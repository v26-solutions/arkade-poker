package ui

import (
	"fmt"
	"strings"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func card(c game.KnownCard) string {
	rank, suit := "", "·"
	if c.Known && c.Index < 52 {
		rank = []string{"2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K", "A"}[c.Index%13]
		suit = []string{"♣", "♦", "♥", "♠"}[c.Index/13]
	}
	return panelStyle.Width(7).Height(3).Align(lipgloss.Center).Render(rank + "\n" + suit + "\n" + rank)
}
func cardRow(cards ...game.KnownCard) string {
	parts := []string{}
	for i, c := range cards {
		if i != 0 {
			parts = append(parts, "  ")
		}
		parts = append(parts, card(c))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}
func termsText(t game.Terms) string {
	return fmt.Sprintf("Stake: %d sats   Bond: %d sats\nMinimum bet: %d sats   Maximum wager: %d sats", t.Stake, t.Bond, t.MinBet, t.MaxWager)
}
func (m *Model) actionRow() string {
	a := m.actions()
	parts := []string{}
	for i, action := range a {
		label := "[" + strings.ToUpper(action.key) + "] " + action.label
		style := textStyle.Padding(0, 1)
		if i == m.selected {
			// The qualified browser rendered inverse selection as an unreadable
			// solid rectangle. An explicit marker keeps the label readable too.
			label = "> " + label
			style = style.Bold(true).Underline(true)
		}
		parts = append(parts, style.Render(label))
	}
	return strings.Join(parts, "  ")
}
func (m *Model) body() string {
	s := m.snapshot
	if m.modal != noModal {
		return m.modalBody()
	}
	if s.Outcome != nil {
		o := s.Outcome
		title := map[game.OutcomeKind]string{game.Won: "YOU WON", game.Lost: "HAND COMPLETE", game.Tied: "SPLIT POT", game.Aborted: "SESSION ABORTED"}[o.Kind]
		body := title
		if o.Kind == game.Aborted {
			body += "\n\n" + o.AbortReason
		} else if o.Transaction != nil {
			body += "\n\nAccepted payout transaction\n" + o.Transaction.TxHash().String()
			out := o.Payouts.Player1
			if s.Role == covenant.Player2 {
				out = o.Payouts.Player2
			}
			if out.Hash == o.Transaction.TxHash() && int(out.Index) < len(o.Transaction.TxOut) {
				body += fmt.Sprintf("\n\nYour payout: %d sats\nOutput %d", o.Transaction.TxOut[out.Index].Value, out.Index)
			}
		}
		return body + "\n\n" + m.actionRow()
	}
	if s.State != nil {
		c := s.Cards
		holes, wager, opponent := c.HoleCards.Player1, s.State.Wagers.Player1, s.State.Wagers.Player2
		if s.Role == covenant.Player2 {
			holes, wager, opponent = c.HoleCards.Player2, opponent, wager
		}
		street := map[covenant.Street]string{covenant.PreFlop: "PRE-FLOP", covenant.Flop: "FLOP", covenant.Turn: "TURN", covenant.River: "RIVER"}[s.State.Phase.Street]
		if street == "" {
			street = "BOARD"
		}
		pot := uint64(s.Terms.Stake)*2 + s.State.Wagers.Player1 + s.State.Wagers.Player2
		body := street + "\n" + cardRow(c.Flop[0], c.Flop[1], c.Flop[2], c.Turn, c.River)
		body += fmt.Sprintf("\nPOT %d SATS  |  Bonds %d sats each\n\nYOUR HAND · PLAYER %d\n", pot, s.Terms.Bond, s.Role)
		body += cardRow(holes[:]...)
		if !m.stopped && s.Role == covenant.Player1 && s.State.Phase.Kind == covenant.AwaitPlayer2RevealAndOpening && !holes[0].Known && !holes[1].Known {
			body += "\nYour cards appear after Player 2 checks or raises."
		}
		body += fmt.Sprintf("\nYour wager %d  |  Opponent wager %d  |  Available wager %d sats", wager, opponent, uint64(s.Terms.MaxWager)-wager)
		return body + "\n\n" + m.actionRow()
	}
	if s.Invitation != nil {
		return "SESSION READY\n\n" + termsText(s.Terms) + fmt.Sprintf("\n\nYou are Player %d\n", s.Role) + "\n" + m.actionRow()
	}
	body := "   ▄▀█ █▀█ █▄▀ ▄▀█ █▀▄ █▀▀\n   █▀█ █▀▄ █ █ █▀█ █▄▀ ██▄\n\n      █▀█ █▀█ █▄▀ █▀▀ █▀█\n      █▀▀ █▄█ █ █ ██▄ █▀▄"
	if m.key == nil {
		return body + "\n\nImport an externally funded Ark wallet.\n\n[A] Add Wallet"
	}
	return body + "\n\n" + m.actionRow()
}

func (m *Model) modalBody() string {
	switch m.modal {
	case walletModal:
		return "ADD WALLET\n\nEnter nsec, a hexadecimal key or a BIP39 mnemonic.\n\n" + m.input.View() + "\n\nEnter: Import    Escape: Cancel"
	case clearGameModal:
		buttons := "[ Cancel ]    Confirm clear"
		if m.clearConfirm {
			buttons = "Cancel    [ Confirm clear ]"
		}
		return fmt.Sprintf("CLEAR SAVED GAME?\n\nRemove all saved games for wallet %x…%x\nfrom this device?\n\nThis deletes recovery data and does not refund funds in a hand.\nYou will need to add the wallet again.\n\n%s\n\n←/→: Select    Enter: Activate    Escape: Cancel", m.clearPublic[:4], m.clearPublic[28:], buttons)
	case exitModal:
		buttons := "[ Cancel ]    Confirm exit"
		if m.exitConfirm {
			buttons = "Cancel    [ Confirm exit ]"
		}
		return "LEAVE POKER?\n\nYour saved session will remain on this device.\nGame deadlines continue while you are away.\n\n" + buttons + "\n\n←/→: Select    Enter: Activate    Escape: Cancel"
	case createModal:
		rows := make([]string, 0, len(m.fields))
		labelStyle := textStyle.Width(15).Align(lipgloss.Left)
		for i, label := range []string{"Stake", "Bond", "Minimum bet", "Maximum wager", "Nostr relay"} {
			rows = append(rows, labelStyle.Render(label)+m.fields[i].View())
		}
		// Pad rows as one block so the surrounding centered panel preserves
		// the label and input columns, including when focus changes.
		form := lipgloss.JoinVertical(lipgloss.Left, rows...)
		return "CREATE SESSION\n\nAmounts are per player, in satoshis.\n\n" + form +
			"\n\nTab: Next field   Enter: Create   Escape: Cancel"
	case joinModal:
		return "JOIN SESSION\n\nPaste a Go poker invitation.\n\n" + m.fields[0].View() + "\n\nEnter: Review terms    Escape: Cancel"
	case joinConfirmModal:
		if m.joinInvitation == nil {
			return "Invitation unavailable"
		}
		t := m.joinInvitation.Terms
		rows := []string{}
		for _, param := range []struct{ label, value string }{
			{"Stake:", formatSats(t.Stake) + " sats"},
			{"Bond:", formatSats(t.Bond) + " sats"},
			{"Minimum bet:", formatSats(t.MinBet) + " sats"},
			{"Maximum wager:", formatSats(t.MaxWager) + " sats"},
			{"Relay:", m.joinInvitation.RelayURL},
		} {
			row := fmt.Sprintf("%14s  %s", param.label, param.value)
			rows = append(rows, ansi.Truncate(row, max(1, min(68, m.width-12)), "…"))
		}
		params := lipgloss.JoinVertical(lipgloss.Left, rows...)
		return "JOIN THIS GAME?\n\n" + params + fmt.Sprintf("\n\nPress Enter to deposit %s sats and join\nEscape to cancel", formatSats(t.Stake+t.Bond))
	case tokenModal:
		if m.snapshot.Invitation == nil {
			return "Invitation unavailable"
		}
		token, err := game.EncodeInvitation(*m.snapshot.Invitation)
		if err != nil {
			return "Invitation unavailable"
		}
		rows := strings.Split(wrapToken(token, max(10, min(68, m.width-12))), "\n")
		limit := max(3, m.height-22)
		if len(rows) > limit {
			rows = rows[:limit]
			rows[limit-1] = "… (copy for the complete invitation)"
		}
		return "SESSION INVITATION\n\nShare this invitation with your opponent.\n\n" + strings.Join(rows, "\n") + "\n\nY / Enter: Copy invitation    Escape: Close"
	case raiseModal:
		r, ok := m.raiseRange()
		if !ok {
			return "Raise unavailable. Escape: Close"
		}
		preview := "Enter a raise amount within the displayed range."
		if n, err := amount(m.fields[0].Value()); err == nil && n >= r.min && n <= r.max {
			target := r.theirs + n
			preview = fmt.Sprintf("Call %s + raise %s = add %s sats\nYour total bet after this move: %s sats",
				formatSats(r.theirs-r.mine), formatSats(n), formatSats(target-r.mine), formatSats(target))
		}
		return fmt.Sprintf("RAISE BY\n\nAmount above opponent's bet, in sats.\nMinimum %s   Maximum %s\n\n%s\n\n%s\n\nEnter: Raise    Escape: Cancel",
			formatSats(r.min), formatSats(r.max), m.fields[0].View(), preview)
	}
	return ""
}
func wrapToken(text string, width int) string {
	var rows []string
	for len(text) > width {
		rows = append(rows, text[:width])
		text = text[width:]
	}
	return strings.Join(append(rows, text), "\n")
}
