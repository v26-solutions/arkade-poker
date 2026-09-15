package game

import (
	"context"
	"io"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/merkel"
	"arkade-poker/go/internal/shuffle"
)

var slotCards = [18]int{0, 1, 2, 3, 4, 5, 6, 4, 5, 6, 7, 7, 8, 8, 0, 1, 2, 3}
var slotPublishers = [18]covenant.Player{2, 2, 1, 1, 1, 1, 1, 2, 2, 2, 1, 2, 1, 2, 1, 1, 2, 2}

func dealOrder[T any](d covenant.DealtCards[T]) [9]T {
	return [9]T{d.HoleCards.Player1[0], d.HoleCards.Player1[1], d.HoleCards.Player2[0], d.HoleCards.Player2[1], d.Flop[0], d.Flop[1], d.Flop[2], d.Turn, d.River}
}
func dealFrom[T any](d [9]T) covenant.DealtCards[T] {
	return covenant.DealtCards[T]{HoleCards: covenant.PerPlayer[[2]T]{Player1: [2]T{d[0], d[1]}, Player2: [2]T{d[2], d[3]}}, Flop: [3]T{d[4], d[5], d[6]}, Turn: d[7], River: d[8]}
}
func ownSlots(p covenant.Player) []int { return []int{14 + 2*(int(p)-1), 15 + 2*(int(p)-1)} }
func boardSlots(p covenant.Player, street covenant.Street) []int {
	n := int(p) - 1
	switch street {
	case covenant.Flop:
		return []int{4 + 3*n, 5 + 3*n, 6 + 3*n}
	case covenant.Turn:
		return []int{10 + n}
	case covenant.River:
		return []int{12 + n}
	}
	return nil
}
func remainingSlots(p covenant.Player, street covenant.Street) []int {
	var out []int
	for s := street; s <= covenant.River; s++ {
		out = append(out, boardSlots(p, s)...)
	}
	return append(out, ownSlots(p)...)
}
func revealSlots(p covenant.Phase) []int {
	actor, _ := p.RequiredActor()
	switch p.Kind {
	case covenant.AwaitPlayer1FundingAndReveal:
		return []int{2, 3}
	case covenant.AwaitPlayer2RevealAndOpening:
		return []int{0, 1}
	case covenant.BoardReveal:
		return boardSlots(actor, p.Street)
	case covenant.ShowdownReveal:
		return ownSlots(actor)
	case covenant.AllIn:
		if p.Street == covenant.River {
			return ownSlots(actor)
		}
		return remainingSlots(actor, p.Street+1)
	case covenant.AllInReveal:
		return remainingSlots(actor, p.Street)
	}
	return nil
}
func revealKind(p covenant.Phase) covenant.RevealKind {
	switch p.Kind {
	case covenant.AwaitPlayer1FundingAndReveal, covenant.AwaitPlayer2RevealAndOpening:
		return covenant.RevealOpponentHoles
	case covenant.BoardReveal:
		return covenant.RevealFlop + covenant.RevealKind(p.Street-covenant.Flop)
	case covenant.ShowdownReveal:
		return covenant.RevealOwnHoles
	case covenant.AllIn:
		if p.Street == covenant.River {
			return covenant.RevealOwnHoles
		}
		return covenant.RevealRemainingFromFlop + covenant.RevealKind(p.Street-covenant.PreFlop)
	case covenant.AllInReveal:
		return covenant.RevealRemainingFromFlop + covenant.RevealKind(p.Street-covenant.Flop)
	}
	return 0
}
func revealFields(w *covenant.RevealWitness) []*covenant.CardReveal {
	holes := []*covenant.CardReveal{&w.Holes[0], &w.Holes[1]}
	flop := []*covenant.CardReveal{&w.Flop[0], &w.Flop[1], &w.Flop[2]}
	switch w.Kind {
	case covenant.RevealOpponentHoles, covenant.RevealOwnHoles:
		return holes
	case covenant.RevealFlop:
		return flop
	case covenant.RevealTurn:
		return []*covenant.CardReveal{&w.Turn}
	case covenant.RevealRiver:
		return []*covenant.CardReveal{&w.River}
	case covenant.RevealRemainingFromFlop:
		return append(append(flop, &w.Turn, &w.River), holes...)
	case covenant.RevealRemainingFromTurn:
		return append([]*covenant.CardReveal{&w.Turn, &w.River}, holes...)
	case covenant.RevealRemainingFromRiver:
		return append([]*covenant.CardReveal{&w.River}, holes...)
	}
	return nil
}
func (g *Game) verifyShare(slot int, r covenant.CardReveal) (shuffle.VerifiedRevealToken, error) {
	if g.setup == nil || g.setup.params == nil || slot < 0 || slot >= 18 {
		return shuffle.VerifiedRevealToken{}, ErrProtocol
	}
	id, _ := g.setup.contract.ID()
	binding, _ := shuffle.RevealBinding([32]byte(id), byte(slot))
	cards := dealOrder(g.setup.params.EncryptedDeal)
	return shuffle.VerifyReveal(g.setup.verifiedKeys[int(slotPublishers[slot])-1], cards[slotCards[slot]], r.Share, r.Proof, binding)
}
func (g *Game) makeShare(ctx context.Context, entropy io.Reader, slot int) (covenant.CardReveal, error) {
	if g.setup == nil || slot < 0 || slot >= 18 || slotPublishers[slot] != g.setup.role {
		return covenant.CardReveal{}, ErrInput
	}
	secret, err := g.setup.secrets.MarshalBinary()
	if err != nil {
		return covenant.CardReveal{}, err
	}
	defer clear(secret)
	sk, err := shuffle.DecodeSecretKey(secret[2:34])
	if err != nil {
		return covenant.CardReveal{}, err
	}
	defer sk.Destroy()
	id, _ := g.setup.contract.ID()
	binding, _ := shuffle.RevealBinding([32]byte(id), byte(slot))
	cards := dealOrder(g.setup.params.EncryptedDeal)
	share, proof, err := shuffle.Reveal(ctx, entropy, sk, cards[slotCards[slot]], binding)
	if err != nil {
		return covenant.CardReveal{}, err
	}
	r := covenant.CardReveal{Share: share, Proof: proof}
	_, err = g.verifyShare(slot, r)
	return r, err
}
func (g *Game) makeReveals(ctx context.Context, entropy io.Reader) (covenant.RevealWitness, error) {
	if g.hand == nil {
		return covenant.RevealWitness{}, ErrInput
	}
	state, err := covenant.ReadState(g.hand.accepted.Transaction)
	if err != nil {
		return covenant.RevealWitness{}, err
	}
	w := covenant.RevealWitness{Kind: revealKind(state.Phase)}
	fields := revealFields(&w)
	slots := revealSlots(state.Phase)
	if len(fields) == 0 || len(fields) != len(slots) {
		return covenant.RevealWitness{}, ErrInput
	}
	for i, p := range fields {
		*p, err = g.makeShare(ctx, entropy, slots[i])
		if err != nil {
			return covenant.RevealWitness{}, err
		}
	}
	return w, nil
}
func (g *Game) decodedCards(h *handState) ([9]KnownCard, error) {
	var out [9]KnownCard
	shares := h.reveals
	if h.privateHoles != nil {
		for i, slot := range ownSlots(g.setup.role) {
			shares[slot] = &h.privateHoles[i]
		}
	}
	protocol, err := shuffle.New()
	if err != nil {
		return out, err
	}
	deal := dealOrder(g.setup.params.EncryptedDeal)
	seen := [52]bool{}
	for i, pair := range [9][2]int{{0, 14}, {1, 15}, {2, 16}, {3, 17}, {4, 7}, {5, 8}, {6, 9}, {10, 11}, {12, 13}} {
		if shares[pair[0]] == nil || shares[pair[1]] == nil {
			continue
		}
		a, err := g.verifyShare(pair[0], *shares[pair[0]])
		if err != nil {
			return out, err
		}
		b, err := g.verifyShare(pair[1], *shares[pair[1]])
		if err != nil {
			return out, err
		}
		sum, err := shuffle.AggregateReveals([]shuffle.VerifiedRevealToken{a, b})
		if err != nil {
			return out, err
		}
		card, err := protocol.RevealCard(deal[i], sum)
		if err != nil {
			return out, err
		}
		if seen[card] {
			return out, ErrProtocol
		}
		seen[card] = true
		out[i] = KnownCard{Known: true, Index: card}
	}
	return out, nil
}
func (g *Game) knownCards(h *handState) (covenant.DealtCards[KnownCard], error) {
	cards, err := g.decodedCards(h)
	if err != nil {
		return covenant.DealtCards[KnownCard]{}, err
	}
	// Match the reference UI projection: own holes and accepted complete board
	// prefix only. Opponent holes are still used internally for settlement.
	offset := 0
	if g.setup.role == covenant.Player1 {
		offset = 2
	}
	cards[offset], cards[offset+1] = KnownCard{}, KnownCard{}
	if !cards[4].Known || !cards[5].Known || !cards[6].Known {
		for i := 4; i < 9; i++ {
			cards[i] = KnownCard{}
		}
	} else if !cards[7].Known {
		cards[8] = KnownCard{}
	}
	return dealFrom(cards), nil
}
func (g *Game) evaluate(ctx context.Context) (covenant.ShowdownWitness, covenant.Player, error) {
	cards, err := g.decodedCards(g.hand)
	if err != nil {
		return covenant.ShowdownWitness{}, 0, err
	}
	var deal [9]byte
	for i, c := range cards {
		if !c.Known {
			return covenant.ShowdownWitness{}, 0, ErrProtocol
		}
		deal[i] = c.Index
	}
	e, err := merkel.Evaluate(ctx, deal)
	if err != nil {
		return covenant.ShowdownWitness{}, 0, err
	}
	return covenant.ShowdownWitness{Cards: dealFrom(deal), RankProofs: covenant.PerPlayer[merkel.HandProof]{Player1: e.Player1, Player2: e.Player2}}, covenant.Player(e.Winner), nil
}
func (g *Game) checkEvaluation(h *handState, w covenant.ShowdownWitness) (covenant.Player, error) {
	cards, err := g.decodedCards(h)
	if err != nil {
		return 0, err
	}
	deal := dealOrder(w.Cards)
	for i, c := range cards {
		if !c.Known || c.Index != deal[i] {
			return 0, ErrProtocol
		}
	}
	for i, p := range []merkel.HandProof{w.RankProofs.Player1, w.RankProofs.Player2} {
		seven := [7]byte{deal[2*i], deal[2*i+1], deal[4], deal[5], deal[6], deal[7], deal[8]}
		rank, err := merkel.Rank7(seven)
		if err != nil {
			return 0, err
		}
		if rank != p.Rank || !p.Verify(seven) {
			return 0, ErrProtocol
		}
	}
	if w.RankProofs.Player1.Rank > w.RankProofs.Player2.Rank {
		return covenant.Player1, nil
	}
	if w.RankProofs.Player2.Rank > w.RankProofs.Player1.Rank {
		return covenant.Player2, nil
	}
	return 0, nil
}
