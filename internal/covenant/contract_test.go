package covenant

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"testing"

	"arkade-poker/go/internal/shuffle"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
)

func scalarPoint(n uint32) btcec.JacobianPoint {
	var s btcec.ModNScalar
	s.SetInt(n)
	var p btcec.JacobianPoint
	btcec.ScalarBaseMultNonConst(&s, &p)
	p.ToAffine()
	return p
}
func affinePoint(p btcec.JacobianPoint) [64]byte {
	p.ToAffine()
	key := btcec.NewPublicKey(&p.X, &p.Y)
	b := key.SerializeUncompressed()[1:]
	slices.Reverse(b[:32])
	slices.Reverse(b[32:])
	return [64]byte(b)
}
func cryptoParams(t *testing.T) Params {
	return cryptoParamsForDeal(t, [9]byte{0, 1, 2, 3, 4, 5, 6, 7, 8})
}
func cryptoParamsForDeal(t *testing.T, deal [9]byte) Params {
	t.Helper()
	params := terminalParams()
	params.InitialDeadline = 1
	for i, target := range []*shuffle.PublicKey{&params.Players.Player1.EncryptionKey, &params.Players.Player2.EncryptionKey} {
		k, err := shuffle.PublicKeyFromAffine(affinePoint(scalarPoint(uint32(i + 10))))
		if err != nil {
			t.Fatal(err)
		}
		*target = k
	}
	// Deterministic test cards: c1=rG and c2=(index+1+21r)G. Production keys,
	// shuffle provenance and randomness belong to shuffle; this is a VM fixture.
	cards := make([]shuffle.MaskedCard, 9)
	for i := range cards {
		var b [128]byte
		a := affinePoint(scalarPoint(uint32(i + 20)))
		c := affinePoint(scalarPoint(uint32(int(deal[i]) + 1 + 21*(i+20))))
		copy(b[:64], a[:])
		copy(b[64:], c[:])
		var err error
		cards[i], err = shuffle.MaskedCardFromAffine(b)
		if err != nil {
			t.Fatal(err)
		}
	}
	params.EncryptedDeal = DealtCards[shuffle.MaskedCard]{HoleCards: PerPlayer[[2]shuffle.MaskedCard]{[2]shuffle.MaskedCard{cards[0], cards[1]}, [2]shuffle.MaskedCard{cards[2], cards[3]}}, Flop: [3]shuffle.MaskedCard{cards[4], cards[5], cards[6]}, Turn: cards[7], River: cards[8]}
	return params
}

// Fixture proof generation uses public, fixed scalar secrets and nonces. There
// is no test-side proof verifier: every equation is checked by the real emulator.
func fixtureReveal(t *testing.T, params Params, id ContractID, actor Player, slot byte, card shuffle.MaskedCard) CardReveal {
	t.Helper()
	key := params.Players.Player1.EncryptionKey
	if actor == Player2 {
		key = params.Players.Player2.EncryptionKey
	}
	publisher, err := key.AffineBytes()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := card.AffineBytes()
	if err != nil {
		t.Fatal(err)
	}
	compact, err := card.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	c1, err := btcec.ParsePubKey(compact[:33])
	if err != nil {
		t.Fatal(err)
	}
	var cp btcec.JacobianPoint
	c1.AsJacobian(&cp)
	var secret, nonce btcec.ModNScalar
	secret.SetInt(uint32(9 + actor))
	nonce.SetInt(uint32(100 + slot))
	var share, tg, tc btcec.JacobianPoint
	btcec.ScalarMultNonConst(&secret, &cp, &share)
	btcec.ScalarBaseMultNonConst(&nonce, &tg)
	btcec.ScalarMultNonConst(&nonce, &cp, &tc)
	shareBytes, tgBytes, tcBytes := affinePoint(share), affinePoint(tg), affinePoint(tc)
	h := sha256.New()
	h.Write([]byte("ziffle/DLEQ/v2arkade/poker/reveal/v1"))
	h.Write(id[:])
	h.Write([]byte{slot})
	h.Write(publisher[:])
	h.Write(shareBytes[:])
	h.Write(cipher[:64])
	h.Write(tgBytes[:])
	h.Write(tcBytes[:])
	var e, z btcec.ModNScalar
	e.SetByteSlice(h.Sum(nil))
	z.Mul2(&e, &secret).Negate().Add(&nonce)
	response := z.Bytes()
	slices.Reverse(response[:])
	var proofBytes [160]byte
	copy(proofBytes[:64], tgBytes[:])
	copy(proofBytes[64:128], tcBytes[:])
	copy(proofBytes[128:], response[:])
	token, err := shuffle.RevealTokenFromAffine(shareBytes)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := shuffle.RevealProofFromAffine(proofBytes)
	if err != nil {
		t.Fatal(err)
	}
	return CardReveal{Share: token, Proof: proof}
}

