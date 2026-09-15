package covenant

import (
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

func TestPhaseTable(t *testing.T) {
	// Explicit reference table; the hole at 1b is deliberately unassigned.
	want := []Phase{
		{Kind: AwaitPlayer1FundingAndReveal}, {Kind: AwaitPlayer2RevealAndOpening},
		{Kind: Betting, Street: PreFlop, Actor: Player1}, {Kind: Betting, Street: PreFlop, Actor: Player2},
		{Kind: Betting, Street: Flop, Actor: Player1}, {Kind: Betting, Street: Flop, Actor: Player2},
		{Kind: Betting, Street: Turn, Actor: Player1}, {Kind: Betting, Street: Turn, Actor: Player2},
		{Kind: Betting, Street: River, Actor: Player1}, {Kind: Betting, Street: River, Actor: Player2},
		{Kind: BoardReveal, Street: Flop, Pass: FirstPass, Actor: Player1}, {Kind: BoardReveal, Street: Flop, Pass: FirstPass, Actor: Player2},
		{Kind: BoardReveal, Street: Flop, Pass: SecondPass, Actor: Player1}, {Kind: BoardReveal, Street: Flop, Pass: SecondPass, Actor: Player2},
		{Kind: BoardReveal, Street: Turn, Pass: FirstPass, Actor: Player1}, {Kind: BoardReveal, Street: Turn, Pass: FirstPass, Actor: Player2},
		{Kind: BoardReveal, Street: Turn, Pass: SecondPass, Actor: Player1}, {Kind: BoardReveal, Street: Turn, Pass: SecondPass, Actor: Player2},
		{Kind: BoardReveal, Street: River, Pass: FirstPass, Actor: Player1}, {Kind: BoardReveal, Street: River, Pass: FirstPass, Actor: Player2},
		{Kind: BoardReveal, Street: River, Pass: SecondPass, Actor: Player1}, {Kind: BoardReveal, Street: River, Pass: SecondPass, Actor: Player2},
		{Kind: ShowdownReveal, Pass: FirstPass, Actor: Player1}, {Kind: ShowdownReveal, Pass: FirstPass, Actor: Player2},
		{Kind: ShowdownReveal, Pass: SecondPass, Actor: Player1}, {Kind: ShowdownReveal, Pass: SecondPass, Actor: Player2},
		{Kind: ShowdownEvaluation}, {},
		{Kind: AllIn, Street: PreFlop, Actor: Player1}, {Kind: AllIn, Street: PreFlop, Actor: Player2},
		{Kind: AllIn, Street: Flop, Actor: Player1}, {Kind: AllIn, Street: Flop, Actor: Player2},
		{Kind: AllIn, Street: Turn, Actor: Player1}, {Kind: AllIn, Street: Turn, Actor: Player2},
		{Kind: AllIn, Street: River, Actor: Player1}, {Kind: AllIn, Street: River, Actor: Player2},
		{Kind: AllInReveal, Street: Flop, Actor: Player1}, {Kind: AllInReveal, Street: Flop, Actor: Player2},
		{Kind: AllInReveal, Street: Turn, Actor: Player1}, {Kind: AllInReveal, Street: Turn, Actor: Player2},
		{Kind: AllInReveal, Street: River, Actor: Player1}, {Kind: AllInReveal, Street: River, Actor: Player2},
	}
	for i := range 256 {
		got, err := DecodePhase(byte(i))
		if i == 0x1b || i >= len(want) {
			if err == nil {
				t.Fatalf("unassigned phase %x accepted", i)
			}
			continue
		}
		if err != nil || got != want[i] {
			t.Fatalf("phase %x: %+v %v", i, got, err)
		}
		b, err := got.Encode()
		if err != nil || b != byte(i) {
			t.Fatalf("encode phase %x: %x %v", i, b, err)
		}
		actor, err := got.RequiredActor()
		expectedActor := got.Actor
		if i == 0 {
			expectedActor = Player1
		}
		if i == 1 {
			expectedActor = Player2
		}
		if err != nil || actor != expectedActor {
			t.Fatalf("actor for phase %x: %d %v", i, actor, err)
		}
	}
	// Ensure redundant semantic fields cannot introduce alternative encodings.
	for kind := PhaseKind(0); kind <= ShowdownEvaluation+1; kind++ {
		for actor := Player(0); actor <= Player2+1; actor++ {
			for street := Street(0); street <= River+1; street++ {
				for pass := RevealPass(0); pass <= SecondPass+1; pass++ {
					p := Phase{kind, actor, street, pass}
					if b, err := p.Encode(); err == nil {
						if int(b) >= len(want) || want[b] != p {
							t.Fatalf("noncanonical phase accepted: %+v", p)
						}
					}
				}
			}
		}
	}
}

func TestStatePayloadLayoutAndTransport(t *testing.T) {
	s := State{Phase: Phase{Kind: AwaitPlayer1FundingAndReveal}, LastAction: LastRaise, Wagers: PerPlayer[uint64]{math.MaxUint64, uint64(maxMoney) + 1}, Deadline: math.MaxUint64}
	for i := range s.ContractID {
		s.ContractID[i] = byte(i)
	}
	for slot := range s.Reveals {
		for j := range s.Reveals[slot] {
			s.Reveals[slot][j] = byte(slot*64 + j)
		}
	}
	p, err := EncodeState(s)
	if err != nil {
		t.Fatal(err)
	}
	header, _ := p[0].Serialize()
	if len(header) != 60 || !bytes.Equal(header[:2], []byte{1, 0}) || !bytes.Equal(header[2:34], s.ContractID[:]) || header[34] != 0 || header[35] != 2 || binary.LittleEndian.Uint64(header[36:44]) != math.MaxUint64 || binary.LittleEndian.Uint64(header[44:52]) != uint64(maxMoney)+1 || binary.LittleEndian.Uint64(header[52:]) != math.MaxUint64 {
		t.Fatalf("header %x", header)
	}
	for i, slots := range [][]int{{0, 1, 2, 3}, {4, 5, 6, 10, 12, 14, 15}, {7, 8, 9, 11, 13, 16, 17}} {
		data, _ := p[i+1].Serialize()
		for j, slot := range slots {
			if !bytes.Equal(data[j*64:(j+1)*64], s.Reveals[slot][:]) {
				t.Fatalf("packet %d slot %d", i+1, slot)
			}
		}
	}
	ext, err := ActionExtension(&s)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ext.TxOut()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.PkScript[:4], []byte{0x6a, 0x4d, 0xca, 0x04}) {
		t.Fatalf("envelope prefix %x", out.PkScript[:4])
	}
	tx := wire.NewMsgTx(3)
	tx.AddTxOut(out)
	got, err := ReadState(tx)
	if err != nil || *got != s {
		t.Fatalf("transport: %+v %v", got, err)
	}
	got.Reveals[0][0] ^= 1
	again, _ := ReadState(tx)
	if *again != s {
		t.Fatal("decoder aliases transaction")
	}
	// Serialization checks syntax, intentionally not poker snapshot legality.
	for i := range 0x2a {
		if i == 0x1b {
			continue
		}
		s.Phase, _ = DecodePhase(byte(i))
		for action := LastStart; action <= LastRaise; action++ {
			s.LastAction = action
			p, err := EncodeState(s)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeState(p)
			if err != nil || got != s {
				t.Fatalf("phase %x/action %d: %v", i, action, err)
			}
		}
	}
}

