package game

import (
	"bytes"
	"context"
	"math"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/shuffle"
)

func other(p covenant.Player) covenant.Player {
	if p == covenant.Player1 {
		return covenant.Player2
	}
	return covenant.Player1
}
func ownershipContext(inv Invitation, role covenant.Player, identity [32]byte, p covenant.Participant) ([]byte, error) {
	b, err := invitationBinary(inv)
	if err != nil {
		return nil, err
	}
	w := new(encoder)
	w.header("arkade-poker/ownership\x00")
	w.blob(b, 4096)
	w.u8(byte(role))
	w.raw(identity[:])
	w.raw(p.SigningKey[:])
	w.blob(p.PayoutScript, maxScriptBytes)
	return w.finish()
}
func checkMessageContext(inv Invitation, m Message, role covenant.Player, identity *[32]byte) error {
	if _, err := EncodeMessage(m); err != nil {
		return err
	}
	if m.SessionID != inv.SessionID || m.Sender != role || identity != nil && m.Identity != *identity {
		return ErrProtocol
	}
	if role == covenant.Player1 && m.Identity != inv.CreatorTransportKey {
		return ErrProtocol
	}
	if role == covenant.Player2 && identity == nil && (m.Sequence != 0 || m.Kind != KeyOffer) {
		return ErrProtocol
	}
	return nil
}

