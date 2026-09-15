package covenant

import (
	"encoding/binary"
	"fmt"

	"arkade-poker/go/internal/merkel"
	"arkade-poker/go/internal/shuffle"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/wire"
)

type PhaseKind uint8

const (
	AwaitPlayer1FundingAndReveal PhaseKind = iota + 1
	AwaitPlayer2RevealAndOpening
	Betting
	AllIn
	BoardReveal
	ShowdownReveal
	AllInReveal
	ShowdownEvaluation
)

type RevealPass uint8

const (
	FirstPass RevealPass = iota + 1
	SecondPass
)

// Phase is semantic caller data. Its codec must reject invalid kind/actor/street/
// pass combinations; enum ordinals are not a promise of a wire encoding.
type Phase struct {
	Kind   PhaseKind
	Actor  Player
	Street Street // For AllInReveal: first unrevealed board street.
	Pass   RevealPass
}

type LastBettingAction uint8

const (
	LastStart LastBettingAction = iota
	LastCheck
	LastRaise
)

// RevealSlots holds raw emulator affine x_le || y_le shares. These are not
// validated points. Slot ownership/publication follows authenticated history.
type RevealSlots [18][64]byte

type State struct {
	ContractID ContractID
	Phase      Phase
	LastAction LastBettingAction
	Wagers     PerPlayer[uint64]
	Deadline   UnixSeconds
	Reveals    RevealSlots
}

type CardReveal struct {
	Share shuffle.RevealToken
	Proof shuffle.RevealProof
}

type RevealKind uint8

const (
	RevealOpponentHoles RevealKind = iota + 1
	RevealFlop
	RevealTurn
	RevealRiver
	RevealOwnHoles
	RevealRemainingFromFlop
	RevealRemainingFromTurn
	RevealRemainingFromRiver
)

// RevealWitness retains fixed semantic positions. The selected kind determines
// the required fields; action builders must reject irrelevant nonzero fields.
type RevealWitness struct {
	Kind  RevealKind
	Holes [2]CardReveal
	Flop  [3]CardReveal
	Turn  CardReveal
	River CardReveal
}

type ShowdownWitness struct {
	Cards      DealtCards[byte]
	RankProofs PerPlayer[merkel.HandProof]
}

// State schema v1 retains the reference's explicit phase table and four payloads:
// 20: version u16le, ID[32], phase byte, action byte, two u64le wagers, u64le
// deadline (60 bytes); 21: opening shares (256); 22/23: each player's remaining
// shares (448 each). Points are raw x_le || y_le. Packet framing is upstream ARK
// framing. Decoding is lossless for all u64 values and does not authorize a state.
const (
	packetVersion = 1
	typeState     = 0x20
	typeOpening   = 0x21
	typePlayer1   = 0x22
	typePlayer2   = 0x23
	headerLen     = 60
	offPhase      = 34
	offAction     = 35
	offWager1     = 36
	offWager2     = 44
	offDeadline   = 52
)

var revealLocations = [18]struct{ packet, offset int }{
	{1, 0}, {1, 64}, {1, 128}, {1, 192},
	{2, 0}, {2, 64}, {2, 128}, {3, 0}, {3, 64}, {3, 128},
	{2, 192}, {3, 192}, {2, 256}, {3, 256},
	{2, 320}, {2, 384}, {3, 320}, {3, 384},
}

// Encode returns the assigned phase byte, rejecting irrelevant fields as well
// as invalid enum values. Opening/evaluation phases have no actor field; their
// obligation is obtained through RequiredActor.
func (p Phase) Encode() (byte, error) {
	switch p.Kind {
	case AwaitPlayer1FundingAndReveal, AwaitPlayer2RevealAndOpening, ShowdownEvaluation:
		if p.Actor == 0 && p.Street == 0 && p.Pass == 0 {
			switch p.Kind {
			case AwaitPlayer1FundingAndReveal:
				return 0x00, nil
			case AwaitPlayer2RevealAndOpening:
				return 0x01, nil
			default:
				return 0x1a, nil
			}
		}
	case Betting, AllIn:
		if validPlayer(p.Actor) && p.Street >= PreFlop && p.Street <= River && p.Pass == 0 {
			base := byte(0x02)
			if p.Kind == AllIn {
				base = 0x1c
			}
			return base + 2*byte(p.Street-PreFlop) + byte(p.Actor-Player1), nil
		}
	case BoardReveal:
		if validPlayer(p.Actor) && p.Street >= Flop && p.Street <= River && (p.Pass == FirstPass || p.Pass == SecondPass) {
			return 0x0a + 4*byte(p.Street-Flop) + 2*byte(p.Pass-FirstPass) + byte(p.Actor-Player1), nil
		}
	case ShowdownReveal:
		if validPlayer(p.Actor) && p.Street == 0 && (p.Pass == FirstPass || p.Pass == SecondPass) {
			return 0x16 + 2*byte(p.Pass-FirstPass) + byte(p.Actor-Player1), nil
		}
	case AllInReveal:
		if validPlayer(p.Actor) && p.Street >= Flop && p.Street <= River && p.Pass == 0 {
			return 0x24 + 2*byte(p.Street-Flop) + byte(p.Actor-Player1), nil
		}
	}
	return 0, fmt.Errorf("covenant: invalid phase %+v", p)
}