func TestStateReaderAdmissionAndLookup(t *testing.T) {
	state := State{Phase: Phase{Kind: AwaitPlayer1FundingAndReveal}}
	valid := func() []extension.Packet {
		p, err := EncodeState(state)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := map[string]func([]extension.Packet) []extension.Packet{
		"missing":   func(p []extension.Packet) []extension.Packet { return p[:3] },
		"nil":       func(p []extension.Packet) []extension.Packet { return append(p, nil) },
		"typed nil": func(p []extension.Packet) []extension.Packet { var n *extension.UnknownPacket; return append(p, n) },
		"duplicate": func(p []extension.Packet) []extension.Packet { return append(p, p[0]) },
		"unknown": func(p []extension.Packet) []extension.Packet {
			return append(p, extension.UnknownPacket{PacketType: 0xff})
		},
		"asset": func(p []extension.Packet) []extension.Packet {
			return append(p, extension.UnknownPacket{PacketType: 0})
		},
		"version": func(p []extension.Packet) []extension.Packet { p[0].(extension.UnknownPacket).Data[0] = 2; return p },
		"reserved phase": func(p []extension.Packet) []extension.Packet {
			p[0].(extension.UnknownPacket).Data[34] = 0x1b
			return p
		},
		"action": func(p []extension.Packet) []extension.Packet { p[0].(extension.UnknownPacket).Data[35] = 3; return p },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeState(mutate(valid())); err == nil {
				t.Fatal("accepted malformed packets")
			}
		})
	}
	for i := range 4 {
		for _, delta := range []int{-1, 1} {
			p := valid()
			d := p[i].(extension.UnknownPacket)
			d.Data = append(d.Data, 0)[:len(d.Data)+delta]
			p[i] = d
			if _, err := DecodeState(p); err == nil {
				t.Fatalf("accepted size %d delta %d", i, delta)
			}
		}
	}
	p := append(valid(), extension.UnknownPacket{PacketType: arkade.PacketType, Data: []byte{0xff}})
	if _, err := DecodeState(p); err != nil {
		t.Fatalf("reader must leave emulator payload opaque: %v", err)
	}
	ext, _ := extension.NewExtensionFromPackets(p...)
	out, _ := ext.TxOut()
	// Lookup policy matches the actual emulator: first extension, arbitrary
	// position/value, nonminimal push, trailing instructions, later match ignored.
	payload := out.PkScript[4:]
	script := []byte{txscript.OP_RETURN, txscript.OP_PUSHDATA4}
	script = binary.LittleEndian.AppendUint32(script, uint32(len(payload)))
	script = append(script, payload...)
	script = append(script, txscript.OP_DROP)
	tx := wire.NewMsgTx(3)
	tx.AddTxOut(wire.NewTxOut(1, []byte{txscript.OP_TRUE}))
	tx.AddTxOut(wire.NewTxOut(42, script))
	tx.AddTxOut(wire.NewTxOut(0, []byte{txscript.OP_RETURN, 4, 'A', 'R', 'K', 0xff}))
	got, err := ReadState(tx)
	if err != nil || *got != state {
		t.Fatalf("lookup: %v", err)
	}
	tx.TxOut[0] = wire.NewTxOut(0, []byte{txscript.OP_RETURN, 4, 'A', 'R', 'K', 0xff})
	if _, err := ReadState(tx); err == nil {
		t.Fatal("malformed first match fell through")
	}
	for _, tx := range []*wire.MsgTx{nil, wire.NewMsgTx(3), {TxOut: []*wire.TxOut{nil}}} {
		if _, err := ReadState(tx); err == nil {
			t.Fatal("accepted missing transaction/extension")
		}
	}
}

