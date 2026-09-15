// shuffle-qualify exercises the production crypto and shared UI locally, without
// wallet keys, services or gameplay. This is an explicit qualification harness.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arkade-poker/go/internal/merkel"
	"arkade-poker/go/internal/shuffle"
	"arkade-poker/go/internal/ui"
	tea "charm.land/bubbletea/v2"
	booba "github.com/NimbleMarkets/go-booba"
)

type measurement struct {
	Name         string
	Milliseconds float64
}
type report struct {
	Operations     []measurement
	SpinnerFrames  int
	DistinctFrames int
	Error          string
}
type resultMsg report
type startMsg struct{}
type model struct {
	inner    *ui.Model
	busy     bool
	started  bool
	frames   int
	distinct map[rune]bool
	report   report
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.inner.Init(), startCommand()) }
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case startMsg:
		if m.started {
			return m, nil
		}
		m.started = true
		m.busy = true
		_, cmd := m.inner.Update(ui.ProgressMsg{Shuffling: true})
		return m, tea.Batch(cmd, qualify)
	case resultMsg:
		m.busy = false
		m.report = report(msg)
		m.report.SpinnerFrames = m.frames
		m.report.DistinctFrames = len(m.distinct)
		status := "Shuffle qualification passed"
		if m.report.Error != "" {
			status = "Shuffle qualification failed: " + m.report.Error
		}
		_, cmd := m.inner.Update(ui.ProgressMsg{Status: status})
		publish(m.report)
		return m, tea.Batch(cmd, finishCommand())
	}
	_, cmd := m.inner.Update(msg)
	return m, cmd
}
func (m *model) View() tea.View {
	v := m.inner.View()
	if m.busy && strings.Contains(v.Content, "shuffling...") {
		for _, r := range "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏" {
			if strings.ContainsRune(v.Content, r) {
				m.frames++
				m.distinct[r] = true
				break
			}
		}
	}
	return v
}
func qualify() tea.Msg {
	r := report{}
	err := runCrypto(&r)
	if err != nil {
		r.Error = err.Error()
	}
	return resultMsg(r)
}
func runCrypto(r *report) error {
	ctx := context.Background()
	binding := []byte("go/shuffle-host-qualification/v1")
	p, err := shuffle.New()
	if err != nil {
		return err
	}
	var secrets [2]*shuffle.SecretKey
	var keys [2]shuffle.VerifiedPublicKey
	for i := range keys {
		var pk shuffle.PublicKey
		var own shuffle.OwnershipProof
		secrets[i], pk, own, err = shuffle.GenerateKey(ctx, nil, binding)
		if err != nil {
			return err
		}
		defer secrets[i].Destroy()
		keys[i], err = shuffle.VerifyOwnership(pk, own, binding)
		if err != nil {
			return err
		}
	}
	key, err := shuffle.AggregateKeys(keys[:])
	if err != nil {
		return err
	}
	record := func(name string, start time.Time) {
		r.Operations = append(r.Operations, measurement{name, float64(time.Since(start)) / float64(time.Millisecond)})
	}
	// Repetition provides a visible interval for observing the actual status-row
	// spinner. Each iteration generates fresh permutations, ciphertexts and proofs.
	var final shuffle.VerifiedDeck
	for round := range 12 {
		start := time.Now()
		deck, proof, err := p.ShuffleInitial(ctx, nil, key, binding)
		if err != nil {
			return err
		}
		record(fmt.Sprintf("%d initial+proof", round), start)
		start = time.Now()
		first, err := p.VerifyInitial(ctx, key, deck, proof, binding)
		if err != nil {
			return err
		}
		record(fmt.Sprintf("%d verify initial", round), start)
		start = time.Now()
		deck, proof, err = p.Shuffle(ctx, nil, key, first, binding)
		if err != nil {
			return err
		}
		record(fmt.Sprintf("%d reshuffle+proof", round), start)
		start = time.Now()
		final, err = p.Verify(ctx, key, first, deck, proof, binding)
		if err != nil {
			return err
		}
		record(fmt.Sprintf("%d verify reshuffle", round), start)
	}
	var plain [shuffle.DeckSize]byte
	var seen [shuffle.DeckSize]bool
	start := time.Now()
	for i := range plain {
		card, err := final.Card(i)
		if err != nil {
			return err
		}
		var shares [2]shuffle.VerifiedRevealToken
		for j := range shares {
			token, proof, err := shuffle.Reveal(ctx, nil, secrets[j], card, binding)
			if err != nil {
				return err
			}
			shares[j], err = shuffle.VerifyReveal(keys[j], card, token, proof, binding)
			if err != nil {
				return err
			}
		}
		token, err := shuffle.AggregateReveals(shares[:])
		if err != nil {
			return err
		}
		plain[i], err = p.RevealCard(card, token)
		if err != nil {
			return err
		}
		if seen[plain[i]] {
			return fmt.Errorf("duplicate plaintext")
		}
		seen[plain[i]] = true
	}
	record("52 reveals with verification", start)
	start = time.Now()
	e, err := merkel.Evaluate(ctx, [9]byte(plain[:9]))
	if err != nil {
		return err
	}
	record("two Merkle hand proofs", start)
	if !e.Player1.Verify([7]byte{plain[0], plain[1], plain[4], plain[5], plain[6], plain[7], plain[8]}) || !e.Player2.Verify([7]byte{plain[2], plain[3], plain[4], plain[5], plain[6], plain[7], plain[8]}) {
		return fmt.Errorf("hand proof verification")
	}
	return nil
}
func main() {
	m := &model{inner: ui.New(context.Background(), ui.Host{Browser: isBrowser}), distinct: map[rune]bool{}}
	p := booba.NewProgram(m, tea.WithoutSignalHandler())
	installStart(func() { p.Send(startMsg{}) })
	if _, err := p.Run(); err != nil {
		panic(err)
	}
	finished(m.report)
}
