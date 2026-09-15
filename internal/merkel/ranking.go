// Package merkel implements the universal poker hand-rank commitment. Card c
// has suit c/13 and rank c%13+2. Larger ranks are stronger, from 1 through 7462.
// The committed tree and hash domains are the production Rust protocol assets.
package merkel

import (
	"errors"
	"sort"
	"sync"
)

const NumRanks = 7462

var ErrCards = errors.New("cards must be distinct indices below 52")

func validate(cards []byte) error {
	var mask uint64
	for _, c := range cards {
		if c >= 52 || mask&(uint64(1)<<c) != 0 {
			return ErrCards
		}
		mask |= uint64(1) << c
	}
	return nil
}

// Strength is a lexicographic tuple (category, kickers...), packed as six
// nibbles. This is an internal lookup key, not a wire encoding.
func strength(cards [5]byte) uint32 {
	var counts [15]byte
	flush := true
	for _, c := range cards {
		counts[c%13+2]++
		flush = flush && c/13 == cards[0]/13
	}
	var distinct, singles [5]byte
	var pairs [2]byte
	var quad, trip byte
	nd, ns, np := 0, 0, 0
	for r := byte(14); r >= 2; r-- {
		if counts[r] != 0 {
			distinct[nd] = r
			nd++
		}
		switch counts[r] {
		case 4:
			quad = r
		case 3:
			trip = r
		case 2:
			pairs[np] = r
			np++
		case 1:
			singles[ns] = r
			ns++
		}
	}
	var straight byte
	if nd == 5 {
		if distinct == [5]byte{14, 5, 4, 3, 2} {
			straight = 5
		} else if distinct[0]-distinct[4] == 4 {
			straight = distinct[0]
		}
	}
	var tuple [6]byte
	switch {
	case flush && straight != 0:
		tuple = [6]byte{8, straight}
	case quad != 0:
		tuple = [6]byte{7, quad, singles[0]}
	case trip != 0 && np == 1:
		tuple = [6]byte{6, trip, pairs[0]}
	case flush:
		tuple = [6]byte{5, distinct[0], distinct[1], distinct[2], distinct[3], distinct[4]}
	case straight != 0:
		tuple = [6]byte{4, straight}
	case trip != 0:
		tuple = [6]byte{3, trip, singles[0], singles[1]}
	case np == 2:
		tuple = [6]byte{2, pairs[0], pairs[1], singles[0]}
	case np == 1:
		tuple = [6]byte{1, pairs[0], singles[0], singles[1], singles[2]}
	default:
		tuple = [6]byte{0, distinct[0], distinct[1], distinct[2], distinct[3], distinct[4]}
	}
	var packed uint32
	for _, v := range tuple {
		packed = packed<<4 | uint32(v)
	}
	return packed
}

var ranksOnce sync.Once
var ranks map[uint32]uint16

func rankTable() map[uint32]uint16 {
	ranksOnce.Do(func() {
		keys := make(map[uint32]struct{}, NumRanks)
		// Enumerate rank multisets and their possible flush/nonflush forms.
		// Assign duplicate ranks different suits. A flush is possible only when
		// all five ranks differ. No 2.6-million-hand startup scan is needed.
		for a := byte(0); a < 13; a++ {
			for b := a; b < 13; b++ {
				for c := b; c < 13; c++ {
					for d := c; d < 13; d++ {
						for e := d; e < 13; e++ {
							values := [5]byte{a, b, c, d, e}
							var count [13]byte
							var cards [5]byte
							valid, distinct := true, true
							for i, r := range values {
								if count[r] >= 4 {
									valid = false
									break
								}
								cards[i] = r + 13*count[r]
								distinct = distinct && count[r] == 0
								count[r]++
							}
							if !valid {
								continue
							}
							keys[strength(cards)] = struct{}{}
							if distinct {
								cards[4] += 13
								keys[strength(cards)] = struct{}{}
							}
						}
					}
				}
				cooperate()
			}
		}
		ordered := make([]uint32, 0, len(keys))
		for k := range keys {
			ordered = append(ordered, k)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
		if len(ordered) != NumRanks {
			panic("invalid universal rank table")
		}
		ranks = make(map[uint32]uint16, NumRanks)
		for i, k := range ordered {
			ranks[k] = uint16(i + 1)
		}
	})
	return ranks
}

func Rank5(cards [5]byte) (uint16, error) {
	if err := validate(cards[:]); err != nil {
		return 0, err
	}
	return rankTable()[strength(cards)], nil
}

func rank7(cards [7]byte, table map[uint32]uint16) uint16 {
	var best uint16
	// Each pair of omitted cards selects one of the 21 five-card subsets.
	for i := 0; i < 7; i++ {
		for j := i + 1; j < 7; j++ {
			var subset [5]byte
			n := 0
			for k, c := range cards {
				if k != i && k != j {
					subset[n] = c
					n++
				}
			}
			best = max(best, table[strength(subset)])
		}
	}
	return best
}

func Rank7(cards [7]byte) (uint16, error) {
	if err := validate(cards[:]); err != nil {
		return 0, err
	}
	return rank7(cards, rankTable()), nil
}

func choose(n, k uint64) uint64 {
	if k > n {
		return 0
	}
	k = min(k, n-k)
	v := uint64(1)
	for i := uint64(0); i < k; i++ {
		v = v * (n - i) / (i + 1)
	}
	return v
}

func combinationIndex(cards [7]byte) uint64 {
	sort.Slice(cards[:], func(i, j int) bool { return cards[i] < cards[j] })
	var index uint64
	previous := byte(0)
	for i, c := range cards {
		for v := previous; v < c; v++ {
			index += choose(uint64(51-v), uint64(6-i))
		}
		previous = c + 1
	}
	return index
}

func combinationAt(index uint64) [7]byte {
	var cards [7]byte
	previous := byte(0)
	for i := range cards {
		v := previous
		for {
			count := choose(uint64(51-v), uint64(6-i))
			if count > index {
				break
			}
			index -= count
			v++
		}
		cards[i] = v
		previous = v + 1
	}
	return cards
}