func DecodePhase(b byte) (Phase, error) {
	switch {
	case b == 0:
		return Phase{Kind: AwaitPlayer1FundingAndReveal}, nil
	case b == 1:
		return Phase{Kind: AwaitPlayer2RevealAndOpening}, nil
	case b >= 0x02 && b <= 0x09:
		return Phase{Kind: Betting, Street: PreFlop + Street((b-2)/2), Actor: Player1 + Player(b%2)}, nil
	case b >= 0x0a && b <= 0x15:
		return Phase{Kind: BoardReveal, Street: Flop + Street((b-0x0a)/4), Pass: FirstPass + RevealPass(((b-0x0a)%4)/2), Actor: Player1 + Player(b%2)}, nil
	case b >= 0x16 && b <= 0x19:
		return Phase{Kind: ShowdownReveal, Pass: FirstPass + RevealPass((b-0x16)/2), Actor: Player1 + Player(b%2)}, nil
	case b == 0x1a:
		return Phase{Kind: ShowdownEvaluation}, nil
	case b >= 0x1c && b <= 0x23:
		return Phase{Kind: AllIn, Street: PreFlop + Street((b-0x1c)/2), Actor: Player1 + Player(b%2)}, nil
	case b >= 0x24 && b <= 0x29:
		return Phase{Kind: AllInReveal, Street: Flop + Street((b-0x24)/2), Actor: Player1 + Player(b%2)}, nil
	default:
		return Phase{}, fmt.Errorf("covenant: unknown phase 0x%02x", b)
	}
}

func validPlayer(p Player) bool { return p == Player1 || p == Player2 }

// RequiredActor returns zero only at fully revealed evaluation, when either
// participant may settle and neither may claim a timeout.
func (p Phase) RequiredActor() (Player, error) {
	if _, err := p.Encode(); err != nil {
		return 0, err
	}
	switch p.Kind {
	case AwaitPlayer1FundingAndReveal:
		return Player1, nil
	case AwaitPlayer2RevealAndOpening:
		return Player2, nil
	case ShowdownEvaluation:
		return 0, nil
	default:
		return p.Actor, nil
	}
}

func EncodeState(state State) ([]extension.Packet, error) {
	phase, err := state.Phase.Encode()
	if err != nil {
		return nil, err
	}
	if state.LastAction > LastRaise {
		return nil, fmt.Errorf("covenant: unknown last action %d", state.LastAction)
	}
	payloads := [4][]byte{make([]byte, headerLen), make([]byte, 256), make([]byte, 448), make([]byte, 448)}
	h := payloads[0]
	binary.LittleEndian.PutUint16(h, packetVersion)
	copy(h[2:offPhase], state.ContractID[:])
	h[offPhase], h[offAction] = phase, byte(state.LastAction)
	binary.LittleEndian.PutUint64(h[offWager1:], state.Wagers.Player1)
	binary.LittleEndian.PutUint64(h[offWager2:], state.Wagers.Player2)
	binary.LittleEndian.PutUint64(h[offDeadline:], uint64(state.Deadline))
	for i, loc := range revealLocations {
		copy(payloads[loc.packet][loc.offset:], state.Reveals[i][:])
	}
	packets := make([]extension.Packet, 4)
	for i, payload := range payloads {
		packets[i] = extension.UnknownPacket{PacketType: typeState + byte(i), Data: payload}
	}
	return packets, nil
}

// pokerRecords admits exactly the four poker types and an optional opaque
// emulator packet. The terminal header reader deliberately does not require
// reveal payloads, a valid last action, or wager history.
func pokerRecords(packets []extension.Packet) ([4][]byte, error) {
	var records [4][]byte
	checked, err := extension.NewExtensionFromPackets(packets...)
	if err != nil {
		return records, fmt.Errorf("covenant: packets: %w", err)
	}
	for _, p := range checked {
		switch t := p.Type(); {
		case t >= typeState && t <= typePlayer2:
			b, err := p.Serialize()
			if err != nil {
				return records, fmt.Errorf("covenant: packet 0x%02x: %w", t, err)
			}
			// Keep a present empty payload distinct from an absent packet.
			records[t-typeState] = append([]byte{}, b...)
		case t == arkade.PacketType:
		default:
			return records, fmt.Errorf("covenant: unsupported packet type 0x%02x", t)
		}
	}
	return records, nil
}

