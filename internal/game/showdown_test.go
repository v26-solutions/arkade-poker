package game

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"testing"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/shuffle"
)

// Control only the fixture's Fisher-Yates entropy to exercise specified results.
// Every ownership/shuffle/reveal proof is generated and verified normally, and
// the full setup log still commits the first nine positions of the final deck.
func handForDeal(t *testing.T, deal [9]byte) *handHarness {
	t.Helper()
	f := completeSetup(t)
	h := newHandHarness(t)
	h.logs[0] = append([][]byte(nil), f.logs[0][:setupEventIndex(t, f.logs[0], MessagePrepared, FinalShuffle)]...)
	h.logs[1] = append([][]byte(nil), f.logs[1][:setupEventIndex(t, f.logs[1], MessageReceived, FinalShuffle)]...)
	for i := range h.games {
		h.games[i] = replayBytes(t, f.configs[i], h.logs[i])
	}
	var secrets [2]*shuffle.SecretKey
	for i, g := range h.games {
		b, err := g.setup.secrets.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		sk, err := shuffle.DecodeSecretKey(b[2:34])
		clear(b)
		if err != nil {
			t.Fatal(err)
		}
		secrets[i] = sk
		defer sk.Destroy()
	}
	p, err := shuffle.New()
	if err != nil {
		t.Fatal(err)
	}
	positions := [52]int{}
	for i, card := range *h.games[0].setup.initial.Deck {
		verified := make([]shuffle.VerifiedRevealToken, 0, 2)
		for player := range secrets {
			binding := []byte("game result fixture selection")
			share, proof, err := shuffle.Reveal(context.Background(), nil, secrets[player], card, binding)
			if err != nil {
				t.Fatal(err)
			}
			v, err := shuffle.VerifyReveal(h.games[0].setup.verifiedKeys[player], card, share, proof, binding)
			if err != nil {
				t.Fatal(err)
			}
			verified = append(verified, v)
		}
		sum, err := shuffle.AggregateReveals(verified)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := p.RevealCard(card, sum)
		if err != nil {
			t.Fatal(err)
		}
		positions[plain] = i
	}
	var wanted, current [52]int
	used := [52]bool{}
	for i, c := range deal {
		wanted[i] = positions[c]
		used[positions[c]] = true
	}
	next := 9
	for i := range wanted {
		current[i] = i
		if !used[i] {
			wanted[next] = i
			next++
		}
	}
	var prefix bytes.Buffer
	for i := 51; i > 0; i-- {
		j := 0
		for current[j] != wanted[i] {
			j++
		}
		if j > i {
			t.Fatal("invalid fixture permutation")
		}
		bound := uint32(i + 1)
		draw := uint32(j)
		threshold := uint32((uint64(1) << 32) % uint64(bound))
		if draw < threshold {
			draw += bound
		}
		_ = binary.Write(&prefix, binary.LittleEndian, draw)
		current[i], current[j] = current[j], current[i]
	}
	if current != wanted {
		t.Fatal("fixture shuffle entropy")
	}
	e, err := h.games[0].PrepareShuffle(context.Background(), io.MultiReader(&prefix, rand.Reader), func() (covenant.UnixSeconds, error) { return 1000, nil })
	if err != nil {
		t.Fatal(err)
	}
	h.record(0, e)
	m, err := h.games[0].PendingMessage()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	publication := h.games[0].NewEvent(PublicationPrepared)
	publication.Publication = &ports.PreparedMessage{Payload: payload, Carrier: []byte("opaque fixture carrier")}
	h.record(0, publication)
	sent := h.games[0].NewEvent(MessagePublished)
	sent.Message = &m
	h.record(0, sent)
	received := h.games[1].NewEvent(MessageReceived)
	received.Message = &m
	received.ObservedAt = 1000
	h.record(1, received)
	return h
}
func TestWinnerLoserTiePriorityAndPayouts(t *testing.T) {
	deals := [][9]byte{{12, 25, 1, 14, 0, 17, 32, 47, 11}, {1, 14, 12, 25, 0, 17, 32, 47, 11}, {0, 1, 13, 14, 8, 9, 10, 11, 12}}
	for i, deal := range deals {
		t.Run([]string{"player1", "player2", "tie"}[i], func(t *testing.T) {
			h := handForDeal(t, deal)
			h.start(testTerms.MaxWager)
			h.action(0, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Call}})
			h.action(1, Input{Kind: RevealShowdown})
			expected := covenant.Player(i + 1)
			if i == 2 {
				expected = 0
			}
			for player, g := range h.games {
				e, err := g.PrepareEvaluation(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if e.Winner != expected {
					t.Fatalf("wanted winner %d, got %d", expected, e.Winner)
				}
				h.record(player, e)
			}
			priority := 0
			if expected == covenant.Player2 {
				priority = 1
			}
			fallback := 1 - priority
			at := h.games[fallback].hand.observedAt
			if _, err := h.games[fallback].PrepareAction(context.Background(), nil, Input{Kind: Progress}, covenant.Funding{}, at+29); err == nil {
				t.Fatal("fallback settled before 30 seconds")
			}
			if _, err := h.games[priority].PrepareAction(context.Background(), nil, Input{Kind: Progress}, covenant.Funding{}, at); err != nil {
				t.Fatal("priority could not settle immediately", err)
			}
			h.at = at + 30
			h.action(fallback, Input{Kind: Progress})
			for player, g := range h.games {
				want := Tied
				if expected != 0 {
					want = Lost
					if player == int(expected)-1 {
						want = Won
					}
				}
				if g.outcome.Kind != want {
					t.Fatal("outcome classification")
				}
				outs := g.outcome.Transaction.TxOut
				total := (testTerms.Stake + testTerms.Bond + testTerms.MaxWager) * 2
				amounts := [2]int64{total / 2, total / 2}
				if expected != 0 {
					amounts = [2]int64{testTerms.Bond, testTerms.Bond}
					amounts[int(expected)-1] = total - testTerms.Bond
				}
				if outs[0].Value != amounts[0] || outs[1].Value != amounts[1] {
					t.Fatal("terminal payout amounts")
				}
			}
			if i == 2 {
				h.replay()
			}
		})
	}
}