func TestTerminalHeaderAndActionExtension(t *testing.T) {
	state := State{Phase: Phase{Kind: Betting, Actor: Player1, Street: PreFlop}}
	p, _ := EncodeState(state)
	h := p[0].(extension.UnknownPacket)
	h.Data[35] = 0xff // terminal exits don't use action or wagers
	ext, _ := extension.NewExtensionFromPackets(h)
	out, _ := ext.TxOut()
	tx := wire.NewMsgTx(3)
	tx.AddTxOut(out)
	got, err := readHeader(tx)
	if err != nil || !bytes.Equal(got, h.Data) {
		t.Fatalf("header-only exit: %v", err)
	}
	if _, err := ReadState(tx); err == nil {
		t.Fatal("header-only is not live state")
	}
	entry := arkade.EmulatorEntry{Vin: 0, Script: []byte{txscript.OP_TRUE}, Witness: wire.TxWitness{[]byte{1, 2}, nil}}
	for _, successor := range []*State{nil, &state} {
		e, err := ActionExtension(successor, entry)
		if err != nil {
			t.Fatal(err)
		}
		wantCount := 1
		if successor != nil {
			wantCount = 5
		}
		if len(e) != wantCount || e[0].Type() != arkade.PacketType {
			t.Fatal("wrong action packet order")
		}
		data, _ := e[0].Serialize()
		packet, err := arkade.DeserializeEmulatorPacket(data)
		if err != nil || len(packet) != 1 || packet[0].Vin != entry.Vin || !bytes.Equal(packet[0].Script, entry.Script) || !reflect.DeepEqual(packet[0].Witness[0], entry.Witness[0]) {
			t.Fatalf("emulator payload: %v", err)
		}
	}
	if _, err := ActionExtension(nil); err == nil {
		t.Fatal("empty terminal action accepted")
	}
	if _, err := ActionExtension(nil, entry, entry); err == nil {
		t.Fatal("duplicate vin accepted")
	}
	entry.Script = make([]byte, arkade.MaxScriptLength+1)
	if _, err := ActionExtension(nil, entry); err == nil {
		t.Fatal("oversized script accepted")
	}
}
