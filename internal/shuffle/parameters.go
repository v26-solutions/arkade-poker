package shuffle

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"sync"
)

// Fixed RFC 9380 bases copied verbatim from crates/shuffle/src/params_data.rs.
// DST: ziffle/BG12/Pedersen/v2/secp256k1_XMD:SHA-256_SSWU_RO_
// Messages: H; G || u32be(i), i=0..51. Runtime never generates bases or knows
// their discrete logs. Regeneration belongs to tools/shuffle-paramgen.
//
//go:embed parameters.bin
var parameterBytes []byte

const parameterChecksum = "6873e6d546d61c3add8b32fc4c8cc8cea42ab72bc1bfbdda2b6ac9b6141fbd9d"

type Parameters struct {
	h     point
	bases [DeckSize]point
	valid bool
}

var parametersOnce sync.Once
var fixedParameters Parameters
var parametersError error

func parseParameters(data []byte) (Parameters, error) {
	var p Parameters
	if len(data) != 33*(DeckSize+1) || fmt.Sprintf("%x", sha256.Sum256(data)) != parameterChecksum {
		return p, fmt.Errorf("shuffle: parameter checksum")
	}
	seen := map[string]bool{string(baseMul(scalarInt(1)).bytes()): true}
	for i := 0; i <= DeckSize; i++ {
		b := data[i*33:][:33]
		q, err := decodePoint(b, false)
		if err != nil || seen[string(b)] {
			return Parameters{}, fmt.Errorf("shuffle: parameter %d invalid or repeated", i)
		}
		seen[string(b)] = true
		if i == 0 {
			p.h = q
		} else {
			p.bases[i-1] = q
		}
	}
	p.valid = true
	return p, nil
}

// LoadParameters validates the asset once and returns an independent value.
func LoadParameters() (Parameters, error) {
	parametersOnce.Do(func() { fixedParameters, parametersError = parseParameters(parameterBytes) })
	return fixedParameters, parametersError
}
func (p Parameters) commit(m, r scalar) point { return baseMul(m).add(p.h.mul(r)) }
func (p Parameters) vectorCommit(w *work, ms [DeckSize]scalar, r scalar) point {
	sum := p.h.mul(r)
	for i := range ms {
		if !w.check() {
			return point{}
		}
		sum = sum.add(p.bases[i].mul(ms[i]))
	}
	return sum
}
