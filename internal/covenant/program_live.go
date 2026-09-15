package covenant

import (
	"fmt"

	"arkade-poker/go/internal/merkel"
	"arkade-poker/go/internal/shuffle"
	"github.com/arkade-os/emulator/pkg/arkade"
)

// emitPlayer1Funding establishes initial zero shares and binds the two new DLEQ proofs.
func emitPlayer1Funding(params Params, id ContractID) ([]byte, error) {
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	deposit := params.Stake + params.Bond
	total := 2 * deposit
	initial := make([]byte, headerLen)
	copy(initial, headerPrefix(id))
	copy(initial[offDeadline:], u64LE(uint64(params.InitialDeadline)))
	successor := append([]byte(nil), initial...)
	successor[offPhase] = 1
	p := newProgram()
	// Admit the contributing covenant input and exactly two proof witnesses.
	p.op(arkade.OP_PUSHCURRENTINPUTINDEX).num(0).op(arkade.OP_NUMEQUALVERIFY)
	// ( -- )
	p.op(arkade.OP_INSPECTNUMINPUTS).num(2).op(arkade.OP_GREATERTHANOREQUAL, arkade.OP_VERIFY)
	// ( -- )
	p.op(arkade.OP_DEPTH).num(2).op(arkade.OP_NUMEQUALVERIFY)
	// ( proof3 proof2 -- proof3 proof2 )
	// Establish the initial state before Player1 adds their money.
	p.num(int64(typeState)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY)
	p.data(initial).op(arkade.OP_EQUALVERIFY)
	p.include(emitInitialReveals())
	// Bind all successor header fields except the deadline, checked below.
	p.num(int64(typeState)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	// ( -- header )
	p.op(arkade.OP_DUP).num(0).num(int64(offDeadline)).op(arkade.OP_SUBSTR).data(successor[:offDeadline]).op(arkade.OP_EQUALVERIFY)
	// ( header -- header )
	// Unsigned arithmetic cannot wrap into an eight-byte successor deadline.
	p.num(int64(offDeadline)).num(8).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
	// ( header -- next_deadline )
	p.data(scriptUnsigned(u64LE(uint64(params.InitialDeadline))))
	// ( next_deadline -- next_deadline deadline )
	p.num(DeadlineInterval).op(arkade.OP_ADD, arkade.OP_NUMEQUALVERIFY)
	// ( next_deadline deadline -- )
	// Every other byte remains equal to the initialized predecessor.
	p.include(emitRevealPreservation([]byte{2, 3}))
	// Preserve the covenant destination and exactly double the initial deposit.
	p.num(0).op(arkade.OP_INSPECTINPUTVALUE).num(deposit).op(arkade.OP_NUMEQUALVERIFY)
	// ( -- )
	p.num(0).op(arkade.OP_INSPECTOUTPUTVALUE).num(total).op(arkade.OP_NUMEQUALVERIFY)
	// ( -- )
	p.num(0).op(arkade.OP_INSPECTINPUTSCRIPTPUBKEY)
	// ( -- prev pv )
	p.num(0).op(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY)
	// ( prev pv -- prev pv next nv )
	p.op(arkade.OP_ROT, arkade.OP_EQUALVERIFY, arkade.OP_EQUALVERIFY)
	// ( prev pv next nv -- )
	// Shares are bound to successor packets; proof slot identities stay fixed.
	p.include(emitRevealProof(params, id, Player1, 2, params.EncryptedDeal.HoleCards.Player2[0]))
	p.include(emitRevealProof(params, id, Player1, 3, params.EncryptedDeal.HoleCards.Player2[1]))
	p.op(arkade.OP_1)
	// ( -- true )
	return p.bytes()
}

// emitPlayer2Opening combines the opponent-hole reveal with the first check/bet.
func emitPlayer2Opening(params Params, id ContractID) ([]byte, error) {
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	total := 2 * (params.Stake + params.Bond)
	minBet, maxWager := params.MinBet, params.MaxWager
	previous := make([]byte, headerLen)
	copy(previous, headerPrefix(id))
	previous[offPhase] = 1
	p := newProgram()
	p.op(arkade.OP_PUSHCURRENTINPUTINDEX).num(0).op(arkade.OP_NUMEQUALVERIFY)
	// ( -- )
	p.op(arkade.OP_DEPTH).num(2).op(arkade.OP_NUMEQUALVERIFY)
	// ( proof1 proof0 -- proof1 proof0 )
	// Authenticate phase 01, Start, zero wagers; save the deadline.
	p.num(int64(typeState)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	// ( -- header )
	p.op(arkade.OP_DUP).num(0).num(int64(offDeadline)).op(arkade.OP_SUBSTR).data(previous[:offDeadline]).op(arkade.OP_EQUALVERIFY)
	// ( header -- header )
	p.num(int64(offDeadline)).num(8).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM, arkade.OP_TOALTSTACK)
	// ( header -- | alt: -- deadline )
	// Preserve version, agreement, and Player1's zero wager.
	p.num(int64(typeState)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	// ( -- header )
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(previous[:offPhase]).op(arkade.OP_EQUALVERIFY)
	// ( header -- header )
	p.op(arkade.OP_DUP).num(int64(offWager1)).num(8).op(arkade.OP_SUBSTR).data(make([]byte, 8)).op(arkade.OP_EQUALVERIFY)
	// ( header -- header )
	// Compare an unwrapped increment with the exact unsigned successor deadline.
	p.unsignedField(0, offDeadline, 8)
	// ( header -- header next_deadline )
	p.op(arkade.OP_FROMALTSTACK).num(DeadlineInterval).op(arkade.OP_ADD, arkade.OP_NUMEQUALVERIFY)
	// ( header next_deadline -- header | alt: deadline -- )
	// Admit only check or min_bet <= b <= max_wager.
	p.unsignedField(0, offWager2, 8)
	// ( header -- header b )
	p.op(arkade.OP_DUP).num(maxWager).op(arkade.OP_LESSTHANOREQUAL, arkade.OP_VERIFY)
	// ( header b -- header b )
	p.op(arkade.OP_DUP).num(0).op(arkade.OP_NUMEQUAL, arkade.OP_OVER).num(minBet).op(arkade.OP_GREATERTHANOREQUAL, arkade.OP_BOOLOR, arkade.OP_VERIFY)
	// ( header b -- header b )
	// is_bet also determines the input layout and last action: Check=1, Raise=2.
	p.op(arkade.OP_DUP).num(0).op(arkade.OP_GREATERTHAN)
	// ( header b -- header b is_bet )
	p.op(arkade.OP_DUP, arkade.OP_INSPECTNUMINPUTS).num(1).op(arkade.OP_GREATERTHAN, arkade.OP_NUMEQUALVERIFY)
	// ( header b is_bet -- header b is_bet )
	p.num(1).op(arkade.OP_ADD).unsignedField(2, offAction, 1).op(arkade.OP_NUMEQUALVERIFY)
	// ( header b is_bet -- header b )
	// Pre-flop Betting(Player1)=02; AllIn(Player1)=1c, exactly at the cap.
	p.op(arkade.OP_DUP).num(maxWager).op(arkade.OP_NUMEQUAL).num(26).op(arkade.OP_MUL).num(2).op(arkade.OP_ADD)
	// ( header b -- header b phase )
	p.unsignedField(2, offPhase, 1).op(arkade.OP_NUMEQUALVERIFY)
	// ( header b phase -- header b )
	// Lock precisely the full opening pot plus b and preserve the covenant script.
	p.num(total).op(arkade.OP_ADD).num(0).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY, arkade.OP_DROP)
	// ( header b -- )
	p.num(0).op(arkade.OP_INSPECTINPUTVALUE).num(total).op(arkade.OP_NUMEQUALVERIFY)
	// ( -- )
	p.num(0).op(arkade.OP_INSPECTINPUTSCRIPTPUBKEY)
	// ( -- prev pv )
	p.num(0).op(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY)
	// ( prev pv -- prev pv next nv )
	p.op(arkade.OP_ROT, arkade.OP_EQUALVERIFY, arkade.OP_EQUALVERIFY)
	// ( prev pv next nv -- )
	// Preserve the other opening shares and both players' remaining packets.
	p.include(emitRevealPreservation([]byte{0, 1}))
	p.include(emitRevealProof(params, id, Player2, 0, params.EncryptedDeal.HoleCards.Player1[0]))
	p.include(emitRevealProof(params, id, Player2, 1, params.EncryptedDeal.HoleCards.Player1[1]))
	p.op(arkade.OP_1)
	// ( -- true )
	return p.bytes()
}

// emitBetting selects check/call/raise from authenticated packet values, without
// a witness selector. Wagers are cumulative, and only a cap raise may be short.
func emitBetting(params Params, id ContractID, street Street, actor Player) ([]byte, error) {
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	if !validPlayer(actor) {
		return nil, fmt.Errorf("covenant: invalid actor")
	}
	if street < PreFlop || street > River {
		return nil, fmt.Errorf("covenant: invalid betting street")
	}
	total := 2 * (params.Stake + params.Bond)
	minBet, maxWager := params.MinBet, params.MaxWager
	betting := int64(2 + 2*(street-PreFlop))
	closing := int64(0x0a + 4*(street-PreFlop))
	player := int64(actor - Player1)
	sourcePhase, nextBetting, nextClosing := betting+player, betting+1-player, closing+1-player
	actorOffset, opponentOffset := int64(36)+8*player, int64(44)-8*player
	leastAction := int64(0)
	if street == PreFlop {
		leastAction = 1 + player
	}
	prefix := headerPrefix(id)
	p := newProgram()
	p.op(arkade.OP_PUSHCURRENTINPUTINDEX).num(0).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DEPTH).num(0).op(arkade.OP_NUMEQUALVERIFY)
	// Authenticate the exact source header and save its unsigned deadline.
	p.num(int64(typeState)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offPhase)).num(1).op(arkade.OP_SUBSTR).data([]byte{byte(sourcePhase)}).op(arkade.OP_EQUALVERIFY)
	p.unsignedField(0, offDeadline, 8).op(arkade.OP_TOALTSTACK)
	p.op(arkade.OP_DUP).num(actorOffset).num(8).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
	p.num(1).op(arkade.OP_PICK).num(opponentOffset).num(8).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
	p.unsignedField(2, offAction, 1)
	p.num(3).op(arkade.OP_ROLL, arkade.OP_DROP)
	// ( header a o l -- a o l )
	// Bind the successor's immutable header fields and opponent wager.
	p.num(int64(typeState)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(opponentOffset).num(8).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
	p.num(3).op(arkade.OP_PICK, arkade.OP_NUMEQUALVERIFY)
	// ( a o l header opp -- a o l header )
	p.unsignedField(0, offDeadline, 8)
	p.op(arkade.OP_FROMALTSTACK).num(DeadlineInterval).op(arkade.OP_ADD, arkade.OP_NUMEQUALVERIFY)
	p.num(actorOffset).num(8).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
	p.op(arkade.OP_SWAP)
	// Frame F = [a o n l].
	// Admit only canonical Start/Check/Raise history for this street/actor.
	p.op(arkade.OP_DUP).num(leastAction).op(arkade.OP_GREATERTHANOREQUAL, arkade.OP_VERIFY)
	p.op(arkade.OP_DUP).num(2).op(arkade.OP_LESSTHANOREQUAL, arkade.OP_VERIFY)
	p.num(3).op(arkade.OP_PICK).num(maxWager).op(arkade.OP_LESSTHAN, arkade.OP_VERIFY)
	p.num(2).op(arkade.OP_PICK).num(maxWager).op(arkade.OP_LESSTHAN, arkade.OP_VERIFY)
	p.op(arkade.OP_DUP).num(2).op(arkade.OP_NUMEQUAL, arkade.OP_IF)
	// Facing a raise: the opponent leads by at least the minimum bet.
	p.num(2).op(arkade.OP_PICK).num(4).op(arkade.OP_PICK, arkade.OP_SUB).num(minBet).op(arkade.OP_GREATERTHANOREQUAL, arkade.OP_VERIFY)
	p.op(arkade.OP_ELSE)
	// Start/Check histories have equal wagers.
	p.num(3).op(arkade.OP_PICK).num(3).op(arkade.OP_PICK, arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_ENDIF)
	// ( F -- F )
	// Bound the new wager, input layout, and both exact covenant values.
	p.num(1).op(arkade.OP_PICK).num(4).op(arkade.OP_PICK, arkade.OP_GREATERTHANOREQUAL, arkade.OP_VERIFY)
	p.num(1).op(arkade.OP_PICK).num(maxWager).op(arkade.OP_LESSTHANOREQUAL, arkade.OP_VERIFY)
	p.num(1).op(arkade.OP_PICK).num(4).op(arkade.OP_PICK, arkade.OP_GREATERTHAN)
	p.op(arkade.OP_INSPECTNUMINPUTS).num(1).op(arkade.OP_GREATERTHAN, arkade.OP_NUMEQUALVERIFY)
	p.num(3).op(arkade.OP_PICK).num(3).op(arkade.OP_PICK, arkade.OP_ADD).num(total).op(arkade.OP_ADD)
	p.num(0).op(arkade.OP_INSPECTINPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(1).op(arkade.OP_PICK).num(3).op(arkade.OP_PICK, arkade.OP_ADD).num(total).op(arkade.OP_ADD)
	p.num(0).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(0).op(arkade.OP_INSPECTINPUTSCRIPTPUBKEY).num(0).op(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY)
	p.op(arkade.OP_ROT, arkade.OP_EQUALVERIFY, arkade.OP_EQUALVERIFY)
	// An exact match checks or closes the street; a larger wager raises.
	p.num(1).op(arkade.OP_PICK).num(3).op(arkade.OP_PICK, arkade.OP_NUMEQUAL, arkade.OP_IF)
	p.op(arkade.OP_DUP).num(0).op(arkade.OP_NUMEQUAL, arkade.OP_IF)
	p.num(nextBetting).num(1)
	// First check: opponent acts, LastAction::Check.
	p.op(arkade.OP_ELSE)
	p.num(nextClosing).num(0)
	// Call/second check: reveal, LastAction::Start.
	p.op(arkade.OP_ENDIF)
	p.op(arkade.OP_ELSE)
	// A partial call is invalid. The raise must meet the previous
	// increment (or min_bet on an unopened street), except at the cap.
	p.num(1).op(arkade.OP_PICK).num(3).op(arkade.OP_PICK, arkade.OP_GREATERTHAN, arkade.OP_VERIFY)
	p.op(arkade.OP_DUP).num(2).op(arkade.OP_NUMEQUAL, arkade.OP_IF)
	p.num(2).op(arkade.OP_PICK).num(4).op(arkade.OP_PICK, arkade.OP_SUB)
	// ( F -- F minimum_increment )
	p.op(arkade.OP_ELSE)
	p.num(minBet)
	p.op(arkade.OP_ENDIF)
	p.num(2).op(arkade.OP_PICK).num(4).op(arkade.OP_PICK, arkade.OP_SUB).op(arkade.OP_SWAP, arkade.OP_GREATERTHANOREQUAL)
	p.num(2).op(arkade.OP_PICK).num(maxWager).op(arkade.OP_NUMEQUAL, arkade.OP_BOOLOR, arkade.OP_VERIFY)
	p.num(1).op(arkade.OP_PICK).num(maxWager).op(arkade.OP_NUMEQUAL, arkade.OP_IF)
	p.num(nextBetting + 26)
	// AllIn(opponent) for this street.
	p.op(arkade.OP_ELSE)
	p.num(nextBetting)
	p.op(arkade.OP_ENDIF)
	p.num(2)
	// LastAction::Raise.
	p.op(arkade.OP_ENDIF)
	// ( F -- F next_phase next_action )
	p.num(int64(typeState)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY)
	p.num(int64(offAction)).num(1).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM, arkade.OP_NUMEQUALVERIFY)
	p.num(int64(typeState)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY)
	p.num(int64(offPhase)).num(1).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM, arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_2DROP, arkade.OP_2DROP)
	// ( F -- )
	p.include(emitRevealPreservation([]byte{}))
	p.op(arkade.OP_1)
	return p.bytes()
}

