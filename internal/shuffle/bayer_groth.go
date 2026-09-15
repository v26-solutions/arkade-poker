package shuffle

// Bayer–Groth 2012 with m=1, N=52, matching crates/shuffle/src/shuffle.
// The product argument (section 5.3) proves a permutation; the multi-exponent
// argument (section 6) binds that permutation to ElGamal re-encryption. Both
// are mandatory. Variables follow the paper/reference to keep equations auditable.
type bgProof struct {
	cPi, cXPi point
	multi     multiArg
	product   productArg
}
type multiArg struct {
	cAlpha, cBeta        point
	ct0, ct1             MaskedCard
	oAlpha               [DeckSize]scalar
	oR, beta, oBeta, tau scalar
}
type productArg struct {
	cD, cSmallDelta, cCapitalDelta point
	aTilde, bTilde                 [DeckSize]scalar
	rTilde, sTilde                 scalar
}

func shuffleChallenge(key AggregatePublicKey, prev, next []MaskedCard, cPi point, binding []byte) (transcript, scalar) {
	before, after := make([][]byte, len(prev)), make([][]byte, len(next))
	for i, c := range prev {
		before[i] = c.bytes()
	}
	for i, c := range next {
		after[i] = c.bytes()
	}
	ts := newTranscript(binding).append("apk", key.point.bytes()).append("prev", before...).append("next", after...).append("c_pi", cPi.bytes())
	return ts, ts.challenge("ziffle/BG12/x/v2", 0)
}
func shuffleYZ(ts transcript, cXPi point) (transcript, scalar, scalar) {
	ts = ts.append("c_xpi", cXPi.bytes())
	return ts, ts.challenge("ziffle/BG12/yz/v2", 0), ts.challenge("ziffle/BG12/yz/v2", 1)
}
func (a multiArg) challenge(ts transcript) scalar {
	return ts.append("c_alpha", a.cAlpha.bytes()).append("c_beta", a.cBeta.bytes()).append("ct_mxp0", a.ct0.bytes()).append("ct_mxp1", a.ct1.bytes()).challenge("ziffle/BG12/multiexp/x/v2", 0)
}
func (a productArg) challenge(ts transcript) scalar {
	return ts.append("c_d", a.cD.bytes()).append("c_sdelta", a.cSmallDelta.bytes()).append("c_cdelta", a.cCapitalDelta.bytes()).challenge("ziffle/BG12/product/x/v2", 0)
}
func (p *Protocol) prove(w *work, key AggregatePublicKey, perm [DeckSize]int, prev, next Deck, rho [DeckSize]scalar, binding []byte) bgProof {
	var a bgProof
	var pi, xpi [DeckSize]scalar
	defer clear(pi[:])
	defer clear(xpi[:])
	defer clear(perm[:])
	defer clear(rho[:])
	for i := range pi {
		pi[i] = scalarInt(uint32(perm[i] + 1))
	}
	wPi := w.random(false)
	defer wPi.value.Zero()
	a.cPi = p.parameters.vectorCommit(w, pi, wPi)
	ts, x := shuffleChallenge(key, prev[:], next[:], a.cPi, binding)
	for i := range xpi {
		xpi[i] = x.pow(uint32(perm[i] + 1))
	}
	wXPi := w.random(false)
	defer wXPi.value.Zero()
	a.cXPi = p.parameters.vectorCommit(w, xpi, wXPi)
	ts, y, z := shuffleYZ(ts, a.cXPi)
	a.multi = p.proveMulti(w, key, next, rho, xpi, wXPi, ts)
	a.product = p.proveProduct(w, pi, xpi, wPi, wXPi, y, z, ts)
	return a
}
func (p *Protocol) proveMulti(w *work, key AggregatePublicKey, next Deck, rho, xpi [DeckSize]scalar, wXPi scalar, ts transcript) multiArg {
	var a multiArg
	alpha := w.vector()
	beta := w.random(false)
	wAlpha, wBeta := w.random(false), w.random(false)
	defer clear(alpha[:])
	defer beta.value.Zero()
	defer wAlpha.value.Zero()
	defer wBeta.value.Zero()
	defer clear(rho[:])
	defer clear(xpi[:])
	defer wXPi.value.Zero()
	a.cAlpha = p.parameters.vectorCommit(w, alpha, wAlpha)
	a.cBeta = p.parameters.commit(beta, wBeta)
	tau0 := w.random(false)
	defer tau0.value.Zero()
	c := w.cipherProduct(next, alpha)
	a.ct0 = MaskedCard{baseMul(tau0).add(c.c1), baseMul(beta).add(key.point.mul(tau0)).add(c.c2)}
	var rhoAgg scalar
	defer rhoAgg.value.Zero()
	for i := range rho {
		rhoAgg = rhoAgg.add(rho[i].mul(xpi[i]))
	}
	rhoAgg = rhoAgg.neg()
	c = w.cipherProduct(next, xpi)
	a.ct1 = MaskedCard{baseMul(rhoAgg).add(c.c1), key.point.mul(rhoAgg).add(c.c2)}
	x := a.challenge(ts)
	for i := range alpha {
		a.oAlpha[i] = alpha[i].add(x.mul(xpi[i]))
	}
	a.oR = wAlpha.add(x.mul(wXPi))
	a.beta = beta
	a.oBeta = wBeta
	a.tau = tau0.add(x.mul(rhoAgg))
	return a
}
func (p *Protocol) verifyMulti(w *work, key AggregatePublicKey, prev, next Deck, proof bgProof, xBase scalar, ts transcript) bool {
	a := proof.multi
	x := a.challenge(ts)
	var xs [DeckSize]scalar
	for i := range xs {
		xs[i] = xBase.pow(uint32(i + 1))
	}
	// Anchor to the weighted predecessor, including infinity for initial ct1.c1.
	if !w.cipherProduct(prev, xs).equal(a.ct1) {
		return false
	}
	if !proof.cXPi.mul(x).add(a.cAlpha).equal(p.parameters.vectorCommit(w, a.oAlpha, a.oR)) {
		return false
	}
	if !a.cBeta.equal(p.parameters.commit(a.beta, a.oBeta)) {
		return false
	}
	prod := w.cipherProduct(next, a.oAlpha)
	lhs := MaskedCard{a.ct0.c1.add(a.ct1.c1.mul(x)), a.ct0.c2.add(a.ct1.c2.mul(x))}
	rhs := MaskedCard{baseMul(a.tau).add(prod.c1), baseMul(a.beta).add(key.point.mul(a.tau)).add(prod.c2)}
	return w.err == nil && lhs.equal(rhs)
}
func (p *Protocol) proveProduct(w *work, pi, xpi [DeckSize]scalar, wPi, wXPi, y, z scalar, ts transcript) productArg {
	var out productArg
	d := w.vector()
	wD := w.random(false)
	defer clear(d[:])
	defer wD.value.Zero()
	defer clear(pi[:])
	defer clear(xpi[:])
	defer wPi.value.Zero()
	defer wXPi.value.Zero()
	out.cD = p.parameters.vectorCommit(w, d, wD)
	var smallDelta, v, a, b [DeckSize]scalar
	defer clear(smallDelta[:])
	defer clear(v[:])
	defer clear(a[:])
	defer clear(b[:])
	smallDelta[0] = d[0]
	for i := 1; i < DeckSize-1; i++ {
		smallDelta[i] = w.random(false)
	}
	for i := 0; i < DeckSize-1; i++ {
		v[i] = smallDelta[i].neg().mul(d[i+1])
	}
	wSmall := w.random(false)
	defer wSmall.value.Zero()
	out.cSmallDelta = p.parameters.vectorCommit(w, v, wSmall)
	for i := range a {
		a[i] = y.mul(pi[i]).add(xpi[i]).sub(z)
	}
	b[0] = a[0]
	for i := 1; i < DeckSize; i++ {
		b[i] = b[i-1].mul(a[i])
	}
	for i := 0; i < DeckSize-1; i++ {
		v[i] = smallDelta[i+1].sub(a[i+1].mul(smallDelta[i])).sub(b[i].mul(d[i+1]))
	}
	wCapital := w.random(false)
	defer wCapital.value.Zero()
	out.cCapitalDelta = p.parameters.vectorCommit(w, v, wCapital)
	x := out.challenge(ts)
	for i := range a {
		out.aTilde[i] = x.mul(a[i]).add(d[i])
		out.bTilde[i] = x.mul(b[i]).add(smallDelta[i])
	}
	out.rTilde = x.mul(y.mul(wPi).add(wXPi)).add(wD)
	out.sTilde = x.mul(wCapital).add(wSmall)
	return out
}
func (p *Protocol) verifyProduct(w *work, proof bgProof, xBase, y, z scalar, ts transcript) bool {
	a := proof.product
	x := a.challenge(ts)
	// Boundary checks connect the recurrence to the public permutation product.
	if !a.bTilde[0].equal(a.aTilde[0]) {
		return false
	}
	product := scalarInt(1)
	for i := uint32(1); i <= DeckSize; i++ {
		product = product.mul(y.mul(scalarInt(i)).add(xBase.pow(i)).sub(z))
	}
	if !a.bTilde[DeckSize-1].equal(x.mul(product)) {
		return false
	}
	var mz, v [DeckSize]scalar
	for i := range mz {
		mz[i] = z.neg()
	}
	cMZ := p.parameters.vectorCommit(w, mz, scalar{})
	cA := proof.cPi.mul(y).add(proof.cXPi)
	lhs := a.cD.add(cA.add(cMZ).mul(x))
	if !lhs.equal(p.parameters.vectorCommit(w, a.aTilde, a.rTilde)) {
		return false
	}
	for i := 0; i < DeckSize-1; i++ {
		v[i] = x.mul(a.bTilde[i+1]).sub(a.bTilde[i].mul(a.aTilde[i+1]))
	}
	lhs = a.cSmallDelta.add(a.cCapitalDelta.mul(x))
	rhs := p.parameters.vectorCommit(w, v, a.sTilde)
	return w.err == nil && lhs.equal(rhs)
}
