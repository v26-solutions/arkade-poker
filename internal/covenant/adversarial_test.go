package covenant

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"arkade-poker/go/internal/merkel"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

func mutateActionPackets(t *testing.T, b *Unsigned, edit func(extension.Extension)) {
	t.Helper()
	e, err := extension.NewExtensionFromTx(b.Ark.UnsignedTx)
	if err != nil {
		t.Fatal(err)
	}
	edit(e)
	out, err := e.TxOut()
	if err != nil {
		t.Fatal(err)
	}
	b.Ark.UnsignedTx.TxOut[len(b.Ark.UnsignedTx.TxOut)-2] = out
}
func mutateEntry(t *testing.T, b *Unsigned, edit func(*arkade.EmulatorEntry)) {
	t.Helper()
	mutateActionPackets(t, b, func(e extension.Extension) {
		for i, p := range e {
			if p.Type() == arkade.PacketType {
				data, _ := p.Serialize()
				entries, err := arkade.DeserializeEmulatorPacket(data)
				if err != nil {
					t.Fatal(err)
				}
				edit(&entries[0])
				e[i] = entries
				return
			}
		}
		t.Fatal("missing entry")
	})
}
func requireVMError(t *testing.T, err error, codes ...txscript.ErrorCode) {
	t.Helper()
	var e txscript.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected emulator rejection, got %v", err)
	}
	for _, code := range codes {
		if e.ErrorCode == code {
			return
		}
	}
	t.Fatalf("unexpected emulator reason %v: %v", e.ErrorCode, err)
}

func TestLiveProgramsRejectTransactionTampering(t *testing.T) {
	h := newTestHand(t, nil)
	h.start(0)
	build := func() *Unsigned {
		b, err := h.c.Bet(h.tx, BettingAction{Kind: Check}, Funding{})
		if err != nil {
			t.Fatal(err)
		}
		if err := executeKnownBundle(h.c, b, h.known); err != nil {
			t.Fatal(err)
		}
		return b
	}
	edits := map[string]func(*Unsigned){
		"covenant value":       func(b *Unsigned) { b.Ark.UnsignedTx.TxOut[0].Value-- },
		"covenant destination": func(b *Unsigned) { b.Ark.UnsignedTx.TxOut[0].PkScript[2] ^= 1 },
		"deadline": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) {
				p := e.GetPacketByType(typeState).(extension.UnknownPacket)
				binary.LittleEndian.PutUint64(p.Data[52:], 999)
			})
		},
		"five-minute deadline increment": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) {
				p := e.GetPacketByType(typeState).(extension.UnknownPacket)
				binary.LittleEndian.PutUint64(p.Data[offDeadline:], uint64(h.state().Deadline)+300)
			})
		},
		"version": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) { e.GetPacketByType(typeState).(extension.UnknownPacket).Data[0] = 2 })
		},
		"agreement": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) { e.GetPacketByType(typeState).(extension.UnknownPacket).Data[2] ^= 1 })
		},
		"actor": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) { e.GetPacketByType(typeState).(extension.UnknownPacket).Data[34] = 0x0a })
		},
		"history": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) { e.GetPacketByType(typeState).(extension.UnknownPacket).Data[35] = 1 })
		},
		"opponent wager": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) { e.GetPacketByType(typeState).(extension.UnknownPacket).Data[44] = 1 })
		},
		"unsigned wager": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) {
				binary.LittleEndian.PutUint64(e.GetPacketByType(typeState).(extension.UnknownPacket).Data[36:], math.MaxUint64)
			})
		},
		"opening share preservation": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) { e.GetPacketByType(typeOpening).(extension.UnknownPacket).Data[0] ^= 1 })
		},
		"P1 share preservation": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) { e.GetPacketByType(typePlayer1).(extension.UnknownPacket).Data[447] ^= 1 })
		},
		"P2 share preservation": func(b *Unsigned) {
			mutateActionPackets(t, b, func(e extension.Extension) { e.GetPacketByType(typePlayer2).(extension.UnknownPacket).Data[447] ^= 1 })
		},
		"unexpected witness": func(b *Unsigned) {
			mutateEntry(t, b, func(e *arkade.EmulatorEntry) { e.Witness = wire.TxWitness{[]byte{1}} })
		},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			b := build()
			edit(b)
			requireVMError(t, executeKnownBundle(h.c, b, h.known), txscript.ErrEqualVerify, txscript.ErrNumEqualVerify, txscript.ErrVerify)
		})
	}
}

