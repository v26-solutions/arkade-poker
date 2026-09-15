package ui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var green = lipgloss.Color("#75ff2d")
var black = lipgloss.Color("#000000")
var textStyle = lipgloss.NewStyle().Foreground(green).Background(black)
var panelStyle = textStyle.Border(lipgloss.NormalBorder())

// Preserve the human-readable prefix and separator, then show four payload
// characters and the last four checksum characters. Only display text is
// shortened; the stored receive address and clipboard value stay complete.
func shortAddress(address string) string {
	separator := strings.LastIndexByte(address, '1')
	if separator < 1 || len(address)-separator-1 <= 8 {
		return address
	}
	return address[:separator+5] + "..." + address[len(address)-4:]
}

func (m *Model) walletHeader() string {
	if m.clearing {
		return "Clearing..."
	}
	if m.connecting {
		return "Connecting..."
	}
	if m.receive.Address != "" {
		balance := "... sats"
		if m.balanceFailed {
			balance = "Balance unavailable"
		} else if m.balanceKnown {
			balance = formatSats(m.balance) + " sats"
		}
		return shortAddress(m.receive.Address) + "  " + balance
	}
	return "[ Add Wallet ]"
}

func formatSats(sats int64) string {
	digits := strconv.FormatInt(sats, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

func (m *Model) View() tea.View {
	w, h := max(1, m.width), max(1, m.height)
	content := ""
	if w < 76 || h < 30 {
		content = fmt.Sprintf("ARKADE POKER\n\nResize to at least 76 × 30\nCurrent: %d × %d\n\nYour input is preserved.", w, h)
		if m.modal == exitModal || m.modal == clearGameModal {
			content = m.modalBody()
		}
	} else {
		button := m.walletHeader()
		title := "ARKADE POKER"
		header := title + strings.Repeat(" ", max(1, w-4-lipgloss.Width(title)-lipgloss.Width(button))) + button
		body := m.body()
		if countdown := m.countdown(); countdown != "" {
			body = textStyle.Bold(true).Render(countdown) + "\n\n" + body
		}
		if m.errorText != "" {
			message := strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return ' '
				}
				return r
			}, ansi.Strip(m.errorText))
			body += "\n\n" + ansi.Truncate(message, max(1, w-8), "…")
		}
		status := m.status
		if m.shuffling {
			status = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[m.frame%10] + " shuffling..."
		}
		if m.frame < m.copyNoticeUntil {
			status += " | " + m.copyNotice
		}
		if m.canClearGame() && m.modal == noModal {
			status = "[X] Clear saved game | " + status
		}
		network := m.network
		if m.session != nil && m.session.Network != "" {
			network = m.session.Network
		}
		if network == "" {
			network = "…"
		}
		network += " ●"
		status = ansi.Truncate(status, max(1, w-4-lipgloss.Width(network)-1), "…")
		status += strings.Repeat(" ", max(1, w-4-lipgloss.Width(status)-lipgloss.Width(network))) + network
		content = panelStyle.Width(w-2).Render(header) + "\n" +
			panelStyle.Width(w-2).Height(h-8).Align(lipgloss.Center, lipgloss.Center).Render(body) + "\n" +
			panelStyle.Width(w-2).Render(status)
	}
	v := tea.NewView(textStyle.Width(w).Height(h).Render(content))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor, v.ForegroundColor = black, green
	v.WindowTitle = "Arkade Poker"
	return v
}
