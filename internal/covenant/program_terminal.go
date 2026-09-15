package covenant

import (
	"fmt"

	"github.com/arkade-os/emulator/pkg/arkade"
)

// terminalSource leaves [header actual_value] on the stack. Exits authenticate
// only the header, actor and actual input value; they deliberately do not acquire
// live wager/action/reveal requirements or require the deadline to advance.
func (p *programBuilder) terminalSource(id ContractID, phases []byte) *programBuilder {
	var allowed int64
	for _, phase := range phases {
		allowed |= int64(1) << phase
	}
	return p.op(arkade.OP_PUSHCURRENTINPUTINDEX).num(0).op(arkade.OP_NUMEQUALVERIFY).
		op(arkade.OP_INSPECTNUMINPUTS).num(1).op(arkade.OP_NUMEQUALVERIFY).
		num(typeState).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY).
		op(arkade.OP_SIZE).num(headerLen).op(arkade.OP_NUMEQUALVERIFY).
		op(arkade.OP_DUP).num(0).num(offPhase).op(arkade.OP_SUBSTR).data(headerPrefix(id)).op(arkade.OP_EQUALVERIFY).
		num(allowed).unsignedField(1, offPhase, 1).op(arkade.OP_RSHIFT).num(2).op(arkade.OP_MOD, arkade.OP_VERIFY).
		num(0).op(arkade.OP_INSPECTINPUTVALUE)
}

func emitConcession(params Params, id ContractID, actor Player) ([]byte, error) {
	if !validPlayer(actor) {
		return nil, fmt.Errorf("covenant: invalid concession actor")
	}
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	phases := []byte{2, 4, 6, 8, 0x16, 0x18, 0x1c, 0x1e, 0x20, 0x22, 0x24, 0x26, 0x28}
	for i := range phases {
		phases[i] += byte(actor - Player1)
	}
	p := newProgram().op(arkade.OP_DEPTH).num(0).op(arkade.OP_NUMEQUALVERIFY).
		terminalSource(id, phases).op(arkade.OP_SWAP, arkade.OP_DROP, arkade.OP_DUP).
		num(params.Bond).op(arkade.OP_GREATERTHANOREQUAL, arkade.OP_VERIFY)
	actorOutput := int64(actor - Player1)
	p.num(params.Bond).num(actorOutput).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY).
		num(params.Bond).op(arkade.OP_SUB).num(1-actorOutput).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY).
		payout(0, params.Players.Player1.PayoutScript).payout(1, params.Players.Player2.PayoutScript).
		terminalPacketAbsence().num(1)
	return p.bytes()
}

func emitTimeout(params Params, id ContractID, beneficiary Player) ([]byte, error) {
	if !validPlayer(beneficiary) {
		return nil, fmt.Errorf("covenant: invalid timeout beneficiary")
	}
	if err := validateAmounts(params); err != nil {
		return nil, err
	}
	var phases []byte
	parity := byte(0)
	if beneficiary == Player1 {
		parity = 1
	}
	for phase := byte(0); phase <= 0x29; phase++ {
		if phase != 0x1a && phase != 0x1b && phase%2 == parity {
			phases = append(phases, phase)
		}
	}
	destination := params.Players.Player1.PayoutScript
	if beneficiary == Player2 {
		destination = params.Players.Player2.PayoutScript
	}
	return newProgram().op(arkade.OP_DEPTH).num(0).op(arkade.OP_NUMEQUALVERIFY).
		terminalSource(id, phases).op(arkade.OP_SWAP).num(offDeadline).num(8).op(arkade.OP_SUBSTR).
		data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM, arkade.OP_CHECKTIMEVERIFY).
		num(0).op(arkade.OP_INSPECTOUTPUTVALUE, arkade.OP_NUMEQUALVERIFY).
		payout(0, destination).terminalPacketAbsence().num(1).bytes()
}