// emitBoardReveal covers both passes; the first revealer opens the next betting street.
func emitBoardReveal(params Params, id ContractID, street Street, actor Player) ([]byte, error) {
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	if !validPlayer(actor) {
		return nil, fmt.Errorf("covenant: invalid actor")
	}
	if street < Flop || street > River {
		return nil, fmt.Errorf("covenant: invalid board street")
	}
	half, cap := params.Stake+params.Bond, params.MaxWager
	player := byte(actor - Player1)
	firstPhase, bettingPhase := byte(0x0a+4*(street-Flop)), byte(4+2*(street-Flop))
	firstSlot, count := byte(4), byte(3)
	cards := params.EncryptedDeal.Flop[:]
	if street == Turn {
		firstSlot, count = 10, 1
		cards = []shuffle.MaskedCard{params.EncryptedDeal.Turn}
	}
	if street == River {
		firstSlot, count = 12, 1
		cards = []shuffle.MaskedCard{params.EncryptedDeal.River}
	}
	actorStart := firstSlot + player*count
	sourceFirst, nextFirst, nextSecond := int64(firstPhase+player), int64(firstPhase+2+1-player), int64(bettingPhase+1-player)
	prefix := headerPrefix(id)
	p := newProgram()
	p.op(arkade.OP_PUSHCURRENTINPUTINDEX).num(0).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_INSPECTNUMINPUTS).num(1).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DEPTH).num(int64(count)).op(arkade.OP_NUMEQUALVERIFY)
	p.num(int64(typeState)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offAction)).num(1).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_EQUALVERIFY)
	p.unsignedField(0, offPhase, 1)
	p.num(sourceFirst).op(arkade.OP_SUB)
	p.op(arkade.OP_DUP).num(0).op(arkade.OP_NUMEQUAL, arkade.OP_OVER).num(2).op(arkade.OP_NUMEQUAL, arkade.OP_BOOLOR, arkade.OP_VERIFY)
	p.num(2).op(arkade.OP_DIV)
	// ( prev delta -- prev is_second )
	// Decode unsigned equal wagers and bind both exact covenant values.
	p.unsignedField(1, offWager1, 8)
	p.op(arkade.OP_DUP).num(cap).op(arkade.OP_LESSTHAN, arkade.OP_VERIFY)
	p.unsignedField(2, offWager2, 8)
	p.op(arkade.OP_OVER, arkade.OP_NUMEQUALVERIFY)
	// ( prev is_second wager -- prev is_second wager )
	p.num(half).op(arkade.OP_ADD).num(2).op(arkade.OP_MUL)
	p.op(arkade.OP_DUP).num(0).op(arkade.OP_INSPECTINPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(0).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(0).op(arkade.OP_INSPECTINPUTSCRIPTPUBKEY).num(0).op(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY)
	p.op(arkade.OP_ROT, arkade.OP_EQUALVERIFY, arkade.OP_EQUALVERIFY)
	p.num(int64(typeState)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	// ( prev is_second -- prev is_second next )
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offAction)).num(1).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offWager1)).num(16).op(arkade.OP_SUBSTR).num(3).op(arkade.OP_PICK).num(int64(offWager1)).num(16).op(arkade.OP_SUBSTR, arkade.OP_EQUALVERIFY)
	p.unsignedField(0, offDeadline, 8)
	p.unsignedField(3, offDeadline, 8)
	p.num(DeadlineInterval).op(arkade.OP_ADD, arkade.OP_NUMEQUALVERIFY)
	p.num(1).op(arkade.OP_PICK).num(nextSecond - nextFirst).op(arkade.OP_MUL).num(nextFirst).op(arkade.OP_ADD)
	p.unsignedField(1, offPhase, 1).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_2DROP, arkade.OP_DROP)
	// ( prev is_second next -- )
	slots := make([]byte, count)
	for i := range slots {
		slots[i] = actorStart + byte(i)
	}
	p.include(emitRevealPreservation(slots))
	for i, card := range cards {
		p.include(emitRevealProof(params, id, actor, slots[i], card))
	}
	return p.num(1).bytes()
}

