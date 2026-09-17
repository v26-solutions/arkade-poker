package ui

import (
	"fmt"
	"strings"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/merkel"
	"arkade-poker/go/internal/palette"
	"charm.land/lipgloss/v2"
)

func playingCard(c game.KnownCard, w, h int, winning, imageSuits bool) string {
	style := textStyle.Border(lipgloss.NormalBorder()).BorderForeground(accent)
	if winning {
		highlight := lipgloss.Color(palette.Highlight)
		style = style.Border(lipgloss.DoubleBorder()).Foreground(ink).Background(highlight).
			BorderForeground(highlight).BorderBackground(highlight)
	}
	rows := strings.Split(fit("", w-2, h-2, false), "\n")
	if c.Known && c.Index < 52 {
		rank := []string{"2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K", "A"}[c.Index%13]
		// Use a text-presentation heart to avoid Ghostty's hollow U+2665 fallback.
		suit := []string{"♣", "♦", "❤\ufe0e", "♠"}[c.Index/13]
		gap := strings.Repeat(" ", w-2-lipgloss.Width(rank+suit))
		rows[0] = rank + gap + suit
		if h > 3 {
			rows[len(rows)-1] = suit + gap + rank
			rows[len(rows)/2] = fit(suit, w-2, 1, true)
			if imageSuits && h >= 7 {
				art := suitImageCells(c.Index / 13)
				start := (len(rows) - len(art)) / 2
				for i, row := range art {
					rows[start+i] = fit(row, w-2, 1, true)
				}
			}
		}
	} else {
		for i := range rows {
			rows[i] = strings.Repeat("░", w-2)
		}
	}
	return style.Render(strings.Join(rows, "\n"))
}