func TestRevealProgramsRejectInvalidProofs(t *testing.T) {
	h := newTestHand(t, nil)
	h.accept(h.c.InitialDeposit(h.funding(150)))
	funding := h.funding(150)
	reveals := h.reveal(RevealOpponentHoles, Player1)
	build := func() *Unsigned {
		b, err := h.c.Player1FundingAndReveal(h.tx, funding, reveals)
		if err != nil {
			t.Fatal(err)
		}
		if err := executeKnownBundle(h.c, b, h.known); err != nil {
			t.Fatal(err)
		}
		return b
	}
	for name, edit := range map[string]func(*arkade.EmulatorEntry){
		"response":              func(e *arkade.EmulatorEntry) { e.Witness[1][128] ^= 1 },
		"proof order":           func(e *arkade.EmulatorEntry) { e.Witness[0], e.Witness[1] = e.Witness[1], e.Witness[0] },
		"truncated proof":       func(e *arkade.EmulatorEntry) { e.Witness[1] = e.Witness[1][:159] },
		"response above order":  func(e *arkade.EmulatorEntry) { copy(e.Witness[1][128:], bytes.Repeat([]byte{255}, 32)) },
		"commitment coordinate": func(e *arkade.EmulatorEntry) { e.Witness[1][32] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			b := build()
			mutateEntry(t, b, edit)
			requireVMError(t, executeKnownBundle(h.c, b, h.known), txscript.ErrEqualVerify, txscript.ErrNumEqualVerify, txscript.ErrInvalidStackOperation)
		})
	}
	b := build()
	mutateActionPackets(t, b, func(e extension.Extension) { e.GetPacketByType(typeOpening).(extension.UnknownPacket).Data[128] ^= 1 })
	requireVMError(t, executeKnownBundle(h.c, b, h.known), txscript.ErrEqualVerify, txscript.ErrInvalidStackOperation)
	// A valid proof for a different semantic slot is invalid at this slot.
	wrong := reveals
	wrong.Holes[0] = fixtureReveal(t, h.c.params, h.c.id, Player1, 3, h.c.params.EncryptedDeal.HoleCards.Player2[0])
	b, err := h.c.Player1FundingAndReveal(h.tx, funding, wrong)
	if err != nil {
		t.Fatal(err)
	}
	requireVMError(t, executeKnownBundle(h.c, b, h.known), txscript.ErrEqualVerify)
}

func TestBuildersAdmissionBoundaries(t *testing.T) {
	h := newTestHand(t, nil)
	h.start(10)
	for _, a := range []BettingAction{{Kind: Check}, {Kind: RaiseTo, Amount: 10}, {Kind: RaiseTo, Amount: 19}, {Kind: RaiseTo, Amount: 501}, {Kind: RaiseTo, Amount: -1}, {Kind: Call, Amount: 1}, {Kind: 99}} {
		if _, err := h.c.Bet(h.tx, a, Funding{}); err == nil {
			t.Fatalf("invalid bet accepted %+v", a)
		}
	}
	// A short raise is legal only at the cap, after the opponent opened 490.
	h2 := newTestHand(t, nil)
	h2.start(490)
	h2.bet(BettingAction{Kind: RaiseTo, Amount: 500})
	s := h2.state()
	h2.accept(h2.c.AllInCall(h2.tx, h2.funding(10), h2.reveal(RevealRemainingFromFlop, s.Phase.Actor)))
	if _, err := h.c.Bet(h.tx, BettingAction{Kind: Call}, Funding{}); err == nil {
		t.Fatal("unfunded call accepted")
	}
	h.bet(BettingAction{Kind: Call})
	if _, err := h.c.BoardReveal(h.tx, h.reveal(RevealTurn, h.state().Phase.Actor)); err == nil {
		t.Fatal("wrong reveal shape accepted")
	}
	r := h.reveal(RevealFlop, h.state().Phase.Actor)
	r.Turn = r.Flop[0]
	if _, err := h.c.BoardReveal(h.tx, r); err == nil {
		t.Fatal("irrelevant witness field accepted")
	}
	if _, err := h.c.Concession(h.tx); err == nil {
		t.Fatal("board concession accepted")
	}
	if _, err := h.c.AllInCall(h.tx, Funding{}, RevealWitness{}); err == nil {
		t.Fatal("wrong-phase call accepted")
	}
	if _, err := h.c.ShowdownReveal(h.tx, RevealWitness{}); err == nil {
		t.Fatal("wrong-phase showdown reveal accepted")
	}
	if _, err := h.c.AllInReveal(h.tx, RevealWitness{}); err == nil {
		t.Fatal("wrong-phase all-in reveal accepted")
	}
	if _, err := h.c.Showdown(h.tx, Player1, ShowdownWitness{}); err == nil {
		t.Fatal("premature showdown accepted")
	}
	// Initial deadline has no derivation ceiling; only live advancement overflows.
	params := cryptoParams(t)
	params.InitialDeadline = math.MaxUint64
	c, err := Derive(params, h.c.checkpointScript)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := c.InitialDeposit(h.funding(150))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Player1FundingAndReveal(initial.Ark.UnsignedTx, h.funding(150), RevealWitness{}); err == nil {
		t.Fatal("invalid initial reveal accepted")
	}
	h3 := newTestHand(t, nil)
	h3.c = c
	h3.accept(c.InitialDeposit(h3.funding(150)))
	if _, err := c.Player1FundingAndReveal(h3.tx, h3.funding(150), h3.reveal(RevealOpponentHoles, Player1)); err == nil {
		t.Fatal("deadline overflow accepted")
	}
	if _, err := c.Timeout(h3.tx); err != nil {
		t.Fatalf("deadline overflow blocked terminal construction: %v", err)
	}
	// Multiple wallet sources still have exactly one exact positive remainder.
	h4 := newTestHand(t, nil)
	f1, f2 := h4.funding(80), h4.funding(100)
	f := Funding{Inputs: append(f1.Inputs, f2.Inputs...), Change: wire.NewTxOut(30, []byte{txscript.OP_TRUE})}
	h4.accept(h4.c.InitialDeposit(f))
	if len(h4.tx.TxOut) != 4 || h4.tx.TxOut[1].Value != 30 {
		t.Fatal("split funding layout")
	}
}