// emitShowdownReveal admits capped wagers only for the authenticated second pass.
func emitShowdownReveal(params Params, id ContractID, actor Player) ([]byte, error) {
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	if !validPlayer(actor) {
		return nil, fmt.Errorf("covenant: invalid actor")
	}
	half, cap := params.Stake+params.Bond, params.MaxWager
	player := byte(actor - Player1)
	first, nextFirst := int64(0x16+player), int64(0x18+1-player)
	prefix := headerPrefix(id)
	p := newProgram()
	p.op(arkade.OP_PUSHCURRENTINPUTINDEX).num(0).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_INSPECTNUMINPUTS).num(1).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DEPTH).num(2).op(arkade.OP_NUMEQUALVERIFY)
	p.num(int64(typeState)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offAction)).num(1).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_EQUALVERIFY)
	p.unsignedField(0, offPhase, 1).num(first).op(arkade.OP_SUB)
	p.op(arkade.OP_DUP).num(0).op(arkade.OP_NUMEQUAL, arkade.OP_OVER).num(2).op(arkade.OP_NUMEQUAL, arkade.OP_BOOLOR, arkade.OP_VERIFY)
	p.num(2).op(arkade.OP_DIV)
	// ( prev delta -- prev is_second )
	p.unsignedField(1, offWager1, 8)
	p.op(arkade.OP_DUP).num(cap).op(arkade.OP_LESSTHANOREQUAL, arkade.OP_VERIFY)
	p.op(arkade.OP_DUP).num(cap).op(arkade.OP_LESSTHAN).num(2).op(arkade.OP_PICK, arkade.OP_BOOLOR, arkade.OP_VERIFY)
	p.unsignedField(2, offWager2, 8).op(arkade.OP_OVER, arkade.OP_NUMEQUALVERIFY)
	p.num(half).op(arkade.OP_ADD).num(2).op(arkade.OP_MUL)
	p.op(arkade.OP_DUP).num(0).op(arkade.OP_INSPECTINPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(0).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(0).op(arkade.OP_INSPECTINPUTSCRIPTPUBKEY).num(0).op(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY)
	p.op(arkade.OP_ROT, arkade.OP_EQUALVERIFY, arkade.OP_EQUALVERIFY)
	p.num(int64(typeState)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offAction)).num(1).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offWager1)).num(16).op(arkade.OP_SUBSTR).num(3).op(arkade.OP_PICK).num(int64(offWager1)).num(16).op(arkade.OP_SUBSTR, arkade.OP_EQUALVERIFY)
	p.unsignedField(0, offDeadline, 8)
	p.unsignedField(3, offDeadline, 8).num(DeadlineInterval).op(arkade.OP_ADD, arkade.OP_NUMEQUALVERIFY)
	p.num(1).op(arkade.OP_PICK).num(0x1a - nextFirst).op(arkade.OP_MUL).num(nextFirst).op(arkade.OP_ADD)
	p.unsignedField(1, offPhase, 1).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_2DROP, arkade.OP_DROP)
	// ( prev is_second next -- )
	p.remainingShares(params, id, actor, remainingRevealCards(params, River, actor))
	return p.num(1).bytes()
}