func cardGroup(cards []game.KnownCard, w, h int, winning map[byte]bool, imageSuits bool) string {
	var parts []string
	for i, c := range cards {
		if i != 0 {
			parts = append(parts, "  ")
		}
		parts = append(parts, playingCard(c, w, h, c.Known && winning[c.Index], imageSuits))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func handDescription(holes [2]game.KnownCard, board []game.KnownCard) (string, map[byte]bool) {
	if !holes[0].Known || !holes[1].Known {
		return "NOT REVEALED", nil
	}
	cards := []byte{holes[0].Index, holes[1].Index}
	for _, c := range board {
		if c.Known {
			cards = append(cards, c.Index)
		}
	}
	if len(cards) < 5 {
		return "", nil
	}
	label, best, err := merkel.Describe(cards)
	if err != nil {
		return "", nil
	}
	mask := map[byte]bool{}
	for _, c := range best {
		mask[c] = true
	}
	return label, mask
}

func (m *Model) tableSections(f *screen, boardY, boardH, handY, handH int) {
	s := m.snapshot
	board := []game.KnownCard{s.Cards.Flop[0], s.Cards.Flop[1], s.Cards.Flop[2], s.Cards.Turn, s.Cards.River}
	own, opponent := s.Cards.HoleCards.Player1, s.Cards.HoleCards.Player2
	if s.Role == covenant.Player2 {
		own, opponent = opponent, own
	}
	mine, theirs := m.wagers()
	ownLabel, ownBest := handDescription(own, board)
	oppLabel, oppBest := handDescription(opponent, board)
	var winning map[byte]bool
	if s.Outcome != nil && s.Outcome.Settlement.Kind == game.SettlementShowdown {
		winning = ownBest
		if s.Outcome.Kind == game.Lost {
			winning = oppBest
		}
	}
	street := map[covenant.Street]string{covenant.PreFlop: "PRE-FLOP", covenant.Flop: "FLOP", covenant.Turn: "TURN", covenant.River: "RIVER"}[s.State.Phase.Street]
	if s.State.Phase.Kind == covenant.ShowdownReveal || s.State.Phase.Kind == covenant.ShowdownEvaluation || s.State.Phase.Kind == covenant.AllInReveal {
		street = "SHOWDOWN"
	}
	if s.Outcome != nil && s.Outcome.Settlement.Kind == game.SettlementShowdown {
		street = "SHOWDOWN"
	}
	if street == "" {
		street = "PREPARING HAND"
	}
	oppStatus := "OPPONENT / HIDDEN"
	if opponent[0].Known && opponent[1].Known {
		oppStatus = "OPPONENT / REVEALED"
	}
	if s.Outcome != nil && s.Outcome.Settlement.Kind == game.SettlementConcession && s.Outcome.Kind == game.Won {
		oppStatus = "OPPONENT / FOLDED"
	}
	oppInfo := oppStatus + "\n" + dimStyle.Render(fmt.Sprintf("IN THIS HAND %s SATS", formatSats(s.Terms.Stake+int64(theirs))))
	if oppLabel != "" {
		oppInfo += "\n" + dimStyle.Render(oppLabel)
	}
	oppH, cardW, cardH := 3, 7, 5
	if boardH >= 17 {
		oppH, cardW, cardH = 5, 9, 7
	}
	if boardH >= 21 && f.w >= 110 {
		cardW, cardH = 11, 9
	}
	oppRow := lipgloss.JoinHorizontal(lipgloss.Center, cardGroup(opponent[:], 7, oppH, winning, m.host.Browser), "   ", oppInfo)
	pot := fmt.Sprintf("POT %s SATS", formatSats(2*s.Terms.Stake+int64(mine+theirs)))
	if winning != nil {
		pot += " / WINNING FIVE HIGHLIGHTED"
	} else {
		pot += fmt.Sprintf(" / BOND %s EACH", formatSats(s.Terms.Bond))
	}
	spacer := "\n"
	if boardH >= 17 {
		spacer = "\n\n"
	}
	// Center each differently sized row before stacking it into the board.
	inner := f.w - 4
	body := fit(oppRow, inner, oppH, true) + spacer + fit(cardGroup(board, cardW, cardH, winning, m.host.Browser), inner, cardH, true) + spacer + fit(pot, inner, 1, true)
	f.section("BOARD / "+street, body, boardY, boardH)
	remaining := max(int64(0), s.Terms.MaxWager-int64(mine))
	info := fmt.Sprintf("IN THIS HAND %s SATS", formatSats(s.Terms.Stake+int64(mine)))
	if ownLabel != "" {
		info += "\n" + dimStyle.Render(ownLabel)
	}
	wagers := fmt.Sprintf("WAGER %s / REMAINING %s", formatSats(int64(mine)), formatSats(remaining))
	if lipgloss.Width(wagers) > f.w-29 {
		wagers = strings.Replace(wagers, " / ", "\n", 1)
	}
	info += "\n" + dimStyle.Render(wagers)
	if s.Role == covenant.Player1 && s.State.Phase.Kind == covenant.AwaitPlayer2RevealAndOpening && !own[0].Known {
		info = "Cards appear after Player 2\nchecks or raises."
	}
	holeH := handH - 2
	holeW := 7
	if holeH >= 7 {
		holeW = 9
	}
	row := lipgloss.JoinHorizontal(lipgloss.Center, cardGroup(own[:], holeW, holeH, winning, m.host.Browser), "   ", info)
	f.section(fmt.Sprintf("YOUR HAND / PLAYER %d", s.Role), row, handY, handH)
}

func (m *Model) payout() (int64, bool) {
	s := m.snapshot
	if s.Outcome == nil || s.Outcome.Transaction == nil {
		return 0, false
	}
	o := s.Outcome
	out := o.Payouts.Player1
	if s.Role == covenant.Player2 {
		out = o.Payouts.Player2
	}
	if out.Hash != o.Transaction.TxHash() || int(out.Index) >= len(o.Transaction.TxOut) {
		return 0, false
	}
	return o.Transaction.TxOut[out.Index].Value, true
}

func (m *Model) resultSummary() string {
	o := m.snapshot.Outcome
	if o == nil {
		return ""
	}
	label := map[game.OutcomeKind]string{game.Won: "YOU WIN", game.Lost: "YOU LOSE", game.Tied: "SPLIT POT", game.Aborted: "SESSION ABORTED"}[o.Kind]
	badge := textStyle.Foreground(ink).Background(accent).Render(" " + label + " ")
	reason := "SHOWDOWN"
	if o.Settlement.Kind == game.SettlementConcession {
		reason = "FOLD"
	}
	if o.Settlement.Kind == game.SettlementTimeout {
		reason = "TIMEOUT"
	}
	if o.Kind == game.Aborted {
		return badge + " " + clean(o.AbortReason)
	}
	if value, ok := m.payout(); ok {
		mine, _ := m.wagers()
		net := value - m.snapshot.Terms.Stake - m.snapshot.Terms.Bond - int64(mine)
		netText := formatSats(net)
		if net > 0 {
			netText = "+" + netText
		}
		return fmt.Sprintf("%s  %s / PAYOUT %s / NET %s SATS", badge, reason, formatSats(value), netText)
	}
	return badge + "  " + reason
}
