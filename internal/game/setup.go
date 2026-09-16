package game

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/shuffle"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

type SessionID [32]byte
type Terms struct{ Stake, Bond, MinBet, MaxWager int64 }

// Setup must allow time for final-proof verification, relay delivery and both
// funding transactions. Live actions keep covenant.DeadlineInterval increments.
const initialFundingTimeout = 5 * 60

// Invitation commits exact relay bytes, shared service policy, offered terms and
// a random bearer capability. It contains neither wallet nor session secrets.
type Invitation struct {
	SessionID           SessionID
	Terms               Terms
	RelayURL            string
	CreatorTransportKey [32]byte
	ServiceBinding      [32]byte
	JoinCapability      [32]byte
}

// Copies share destruction. The wallet key is never an argument or a field.
// Go cannot guarantee erasure of temporary compiler/GC copies.
type SessionSecrets struct{ state *sessionSecretState }
type sessionSecretState struct {
	mu                             sync.Mutex
	shuffleSecret, transportSecret [32]byte // shuffle LE; BIP-340 transport BE
	ownership                      shuffle.OwnershipProof
	destroyed                      bool
}

func (s SessionSecrets) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "<game session secrets>")
}
func (s *SessionSecrets) Destroy() error {
	if s == nil || s.state == nil {
		return nil
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	clear(s.state.shuffleSecret[:])
	clear(s.state.transportSecret[:])
	s.state.destroyed = true
	runtime.KeepAlive(s)
	return nil
}

type MessageKind uint8

const (
	KeyOffer       MessageKind = iota + 1 // P2, sequence 0
	KeyReply                              // P1, sequence 0
	InitialShuffle                        // P2, sequence 1
	FinalShuffle                          // P1, sequence 1; also carries the initial deadline
)

// Transport authenticates Identity before delivery. The reducer admits session,
// role, identity, sequence and proofs, without verifying message signatures.
type Message struct {
	SessionID   SessionID
	Sequence    uint64
	Sender      covenant.Player
	Identity    [32]byte
	Kind        MessageKind
	Participant *covenant.Participant
	Ownership   *shuffle.OwnershipProof
	// Opaque wallet-key ownership proof, authenticated by the wallet/transport
	// boundary. The FSM never checks signature presence, shape or validity.
	WalletOwnership []byte
	Deck            *shuffle.Deck
	ShuffleProof    *shuffle.ShuffleProof
	InitialDeadline covenant.UnixSeconds
}

const maxMoney int64 = 21_000_000 * 100_000_000

// Validate checks amounts for both game admission and application defaults.
func (t Terms) Validate() error {
	for _, n := range []int64{t.Stake, t.Bond, t.MinBet, t.MaxWager} {
		if n <= 0 || n > maxMoney {
			return ErrAmount
		}
	}
	if t.MinBet > t.MaxWager || (t.Stake+t.Bond+t.MaxWager)*2 > maxMoney {
		return ErrAmount
	}
	return nil
}

// Match the reference's bounded invitation admission, leaving full URL parsing
// to the socket boundary. Do not normalize paths, queries, ports or case.
func validateRelay(url string) error {
	if !utf8.ValidString(url) || len(url) == 0 || len(url) > 2048 {
		return ErrInvitation
	}
	rest, ok := strings.CutPrefix(url, "wss://")
	if !ok {
		rest, ok = strings.CutPrefix(url, "ws://")
	}
	if !ok {
		return ErrInvitation
	}
	end := strings.IndexAny(rest, "/?")
	if end < 0 {
		end = len(rest)
	}
	if end == 0 || rest[0] == ':' || strings.Contains(rest[:end], "@") || strings.ContainsAny(url, "#\\") {
		return ErrInvitation
	}
	for _, r := range url {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ErrInvitation
		}
	}
	return nil
}

func randomTransport(ctx context.Context, entropy io.Reader) ([32]byte, [32]byte, error) {
	var secret, public [32]byte
	for {
		if err := ctx.Err(); err != nil {
			clear(secret[:])
			return secret, public, err
		}
		if _, err := io.ReadFull(entropy, secret[:]); err != nil {
			clear(secret[:])
			return secret, public, err
		}
		var scalar btcec.ModNScalar
		if scalar.SetByteSlice(secret[:]) || scalar.IsZero() {
			scalar.Zero()
			continue
		}
		key := btcec.PrivKeyFromScalar(&scalar)
		scalar.Zero()
		copy(public[:], schnorr.SerializePubKey(key.PubKey()))
		key.Zero()
		return secret, public, nil
	}
}

