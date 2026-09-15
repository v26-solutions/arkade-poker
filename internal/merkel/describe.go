package merkel

// Describe returns a readable category and one best five-card subset of a
// five-to-seven-card hand. It uses the same strength ordering as Rank5/Rank7,
// but does not construct a rank table or generate a settlement proof.
func Describe(cards []byte) (string, [5]byte, error) {
	var best [5]byte
	if len(cards) < 5 || len(cards) > 7 {
		return "", best, ErrCards
	}
	if err := validate(cards); err != nil {
		return "", best, err
	}
	var value uint32
	for a := 0; a < len(cards)-4; a++ {
		for b := a + 1; b < len(cards)-3; b++ {
			for c := b + 1; c < len(cards)-2; c++ {
				for d := c + 1; d < len(cards)-1; d++ {
					for e := d + 1; e < len(cards); e++ {
						hand := [5]byte{cards[a], cards[b], cards[c], cards[d], cards[e]}
						if v := strength(hand); v > value {
							value, best = v, hand
						}
					}
				}
			}
		}
	}
	category := value >> 20
	high := (value >> 16) & 15
	name := []string{"HIGH CARD", "ONE PAIR", "TWO PAIR", "THREE OF A KIND", "STRAIGHT", "FLUSH", "FULL HOUSE", "FOUR OF A KIND", "STRAIGHT FLUSH"}[category]
	if category == 8 && high == 14 {
		return "ROYAL FLUSH", best, nil
	}
	ranks := []string{"", "", "TWOS", "THREES", "FOURS", "FIVES", "SIXES", "SEVENS", "EIGHTS", "NINES", "TENS", "JACKS", "QUEENS", "KINGS", "ACES"}
	if category == 1 || category == 2 || category == 3 || category == 6 || category == 7 {
		name += " · " + ranks[high]
		if category == 2 {
			name += " & " + ranks[(value>>12)&15]
		}
		if category == 6 {
			name += " OVER " + ranks[(value>>12)&15]
		}
	}
	return name, best, nil
}