// WalletOwnershipDigest is signed and authenticated outside the reducer. Its
// opaque signature bytes are retained in the ordered shuffle transcript.
func WalletOwnershipDigest(inv Invitation, m Message) ([32]byte, error) {
	if m.Participant == nil || m.Ownership == nil {
		return [32]byte{}, ErrProtocol
	}
	context, err := ownershipContext(inv, m.Sender, m.Identity, *m.Participant)
	if err != nil {
		return [32]byte{}, err
	}
	key, err := m.Participant.EncryptionKey.MarshalBinary()
	if err != nil {
		return [32]byte{}, err
	}
	proof, err := m.Ownership.MarshalBinary()
	if err != nil {
		return [32]byte{}, err
	}
	return digest("arkade-poker/wallet-ownership/v1", context, key, proof), nil
}
func validateKeys(c Config, inv Invitation, m Message, role covenant.Player, identity *[32]byte) (shuffle.VerifiedPublicKey, error) {
	var zero shuffle.VerifiedPublicKey
	if err := checkMessageContext(inv, m, role, identity); err != nil {
		return zero, err
	}
	kind := KeyOffer
	if role == covenant.Player1 {
		kind = KeyReply
	}
	if m.Kind != kind || m.Sequence != 0 {
		return zero, ErrProtocol
	}
	for _, value := range []int64{inv.Terms.Bond, (inv.Terms.Stake + inv.Terms.Bond + inv.Terms.MaxWager) * 2} {
		if err := policyOutput(c, m.Participant.PayoutScript, value); err != nil {
			return zero, err
		}
	}
	binding, err := ownershipContext(inv, role, m.Identity, *m.Participant)
	if err != nil {
		return zero, err
	}
	return shuffle.VerifyOwnership(m.Participant.EncryptionKey, *m.Ownership, binding)
}
func sameMessage(a, b Message) bool {
	x, e1 := EncodeMessage(a)
	y, e2 := EncodeMessage(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}
func deadlineWindow(now, deadline covenant.UnixSeconds) error {
	// Keep a fixed safety margin before funding. Deriving the minimum from the
	// setup timeout would leave only 30 seconds for delivery and funding again.
	// The upper bound allows 30 seconds of clock skew without accepting an
	// arbitrarily distant deadline.
	const minimum = 30
	const maximum = initialFundingTimeout + 30
	if uint64(now) > math.MaxUint64-maximum || deadline < now+minimum || deadline > now+maximum {
		return ErrDeadline
	}
	return nil
}

// setupState is immutable once admitted. Reducers copy it before replacing any
// fields. Verified crypto caches never appear in a codec or confer admission to
// caller-supplied values.
type setupState struct {
	invitation            Invitation
	role                  covenant.Player
	secrets               *SessionSecrets
	local                 Message
	peer                  *Message
	publication           *ports.PreparedMessage
	pending               *Message
	nextSend, nextReceive uint64
	initial, final        *Message
	verifiedKeys          [2]shuffle.VerifiedPublicKey
	aggregate             shuffle.AggregatePublicKey
	verifiedInitial       shuffle.VerifiedDeck
	params                *covenant.Params
	contract              *covenant.Contract
}

func (s *setupState) orderedKeys() [2]Message {
	if s.role == covenant.Player1 {
		return [2]Message{s.local, *s.peer}
	}
	return [2]Message{*s.peer, s.local}
}
func (s *setupState) shuffleContext(final bool) ([]byte, error) {
	keys := s.orderedKeys()
	inv, err := invitationBinary(s.invitation)
	if err != nil {
		return nil, err
	}
	p1, err := EncodeMessage(keys[0])
	if err != nil {
		return nil, err
	}
	p2, err := EncodeMessage(keys[1])
	if err != nil {
		return nil, err
	}
	h := digest("", inv, p1, p2)
	b := append([]byte("arkade-poker/shuffle/v1"), h[:]...)
	stage := byte(0)
	if final {
		stage = 1
	}
	return append(b, stage), nil
}
func (s *setupState) admitPeer(c Config, m Message) error {
	key, err := validateKeys(c, s.invitation, m, other(s.role), nil)
	if err != nil {
		return err
	}
	if m.Sequence != s.nextReceive {
		return ErrOrder
	}
	a, _ := m.Participant.EncryptionKey.MarshalBinary()
	b, _ := s.local.Participant.EncryptionKey.MarshalBinary()
	if m.Identity == s.local.Identity || m.Participant.SigningKey == s.local.Participant.SigningKey || bytes.Equal(a, b) {
		return ErrProtocol
	}
	s.verifiedKeys[int(other(s.role))-1] = key
	s.aggregate, err = shuffle.AggregateKeys(s.verifiedKeys[:])
	if err != nil {
		return err
	}
	s.peer = &m
	s.nextReceive++
	return nil
}
func (s *setupState) admitShuffle(ctx context.Context, c Config, m Message, final bool) error {
	binding, err := s.shuffleContext(final)
	if err != nil {
		return err
	}
	protocol, err := shuffle.New()
	if err != nil {
		return err
	}
	if !final {
		if m.Kind != InitialShuffle || m.Sender != covenant.Player2 {
			return ErrProtocol
		}
		verified, err := protocol.VerifyInitial(ctx, s.aggregate, *m.Deck, *m.ShuffleProof, binding)
		if err != nil {
			return err
		}
		s.initial = &m
		s.verifiedInitial = verified
		return nil
	}
	if m.Kind != FinalShuffle || m.Sender != covenant.Player1 {
		return ErrProtocol
	}
	verified, err := protocol.Verify(ctx, s.aggregate, s.verifiedInitial, *m.Deck, *m.ShuffleProof, binding)
	if err != nil {
		return err
	}
	cards := [9]shuffle.MaskedCard{}
	for i := range cards {
		cards[i], err = verified.Card(i)
		if err != nil {
			return err
		}
		if _, err := cards[i].AffineBytes(); err != nil {
			return err
		}
	}
	keys := s.orderedKeys()
	t := s.invitation.Terms
	params := covenant.Params{EmulatorSigningKey: c.Wallet.EmulatorSigningKey, ArkSigningKey: c.Wallet.ArkSigningKey,
		Players: covenant.PerPlayer[covenant.Participant]{Player1: *keys[0].Participant, Player2: *keys[1].Participant},
		Stake:   t.Stake, Bond: t.Bond, MinBet: t.MinBet, MaxWager: t.MaxWager, InitialDeadline: m.InitialDeadline,
		EncryptedDeal: covenant.DealtCards[shuffle.MaskedCard]{HoleCards: covenant.PerPlayer[[2]shuffle.MaskedCard]{Player1: [2]shuffle.MaskedCard{cards[0], cards[1]}, Player2: [2]shuffle.MaskedCard{cards[2], cards[3]}}, Flop: [3]shuffle.MaskedCard{cards[4], cards[5], cards[6]}, Turn: cards[7], River: cards[8]}}
	contract, err := covenant.Derive(params, c.Wallet.CheckpointScript)
	if err != nil {
		return err
	}
	s.final = &m
	s.params = &params
	s.contract = contract
	return nil
}