// emitAllInCall requires a full positive call and publishes all remaining shares.
func emitAllInCall(params Params, id ContractID, street Street, actor Player) ([]byte, error) {
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	if !validPlayer(actor) {
		return nil, fmt.Errorf("covenant: invalid actor")
	}
	if street < PreFlop || street > River {
		return nil, fmt.Errorf("covenant: invalid all-in street")
	}
	half, cap := params.Stake+params.Bond, params.MaxWager
	player := byte(actor - Player1)
	phase, next := byte(0x1c+2*(street-PreFlop)), byte(0x24+2*(street-PreFlop))
	if street == River {
		next = 0x18
	}
	cards := remainingRevealCards(params, street, actor)
	prefix := headerPrefix(id)
	actorOffset, opponentOffset := int64(36+player*8), int64(36+(1-player)*8)
	p := newProgram()
	p.op(arkade.OP_PUSHCURRENTINPUTINDEX).num(0).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_INSPECTNUMINPUTS).num(1).op(arkade.OP_GREATERTHAN, arkade.OP_VERIFY)
	p.op(arkade.OP_DEPTH).num(int64(len(cards))).op(arkade.OP_NUMEQUALVERIFY)
	p.num(int64(typeState)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offPhase)).num(2).op(arkade.OP_SUBSTR).data([]byte{phase + player, 2}).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(opponentOffset).num(8).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM).num(cap).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(actorOffset).num(8).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
	p.op(arkade.OP_DUP).num(cap).op(arkade.OP_LESSTHAN, arkade.OP_VERIFY)
	p.num(2*half+cap).op(arkade.OP_ADD).num(0).op(arkade.OP_INSPECTINPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(2*(half+cap)).num(0).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(0).op(arkade.OP_INSPECTINPUTSCRIPTPUBKEY).num(0).op(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY)
	p.op(arkade.OP_ROT, arkade.OP_EQUALVERIFY, arkade.OP_EQUALVERIFY)
	p.num(int64(typeState)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offPhase)).num(2).op(arkade.OP_SUBSTR).data([]byte{next + 1 - player, 0}).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offWager1)).num(8).op(arkade.OP_SUBSTR).data(u64LE(uint64(cap))).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offWager2)).num(8).op(arkade.OP_SUBSTR).data(u64LE(uint64(cap))).op(arkade.OP_EQUALVERIFY)
	p.unsignedField(0, offDeadline, 8)
	p.unsignedField(2, offDeadline, 8).num(DeadlineInterval).op(arkade.OP_ADD, arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_2DROP)
	// ( prev next -- )
	p.remainingShares(params, id, actor, cards)
	return p.num(1).bytes()
}