func TestDerivationEveryPath(t *testing.T) {
	params := cryptoParams(t)
	_, _, checkpoint := sourceFixture(t, 1, 0)
	contract, err := Derive(params, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := contract.SpendingPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 38 {
		t.Fatalf("paths %d", len(paths))
	}
	output, _ := contract.ScriptPubKey()
	programs := map[string]bool{}
	identities := map[SpendingIdentity]bool{}
	for _, path := range paths {
		if identities[path.Identity] {
			t.Fatal("duplicate identity")
		}
		identities[path.Identity] = true
		programs[string(path.Program)] = true
		if len(path.Program) == 0 || len(path.Program) > arkade.MaxScriptLength || len(path.Tree) != 38 {
			t.Fatalf("invalid path %+v length %d", path.Identity, len(path.Program))
		}
		if err := arkade.VerifyTaprootLeafCommitment(output, path.Leaf); err != nil {
			t.Fatal(err)
		}
		cb, _ := txscript.ParseControlBlock(path.Leaf.ControlBlock)
		if cb.LeafVersion != txscript.BaseLeafVersion {
			t.Fatal("wrong leaf version")
		}
		actorKey := params.Players.Player1.SigningKey
		if path.Identity.Actor == Player2 {
			actorKey = params.Players.Player2.SigningKey
		}
		if !bytes.Equal(path.Leaf.Script[1:33], path.TweakedEmulatorKey[:]) || !bytes.Equal(path.Leaf.Script[35:67], actorKey[:]) || !bytes.Equal(path.Leaf.Script[69:101], params.ArkSigningKey[:]) {
			t.Fatal("outer signer order")
		}
		t.Logf("%+v: %d program bytes", path.Identity, len(path.Program))
	}
	if len(programs) != 37 {
		t.Fatalf("unique programs %d", len(programs))
	}
	other, err := Derive(params, []byte{txscript.OP_TRUE})
	if err != nil {
		t.Fatal(err)
	}
	if other.id != contract.id || !bytes.Equal(other.script, contract.script) {
		t.Fatal("checkpoint entered agreement commitment")
	}
	params.Players.Player1.PayoutScript[2] ^= 1
	paths[0].Program[0] ^= 1
	paths[0].Tree[0] = "00"
	paths[0].Leaf.Script[0] ^= 1
	output[0] ^= 1
	checkpoint[0] ^= 1
	fresh, _ := contract.SpendingPaths()
	if bytes.Equal(fresh[0].Program, paths[0].Program) || bytes.Equal(fresh[0].Leaf.Script, paths[0].Leaf.Script) || fresh[0].Tree[0] == "00" || contract.script[0] != txscript.OP_1 || contract.params.Players.Player1.PayoutScript[2] == params.Players.Player1.PayoutScript[2] {
		t.Fatal("derived contract aliases external slices")
	}
}

func TestDerivationParameterAdmissionAndCommitment(t *testing.T) {
	base := cryptoParams(t)
	a, err := Derive(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*Params){
		"emulator": func(p *Params) { p.EmulatorSigningKey = p.Players.Player2.SigningKey }, "server": func(p *Params) { p.ArkSigningKey = p.Players.Player1.SigningKey },
		"signer1": func(p *Params) { p.Players.Player1.SigningKey = p.Players.Player2.SigningKey }, "signer2": func(p *Params) { p.Players.Player2.SigningKey = p.Players.Player1.SigningKey },
		"encryption1": func(p *Params) { p.Players.Player1.EncryptionKey = p.Players.Player2.EncryptionKey }, "encryption2": func(p *Params) { p.Players.Player2.EncryptionKey = p.Players.Player1.EncryptionKey },
		"payout1": func(p *Params) { p.Players.Player1.PayoutScript = []byte{txscript.OP_TRUE} }, "payout2": func(p *Params) { p.Players.Player2.PayoutScript = nil },
		"stake": func(p *Params) { p.Stake++ }, "bond": func(p *Params) { p.Bond++ }, "minimum": func(p *Params) { p.MinBet++ }, "cap": func(p *Params) { p.MaxWager++ }, "deadline": func(p *Params) { p.InitialDeadline++ },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			p := base
			change(&p)
			b, err := Derive(p, nil)
			if err != nil {
				t.Fatal(err)
			}
			if b.id == a.id || bytes.Equal(b.script, a.script) {
				t.Fatal("parameter not committed")
			}
		})
	}
	for i := range 9 {
		p := base
		card, _ := shuffle.MaskedCardFromAffine(func() [128]byte { b, _ := p.EncryptedDeal.Turn.AffineBytes(); return b }())
		switch i {
		case 0, 1:
			p.EncryptedDeal.HoleCards.Player1[i] = card
		case 2, 3:
			p.EncryptedDeal.HoleCards.Player2[i-2] = card
		case 4, 5, 6:
			p.EncryptedDeal.Flop[i-4] = card
		case 7:
			p.EncryptedDeal.Turn = p.EncryptedDeal.River
		case 8:
			p.EncryptedDeal.River = card
		}
		b, err := Derive(p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if b.id == a.id {
			t.Fatalf("card %d not committed", i)
		}
	}
	for _, change := range []func(*Params){func(p *Params) { p.Stake = 0 }, func(p *Params) { p.Bond = -1 }, func(p *Params) { p.MinBet = 0 }, func(p *Params) { p.MaxWager = p.MinBet - 1 }, func(p *Params) { p.Stake = maxMoney }, func(p *Params) { p.MaxWager = maxMoney + 1 }, func(p *Params) { p.ArkSigningKey = [32]byte{} }, func(p *Params) { p.Players.Player1.EncryptionKey = shuffle.PublicKey{} }, func(p *Params) { p.EncryptedDeal.River = shuffle.MaskedCard{} }, func(p *Params) {
		b, _ := p.Players.Player1.EncryptionKey.MarshalBinary()
		b[0] ^= 1
		p.Players.Player2.EncryptionKey, _ = shuffle.DecodePublicKey(b)
	}} {
		p := base
		change(&p)
		if _, err := Derive(p, nil); err == nil {
			t.Fatal("invalid terms accepted")
		}
	}
	var zero Contract
	for _, c := range []*Contract{nil, &zero} {
		if _, err := c.ID(); err != ErrInvalidContract {
			t.Fatal("zero contract accepted")
		}
		if _, err := c.ScriptPubKey(); err != ErrInvalidContract {
			t.Fatal("zero contract accepted")
		}
		if _, err := c.SpendingPaths(); err != ErrInvalidContract {
			t.Fatal("zero contract accepted")
		}
	}
	// Confirm explicit little-endian initial deadline and stable domain commitment
	// against an independently assembled commitment buffer using compact lengths.
	var b bytes.Buffer
	b.WriteString("arkade-poker/contract\x00")
	b.Write([]byte{1, 0})
	b.Write(base.EmulatorSigningKey[:])
	b.Write(base.ArkSigningKey[:])
	for _, p := range []Participant{base.Players.Player1, base.Players.Player2} {
		b.Write(p.SigningKey[:])
		aff, _ := p.EncryptionKey.AffineBytes()
		b.Write(aff[:])
		b.WriteByte(byte(len(p.PayoutScript)))
		b.Write(p.PayoutScript)
	}
	for _, n := range []uint64{100, 50, 10, 500, 1} {
		binary.Write(&b, binary.LittleEndian, n)
	}
	for _, card := range dealtOrder(base.EncryptedDeal) {
		aff, _ := card.AffineBytes()
		b.Write(aff[:])
	}
	if id := sha256.Sum256(b.Bytes()); ContractID(id) != a.id {
		t.Fatalf("commitment %x", a.id)
	}
}
