package shuffle

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
)

// Reference v2 framing, independent of host word size. Initial state is
// SHA256("ziffle/transcript/v2" || u32be(len(binding)) || binding).
// append hashes state || u16be(label length) || label || u32be(item count)
// followed by u32be(index) || u32be(length) || bytes for each item in order.
// Labels/items are internal fixed-size inputs; remote bindings are bounded
// before this constructor is called. Value receivers make explicit forks safe.
type transcript struct{ state [32]byte }

func validBinding(binding []byte) error {
	if uint64(len(binding)) > math.MaxUint32 {
		return fmt.Errorf("shuffle: binding too long")
	}
	return nil
}
func newTranscript(binding []byte) transcript {
	h := sha256.New()
	h.Write([]byte("ziffle/transcript/v2"))
	h.Write(binary.BigEndian.AppendUint32(nil, uint32(len(binding))))
	h.Write(binding)
	return transcript{[32]byte(h.Sum(nil))}
}
func (t transcript) append(label string, items ...[]byte) transcript {
	h := sha256.New()
	h.Write(t.state[:])
	h.Write(binary.BigEndian.AppendUint16(nil, uint16(len(label))))
	h.Write([]byte(label))
	h.Write(binary.BigEndian.AppendUint32(nil, uint32(len(items))))
	for i, b := range items {
		h.Write(binary.BigEndian.AppendUint32(nil, uint32(i)))
		h.Write(binary.BigEndian.AppendUint32(nil, uint32(len(b))))
		h.Write(b)
	}
	return transcript{[32]byte(h.Sum(nil))}
}

// Challenges are BE(SHA256("ziffle/challenge/v2" || u16be(domain length)
// || domain || state || u32be(index))) mod n. They may be zero. Sampling a
// challenge does not mutate the transcript (yz share one state, indices 0,1).
func (t transcript) challenge(domain string, index uint32) scalar {
	h := sha256.New()
	h.Write([]byte("ziffle/challenge/v2"))
	h.Write(binary.BigEndian.AppendUint16(nil, uint16(len(domain))))
	h.Write([]byte(domain))
	h.Write(t.state[:])
	h.Write(binary.BigEndian.AppendUint32(nil, index))
	return reduceDigest([32]byte(h.Sum(nil)))
}