// emitAllInReveal publishes the bettor's remaining shares and enters evaluation.
func emitAllInReveal(params Params, id ContractID, firstUnrevealed Street, actor Player) ([]byte, error) {
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	if !validPlayer(actor) {
		return nil, fmt.Errorf("covenant: invalid actor")
	}
	if firstUnrevealed < Flop || firstUnrevealed > River {
		return nil, fmt.Errorf("covenant: invalid remaining board street")
	}
	half, cap := params.Stake+params.Bond, params.MaxWager
	player := byte(actor - Player1)
	phase := byte(0x24 + 2*(firstUnrevealed-Flop))
	cards := remainingRevealCards(params, firstUnrevealed-1, actor)
	prefix := headerPrefix(id)
	p := newProgram()
	p.op(arkade.OP_PUSHCURRENTINPUTINDEX).num(0).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_INSPECTNUMINPUTS).num(1).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DEPTH).num(int64(len(cards))).op(arkade.OP_NUMEQUALVERIFY)
	p.num(int64(typeState)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offPhase)).num(2).op(arkade.OP_SUBSTR).data([]byte{phase + player, 0}).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offWager1)).num(8).op(arkade.OP_SUBSTR).data(u64LE(uint64(cap))).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offWager2)).num(8).op(arkade.OP_SUBSTR).data(u64LE(uint64(cap))).op(arkade.OP_EQUALVERIFY)
	p.num(2*(half+cap)).num(0).op(arkade.OP_INSPECTINPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(2*(half+cap)).num(0).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(0).op(arkade.OP_INSPECTINPUTSCRIPTPUBKEY).num(0).op(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY)
	p.op(arkade.OP_ROT, arkade.OP_EQUALVERIFY, arkade.OP_EQUALVERIFY)
	p.num(int64(typeState)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offPhase)).num(2).op(arkade.OP_SUBSTR).data([]byte{0x1a, 0}).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offWager1)).num(16).op(arkade.OP_SUBSTR).num(2).op(arkade.OP_PICK).num(int64(offWager1)).num(16).op(arkade.OP_SUBSTR, arkade.OP_EQUALVERIFY)
	p.unsignedField(0, offDeadline, 8)
	p.unsignedField(2, offDeadline, 8).num(DeadlineInterval).op(arkade.OP_ADD, arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_2DROP)
	// ( prev next -- )
	p.remainingShares(params, id, actor, cards)
	return p.num(1).bytes()
}