func CreateInvitation(ctx context.Context, entropy io.Reader, config Config, terms Terms, relayURL string) (Invitation, *SessionSecrets, error) {
	if ctx == nil {
		return Invitation{}, nil, ErrInput
	}
	if err := config.validate(); err != nil {
		return Invitation{}, nil, err
	}
	if err := terms.Validate(); err != nil {
		return Invitation{}, nil, err
	}
	if err := validateRelay(relayURL); err != nil {
		return Invitation{}, nil, err
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	transport, public, err := randomTransport(ctx, entropy)
	defer clear(transport[:])
	if err != nil {
		return Invitation{}, nil, err
	}
	binding, err := serviceBinding(config)
	if err != nil {
		return Invitation{}, nil, err
	}
	inv := Invitation{Terms: terms, RelayURL: relayURL, CreatorTransportKey: public, ServiceBinding: binding}
	if _, err := io.ReadFull(entropy, inv.JoinCapability[:]); err != nil {
		return Invitation{}, nil, err
	}
	inv.SessionID, err = invitationID(inv)
	if err != nil {
		return Invitation{}, nil, err
	}
	secrets, err := generateSession(ctx, entropy, config, inv, covenant.Player1, transport, public)
	if err != nil {
		return Invitation{}, nil, err
	}
	return inv, secrets, nil
}
func AcceptInvitation(ctx context.Context, entropy io.Reader, config Config, inv Invitation) (*SessionSecrets, error) {
	if ctx == nil {
		return nil, ErrInput
	}
	if err := checkInvitation(config, inv); err != nil {
		return nil, err
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	transport, public, err := randomTransport(ctx, entropy)
	defer clear(transport[:])
	if err != nil {
		return nil, err
	}
	return generateSession(ctx, entropy, config, inv, covenant.Player2, transport, public)
}
func checkInvitation(config Config, inv Invitation) error {
	if err := config.validate(); err != nil {
		return err
	}
	id, err := invitationID(inv)
	if err != nil {
		return err
	}
	binding, err := serviceBinding(config)
	if err != nil {
		return err
	}
	if id != inv.SessionID || binding != inv.ServiceBinding {
		return ErrInvitation
	}
	return nil
}
func generateSession(ctx context.Context, entropy io.Reader, config Config, inv Invitation, role covenant.Player, transport, identity [32]byte) (*SessionSecrets, error) {
	p := covenant.Participant{SigningKey: config.Wallet.WalletPublicKey, PayoutScript: bytes.Clone(config.Wallet.Receive.Script)}
	binding, err := ownershipContext(inv, role, identity, p)
	if err != nil {
		return nil, err
	}
	secret, _, proof, err := shuffle.GenerateKey(ctx, entropy, binding)
	if err != nil {
		return nil, err
	}
	defer secret.Destroy()
	b, err := secret.MarshalBinary()
	if err != nil {
		return nil, err
	}
	defer clear(b)
	s := &SessionSecrets{state: &sessionSecretState{transportSecret: transport, ownership: proof}}
	copy(s.state.shuffleSecret[:], b)
	if _, err := s.KeyMessage(config, inv, role); err != nil {
		_ = s.Destroy()
		return nil, err
	}
	return s, nil
}

// KeyMessage returns the exact key offer created with these session secrets.
// No entropy or signing is repeated after restoration.
func (s *SessionSecrets) KeyMessage(config Config, inv Invitation, role covenant.Player) (Message, error) {
	if err := checkInvitation(config, inv); err != nil {
		return Message{}, err
	}
	b, err := s.MarshalBinary()
	if err != nil {
		return Message{}, err
	}
	defer clear(b)
	sk, err := shuffle.DecodeSecretKey(b[2:34])
	if err != nil {
		return Message{}, err
	}
	defer sk.Destroy()
	key, err := sk.PublicKey()
	if err != nil {
		return Message{}, err
	}
	tk, _ := btcec.PrivKeyFromBytes(b[34:66])
	defer tk.Zero()
	var identity [32]byte
	copy(identity[:], schnorr.SerializePubKey(tk.PubKey()))
	proof, err := shuffle.DecodeOwnershipProof(b[66:])
	if err != nil {
		return Message{}, err
	}
	kind := KeyOffer
	if role == covenant.Player1 {
		kind = KeyReply
	}
	m := Message{SessionID: inv.SessionID, Sender: role, Identity: identity, Kind: kind,
		Participant: &covenant.Participant{SigningKey: config.Wallet.WalletPublicKey, EncryptionKey: key, PayoutScript: bytes.Clone(config.Wallet.Receive.Script)}, Ownership: &proof}
	_, err = validateKeys(config, inv, m, role, &identity)
	if err != nil {
		return Message{}, err
	}
	return m, nil
}
func (s *SessionSecrets) MarshalBinary() ([]byte, error) {
	if s == nil || s.state == nil {
		return nil, ErrSecrets
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.state.destroyed {
		return nil, ErrSecrets
	}
	p, err := s.state.ownership.MarshalBinary()
	if err != nil {
		return nil, err
	}
	b := []byte{1, 0}
	b = append(b, s.state.shuffleSecret[:]...)
	b = append(b, s.state.transportSecret[:]...)
	return append(b, p...), nil
}
func DecodeSessionSecrets(data []byte) (*SessionSecrets, error) {
	if len(data) != 131 || data[0] != 1 || data[1] != 0 {
		return nil, ErrEncoding
	}
	sk, err := shuffle.DecodeSecretKey(data[2:34])
	if err != nil {
		return nil, ErrSecrets
	}
	defer sk.Destroy()
	var scalar btcec.ModNScalar
	defer scalar.Zero()
	if scalar.SetByteSlice(data[34:66]) || scalar.IsZero() {
		return nil, ErrSecrets
	}
	proof, err := shuffle.DecodeOwnershipProof(data[66:])
	if err != nil {
		return nil, err
	}
	s := &SessionSecrets{state: &sessionSecretState{ownership: proof}}
	copy(s.state.shuffleSecret[:], data[2:34])
	copy(s.state.transportSecret[:], data[34:66])
	return s, nil
}