func TestShowdownWinnersAndTamperRejection(t *testing.T) {
	deals := [][9]byte{{48, 49, 0, 1, 50, 8, 16, 24, 32}, {0, 1, 48, 49, 50, 8, 16, 24, 32}, {0, 1, 2, 3, 4, 5, 6, 7, 8}}
	for index, deal := range deals {
		for _, submitter := range []Player{Player1, Player2} {
			h := newTestHand(t, nil)
			c, err := Derive(cryptoParamsForDeal(t, deal), h.c.checkpointScript)
			if err != nil {
				t.Fatal(err)
			}
			h.c = c
			h.start(500)
			h.accept(c.AllInCall(h.tx, h.funding(500), h.reveal(RevealRemainingFromFlop, Player1)))
			h.accept(c.AllInReveal(h.tx, h.reveal(RevealRemainingFromFlop, Player2)))
			evaluation, err := merkel.Evaluate(context.Background(), deal)
			if err != nil {
				t.Fatal(err)
			}
			wantWinner := 0
			if index < 2 {
				wantWinner = index + 1
			}
			if evaluation.Winner != wantWinner {
				t.Fatalf("fixture expected winner %d, got %d", wantWinner, evaluation.Winner)
			}
			cards := DealtCards[byte]{HoleCards: PerPlayer[[2]byte]{[2]byte{deal[0], deal[1]}, [2]byte{deal[2], deal[3]}}, Flop: [3]byte{deal[4], deal[5], deal[6]}, Turn: deal[7], River: deal[8]}
			witness := ShowdownWitness{Cards: cards, RankProofs: PerPlayer[merkel.HandProof]{evaluation.Player1, evaluation.Player2}}
			b, err := c.Showdown(h.tx, submitter, witness)
			if err != nil {
				t.Fatal(err)
			}
			if err := executeKnownBundle(c, b, h.known); err != nil {
				t.Fatal(err)
			}
			want := [2]int64{650, 650}
			if wantWinner != 0 {
				want = [2]int64{50, 50}
				want[wantWinner-1] = 1250
			}
			if b.Ark.UnsignedTx.TxOut[0].Value != want[0] || b.Ark.UnsignedTx.TxOut[1].Value != want[1] {
				t.Fatal("wrong showdown payouts")
			}
			if _, err := c.Timeout(h.tx); err == nil {
				t.Fatal("evaluation timeout accepted")
			}
			if _, err := c.Concession(h.tx); err == nil {
				t.Fatal("evaluation concession accepted")
			}
			if _, err := c.Showdown(h.tx, Player(3), witness); err == nil {
				t.Fatal("invalid submitter accepted")
			}
			for name, edit := range map[string]func(*arkade.EmulatorEntry){
				"rank proof":     func(e *arkade.EmulatorEntry) { e.Witness[1][0] ^= 1 },
				"rank range":     func(e *arkade.EmulatorEntry) { e.Witness[0] = []byte{0, 0} },
				"proof length":   func(e *arkade.EmulatorEntry) { e.Witness[2] = e.Witness[2][:447] },
				"card plaintext": func(e *arkade.EmulatorEntry) { e.Witness[6][0] = (e.Witness[6][0] + 1) % 52 },
				"card range":     func(e *arkade.EmulatorEntry) { e.Witness[6][0] = 52 },
				"card duplicate": func(e *arkade.EmulatorEntry) { e.Witness[6][0] = e.Witness[6][1] },
			} {
				t.Run(name, func(t *testing.T) {
					bad, err := c.Showdown(h.tx, submitter, witness)
					if err != nil {
						t.Fatal(err)
					}
					mutateEntry(t, bad, edit)
					requireVMError(t, executeKnownBundle(c, bad, h.known), txscript.ErrEqualVerify, txscript.ErrNumEqualVerify, txscript.ErrVerify)
				})
			}
		}
	}
}