// emitRevealProof consumes one proof, verifying both coordinates of both DLEQ
// equations. Its 389-byte transcript binds agreement, slot, publisher, c1, share
// and commitments. Native ECMUL bounds z without reducing an invalid response.
func emitRevealProof(params Params, id ContractID, actor Player, slot byte, card shuffle.MaskedCard) ([]byte, error) {
	if !validPlayer(actor) {
		return nil, fmt.Errorf("covenant: invalid actor")
	}
	if int(slot) >= len(revealLocations) {
		return nil, fmt.Errorf("covenant: invalid reveal slot")
	}
	key := params.Players.Player1.EncryptionKey
	if actor == Player2 {
		key = params.Players.Player2.EncryptionKey
	}
	publisher, err := key.AffineBytes()
	if err != nil {
		return nil, err
	}
	cardBytes, err := card.AffineBytes()
	if err != nil {
		return nil, err
	}
	c1 := cardBytes[:64]
	loc := revealLocations[slot]
	kind, offset := typeState+loc.packet, loc.offset
	prefix := append([]byte("ziffle/DLEQ/v2arkade/poker/reveal/v1"), id[:]...)
	prefix = append(prefix, slot)
	prefix = append(prefix, publisher[:]...)
	order, gx, gy := curveConstants()
	p := newProgram()
	p.op(arkade.OP_SIZE).num(160).op(arkade.OP_NUMEQUALVERIFY)
	p.num(int64(kind)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY)
	p.num(int64(offset)).num(64).op(arkade.OP_SUBSTR)
	// ( proof -- proof share )
	// Stage equation operands as y then x on the alternate stack.
	p.data(scriptUnsigned(publisher[32:])).op(arkade.OP_TOALTSTACK)
	p.data(scriptUnsigned(publisher[:32])).op(arkade.OP_TOALTSTACK)
	p.data(scriptUnsigned(c1[32:])).op(arkade.OP_TOALTSTACK)
	p.data(scriptUnsigned(c1[:32])).op(arkade.OP_TOALTSTACK)
	p.op(arkade.OP_DUP).pointNumbers().op(arkade.OP_TOALTSTACK, arkade.OP_TOALTSTACK)
	p.num(1).op(arkade.OP_PICK).num(0).num(64).op(arkade.OP_SUBSTR).pointNumbers().op(arkade.OP_TOALTSTACK, arkade.OP_TOALTSTACK)
	p.num(1).op(arkade.OP_PICK).num(64).num(64).op(arkade.OP_SUBSTR).pointNumbers().op(arkade.OP_TOALTSTACK, arkade.OP_TOALTSTACK)
	p.num(1).op(arkade.OP_PICK).num(128).num(32).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM, arkade.OP_TOALTSTACK)
	// e = BE(SHA256(domain || agreement || slot || pk || share || c1 || t_g || t_c1)) mod n.
	p.data(prefix).num(1).op(arkade.OP_PICK, arkade.OP_CAT).data(c1).op(arkade.OP_CAT)
	p.num(2).op(arkade.OP_PICK).num(0).num(128).op(arkade.OP_SUBSTR, arkade.OP_CAT)
	p.op(arkade.OP_SHA256, arkade.OP_REVERSEBYTES).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
	p.data(scriptUnsigned(order)).op(arkade.OP_MOD, arkade.OP_TOALTSTACK)
	p.op(arkade.OP_2DROP)
	// ( proof share -- )
	// F = [e z tc_x tc_y tg_x tg_y share_x share_y c1_x c1_y pk_x pk_y].
	p.op(arkade.OP_FROMALTSTACK, arkade.OP_FROMALTSTACK).op(arkade.OP_FROMALTSTACK, arkade.OP_FROMALTSTACK)
	p.op(arkade.OP_FROMALTSTACK, arkade.OP_FROMALTSTACK).op(arkade.OP_FROMALTSTACK, arkade.OP_FROMALTSTACK)
	p.op(arkade.OP_FROMALTSTACK, arkade.OP_FROMALTSTACK).op(arkade.OP_FROMALTSTACK, arkade.OP_FROMALTSTACK)
	// t_g == z*G + e*pk.
	p.data(scriptUnsigned(gx)).data(scriptUnsigned(gy))
	p.num(12).op(arkade.OP_PICK).num(0).op(arkade.OP_ECMUL)
	p.num(3).op(arkade.OP_PICK).num(3).op(arkade.OP_PICK).num(15).op(arkade.OP_PICK).num(0).op(arkade.OP_ECMUL)
	p.num(0).op(arkade.OP_ECADD)
	p.num(8).op(arkade.OP_PICK, arkade.OP_EQUALVERIFY).num(8).op(arkade.OP_PICK, arkade.OP_EQUALVERIFY)
	// t_c1 == z*c1 + e*share.
	p.num(3).op(arkade.OP_PICK).num(3).op(arkade.OP_PICK).num(12).op(arkade.OP_PICK).num(0).op(arkade.OP_ECMUL)
	p.num(7).op(arkade.OP_PICK).num(7).op(arkade.OP_PICK).num(15).op(arkade.OP_PICK).num(0).op(arkade.OP_ECMUL)
	p.num(0).op(arkade.OP_ECADD)
	p.num(10).op(arkade.OP_PICK, arkade.OP_EQUALVERIFY).num(10).op(arkade.OP_PICK, arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_2DROP, arkade.OP_2DROP).op(arkade.OP_2DROP, arkade.OP_2DROP).op(arkade.OP_2DROP, arkade.OP_2DROP)
	return p.bytes()
}

