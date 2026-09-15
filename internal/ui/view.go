package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var green = lipgloss.Color("#7cff00")
var black = lipgloss.Color("#000000")

// The pinned Ghostty renderer treats RGB(0,0,0) as default foreground.
// Near-black preserves the demo's dark labels on green in both hosts.
var ink = lipgloss.Color("#010101")
var textStyle = lipgloss.NewStyle().Foreground(green).Background(black)

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
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return sign + digits
}

func (m *Model) View() tea.View {
	v := tea.NewView(m.layout().content())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor, v.ForegroundColor = black, green
	v.WindowTitle = "Arkade Poker"
	return v
}