func DecodeState(packets []extension.Packet) (State, error) {
	var s State
	records, err := pokerRecords(packets)
	if err != nil {
		return s, err
	}
	for i, size := range [4]int{headerLen, 256, 448, 448} {
		if records[i] == nil {
			return s, fmt.Errorf("covenant: missing packet 0x%02x", typeState+i)
		}
		if len(records[i]) != size {
			return s, fmt.Errorf("covenant: packet 0x%02x has %d bytes, expected %d", typeState+i, len(records[i]), size)
		}
	}
	h := records[0]
	if v := binary.LittleEndian.Uint16(h); v != packetVersion {
		return s, fmt.Errorf("covenant: unsupported packet version %d", v)
	}
	s.Phase, err = DecodePhase(h[offPhase])
	if err != nil {
		return State{}, err
	}
	if h[offAction] > byte(LastRaise) {
		return State{}, fmt.Errorf("covenant: unknown last action %d", h[offAction])
	}
	s.LastAction = LastBettingAction(h[offAction])
	copy(s.ContractID[:], h[2:offPhase])
	s.Wagers = PerPlayer[uint64]{binary.LittleEndian.Uint64(h[offWager1:]), binary.LittleEndian.Uint64(h[offWager2:])}
	s.Deadline = UnixSeconds(binary.LittleEndian.Uint64(h[offDeadline:]))
	for i, loc := range revealLocations {
		copy(s.Reveals[i][:], records[loc.packet][loc.offset:loc.offset+64])
	}
	return s, nil
}

// ReadState follows the emulator's first ARK extension lookup, including its
// acceptance of nonminimal pushes and trailing script instructions. A terminal
// transaction lacks live state and returns an error. Success is not acceptance,
// source authentication, proof verification, or evidence of share publication.
func ReadState(previous *wire.MsgTx) (*State, error) {
	packets, err := readExtension(previous)
	if err != nil {
		return nil, err
	}
	s, err := DecodeState(packets)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func readExtension(previous *wire.MsgTx) (extension.Extension, error) {
	if previous == nil {
		return nil, fmt.Errorf("covenant: missing predecessor")
	}
	for _, out := range previous.TxOut {
		if out == nil {
			return nil, fmt.Errorf("covenant: nil predecessor output")
		}
	}
	e, err := extension.NewExtensionFromTx(previous)
	if err != nil {
		return nil, fmt.Errorf("covenant: read extension: %w", err)
	}
	return e, nil
}

func readHeader(previous *wire.MsgTx) ([]byte, error) {
	e, err := readExtension(previous)
	if err != nil {
		return nil, err
	}
	records, err := pokerRecords(e)
	if err != nil {
		return nil, err
	}
	if records[0] == nil {
		return nil, fmt.Errorf("covenant: missing poker header")
	}
	return records[0], nil
}

// ActionExtension uses the upstream codecs, placing emulator entries first and
// state packets next. A terminal action has entries and no successor; initial
// deposit has a successor and no entries. Empty extensions are invalid.
func ActionExtension(successor *State, entries ...arkade.EmulatorEntry) (extension.Extension, error) {
	var packets []extension.Packet
	if len(entries) != 0 {
		p, err := arkade.NewPacket(entries...)
		if err != nil {
			return nil, fmt.Errorf("covenant: emulator packet: %w", err)
		}
		var total int
		for i, entry := range entries {
			witnessSize := entry.Witness.SerializeSize()
			if len(entry.Script) > arkade.MaxScriptLength || witnessSize > arkade.MaxWitnessLength {
				return nil, fmt.Errorf("covenant: emulator entry %d exceeds script or witness limit", i)
			}
			total += len(entry.Script) + witnessSize
			if total > arkade.MaxTotalEntrySize {
				return nil, fmt.Errorf("covenant: emulator packet exceeds total entry limit")
			}
		}
		// NewPacket currently checks identity but its parser enforces resource caps.
		// Round-trip here so our encoder cannot produce a packet that parser rejects.
		b, err := p.Serialize()
		if err != nil {
			return nil, err
		}
		bounded, err := arkade.DeserializeEmulatorPacket(b)
		if err != nil {
			return nil, fmt.Errorf("covenant: emulator packet: %w", err)
		}
		packets = append(packets, bounded)
	}
	if successor != nil {
		p, err := EncodeState(*successor)
		if err != nil {
			return nil, err
		}
		packets = append(packets, p...)
	}
	return extension.NewExtensionFromPackets(packets...)
}