// emitShowdownSource validates the state and packet shapes consumed by settlement.
func emitShowdownSource(params Params, id ContractID) ([]byte, error) {
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	half, cap := params.Stake+params.Bond, params.MaxWager
	prefix := headerPrefix(id)
	p := newProgram()
	p.op(arkade.OP_PUSHCURRENTINPUTINDEX).num(0).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_INSPECTNUMINPUTS).num(1).op(arkade.OP_NUMEQUALVERIFY)
	p.num(int64(typeState)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY)
	p.op(arkade.OP_SIZE).num(int64(headerLen)).op(arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_DUP).num(0).num(int64(offPhase)).op(arkade.OP_SUBSTR).data(prefix).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_DUP).num(int64(offPhase)).num(2).op(arkade.OP_SUBSTR).data([]byte{0x1a, 0}).op(arkade.OP_EQUALVERIFY)
	p.unsignedField(0, offWager1, 8)
	p.op(arkade.OP_DUP).num(cap).op(arkade.OP_LESSTHANOREQUAL, arkade.OP_VERIFY)
	p.unsignedField(1, offWager2, 8)
	p.op(arkade.OP_OVER, arkade.OP_NUMEQUALVERIFY)
	p.op(arkade.OP_SWAP, arkade.OP_DROP)
	// ( header wager -- wager )
	p.op(arkade.OP_DUP).num(half).op(arkade.OP_ADD).num(2).op(arkade.OP_MUL)
	p.op(arkade.OP_DUP).num(0).op(arkade.OP_INSPECTINPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	// ( wager -- wager value )
	for _, v := range revealPackets {
		p.num(int64(v.kind)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY, arkade.OP_SIZE).num(int64(v.length)).op(arkade.OP_NUMEQUALVERIFY, arkade.OP_DROP)
	}
	return p.bytes()
}

// emitShowdownRank verifies the existing universal seven-card rank commitment:
// [mask, rank_le2, low416, high448] -> [rank], sorted branches and depth 27.
func emitShowdownRank() ([]byte, error) {
	root := merkel.Root()
	leafTag := []byte("arkade/poker/hand-rank/leaf/v1")
	branchTag := []byte("arkade/poker/hand-rank/branch/v1")
	p := newProgram()
	p.op(arkade.OP_SIZE).num(448).op(arkade.OP_NUMEQUALVERIFY)
	p.num(1).op(arkade.OP_PICK, arkade.OP_SIZE).num(416).op(arkade.OP_NUMEQUALVERIFY, arkade.OP_DROP)
	p.num(2).op(arkade.OP_PICK, arkade.OP_SIZE).num(2).op(arkade.OP_NUMEQUALVERIFY, arkade.OP_DROP)
	p.num(2).op(arkade.OP_PICK).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
	p.op(arkade.OP_DUP).num(1).num(7463).op(arkade.OP_WITHIN, arkade.OP_VERIFY)
	p.data(leafTag).data(branchTag).num(4).op(arkade.OP_PICK)
	p.num(7).op(arkade.OP_PICK).num(7).op(arkade.OP_NUM2BIN).num(7).op(arkade.OP_PICK, arkade.OP_CAT)
	p.op(arkade.OP_MERKLEBRANCHVERIFY, arkade.OP_TOALTSTACK)
	p.num(0).data(branchTag).num(3).op(arkade.OP_PICK, arkade.OP_FROMALTSTACK, arkade.OP_MERKLEBRANCHVERIFY)
	p.data(root[:]).op(arkade.OP_EQUALVERIFY)
	p.op(arkade.OP_TOALTSTACK, arkade.OP_2DROP).op(arkade.OP_2DROP, arkade.OP_FROMALTSTACK)
	return p.bytes()
}

// emitShowdown proves all nine decrypted cards and both ranks, then pays fixed
// Player1/Player2 outputs. Either participant authorizes the identical program.
func emitShowdown(params Params, id ContractID) ([]byte, error) {
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	stake, bond := params.Stake, params.Bond
	p := newProgram()
	p.op(arkade.OP_DEPTH).num(7).op(arkade.OP_NUMEQUALVERIFY)
	p.include(emitShowdownSource(params, id))
	// Save authenticated C and t=stake+wager; the deadline grants no authority.
	p.op(arkade.OP_TOALTSTACK).num(stake).op(arkade.OP_ADD, arkade.OP_TOALTSTACK)
	p.op(arkade.OP_SIZE).num(9).op(arkade.OP_NUMEQUALVERIFY)
	cards := dealtOrder(params.EncryptedDeal)
	slots := [9][2]byte{{0, 14}, {1, 15}, {2, 16}, {3, 17}, {4, 7}, {5, 8}, {6, 9}, {10, 11}, {12, 13}}
	for i, card := range cards {
		p.include(emitShowdownCard(int64(i), card, slots[i][0], slots[i][1]))
	}
	for position := int64(0); position < 9; position++ {
		p.num(position).op(arkade.OP_PICK).num(position).num(1).op(arkade.OP_SUBSTR)
		p.data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
	}
	for i := int64(0); i < 9; i++ {
		for j := i + 1; j < 9; j++ {
			p.num(8-i).op(arkade.OP_PICK).num(9-j).op(arkade.OP_PICK, arkade.OP_NUMNOTEQUAL, arkade.OP_VERIFY)
		}
	}
	for player, positions := range [2][7]int64{{0, 1, 4, 5, 6, 7, 8}, {2, 3, 4, 5, 6, 7, 8}} {
		p.num(0)
		for _, position := range positions {
			depth := 10 + int64(player) - position
			p.num(1).num(depth).op(arkade.OP_PICK, arkade.OP_LSHIFT, arkade.OP_ADD)
		}
	}
	// Original witness is rank1/low1/high1/rank2/low2/high2/cards, bottom to top.
	p.num(1).op(arkade.OP_PICK).num(18).op(arkade.OP_PICK).num(18).op(arkade.OP_PICK).num(18).op(arkade.OP_PICK).include(emitShowdownRank())
	p.num(1).op(arkade.OP_PICK).num(16).op(arkade.OP_PICK).num(16).op(arkade.OP_PICK).num(16).op(arkade.OP_PICK).include(emitShowdownRank())
	// g and e are derived from both proven ranks, independently of submitter.
	p.op(arkade.OP_2DUP, arkade.OP_GREATERTHAN).num(2).op(arkade.OP_MUL, arkade.OP_TOALTSTACK)
	p.op(arkade.OP_NUMEQUAL, arkade.OP_FROMALTSTACK, arkade.OP_ADD)
	p.op(arkade.OP_FROMALTSTACK, arkade.OP_MUL).num(bond).op(arkade.OP_ADD)
	p.op(arkade.OP_FROMALTSTACK, arkade.OP_OVER, arkade.OP_SUB)
	p.num(1).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.num(0).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY)
	p.payout(0, params.Players.Player1.PayoutScript).payout(1, params.Players.Player2.PayoutScript)
	p.terminalPacketAbsence()
	p.op(arkade.OP_2DROP, arkade.OP_2DROP).op(arkade.OP_2DROP, arkade.OP_2DROP, arkade.OP_2DROP)
	p.op(arkade.OP_2DROP, arkade.OP_2DROP).op(arkade.OP_2DROP, arkade.OP_2DROP).num(1)
	return p.bytes()
}

func emitShowdownCard(position int64, card shuffle.MaskedCard, first, second byte) ([]byte, error) {
	cardBytes, err := card.AffineBytes()
	if err != nil {
		return nil, err
	}
	_, gx, gy := curveConstants()
	shares := newProgram()
	for _, slot := range []byte{first, second} {
		loc := revealLocations[slot]
		shares.num(int64(typeState+loc.packet)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY).num(int64(loc.offset)).num(64).op(arkade.OP_SUBSTR).pointNumbers()
	}
	p := newProgram()
	p.op(arkade.OP_DUP).num(int64(position)).num(1).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
	p.op(arkade.OP_DUP).num(52).op(arkade.OP_LESSTHAN, arkade.OP_VERIFY, arkade.OP_TOALTSTACK)
	p.include(shares.bytes()).num(0).op(arkade.OP_ECADD)
	p.data(scriptUnsigned(gx))
	p.data(scriptUnsigned(gy))
	p.op(arkade.OP_FROMALTSTACK).num(1).op(arkade.OP_ADD).num(0).op(arkade.OP_ECMUL).num(0).op(arkade.OP_ECADD)
	p.data(scriptUnsigned(cardBytes[96:128])).op(arkade.OP_NUMEQUALVERIFY)
	p.data(scriptUnsigned(cardBytes[64:96])).op(arkade.OP_NUMEQUALVERIFY)
	return p.bytes()
}
