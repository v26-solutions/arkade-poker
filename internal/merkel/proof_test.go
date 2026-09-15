package merkel

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUniversalRanksExhaustive(t *testing.T) {
	// Independently published combinatorial category counts for all C(52,5)
	// distinct hands, and all 7,462 ordinal ranks. This scans physical hands;
	// production initialization enumerates rank multisets instead.
	want := [9]int{1302540, 1098240, 123552, 54912, 10200, 5108, 3744, 624, 40}
	var counts [9]int
	seen := make(map[uint16]bool)
	table := rankTable()
	for a := byte(0); a < 48; a++ {
		for b := a + 1; b < 49; b++ {
			for c := b + 1; c < 50; c++ {
				for d := c + 1; d < 51; d++ {
					for e := d + 1; e < 52; e++ {
						s := strength([5]byte{a, b, c, d, e})
						r, ok := table[s]
						if !ok || r == 0 || r > NumRanks {
							t.Fatal("physical hand missing from rank table")
						}
						seen[r] = true
						counts[s>>20]++
					}
				}
			}
		}
	}
	if counts != want || len(seen) != NumRanks {
		t.Fatalf("counts %v, ranks %d", counts, len(seen))
	}
	// Category-boundary rank assertions, including the ace-low wheel.
	for _, tc := range []struct {
		cards [5]byte
		rank  uint16
	}{
		{[5]byte{0, 1, 2, 3, 18}, 1}, // weakest: 7,5,4,3,2 without a flush
		{[5]byte{8, 9, 10, 11, 12}, 7462},
		{[5]byte{12, 0, 1, 2, 3}, 7453},
	} {
		r, err := Rank5(tc.cards)
		if err != nil || r != tc.rank {
			t.Fatalf("%v: rank %d, want %d (%v)", tc.cards, r, tc.rank, err)
		}
	}
}

func TestProofAgainstCommittedRoot(t *testing.T) {
	started := time.Now()
	for _, cards := range [][7]byte{
		{0, 1, 2, 3, 4, 5, 6}, {0, 13, 2, 15, 28, 3, 16}, {8, 9, 10, 11, 12, 25, 38},
		{45, 46, 47, 48, 49, 50, 51}, // block includes tree padding
	} {
		h, err := Generate(context.Background(), cards)
		if err != nil {
			t.Fatalf("%v: %v", cards, err)
		}
		if !h.Verify(cards) {
			t.Fatal("proof did not authenticate hand")
		}
		rank, _ := Rank7(cards)
		if rank != h.Rank {
			t.Fatal("rank mismatch")
		}
		encoded, _ := h.Proof.MarshalBinary()
		decoded, err := DecodeProof(encoded)
		if err != nil || decoded != h.Proof {
			t.Fatal("proof codec changed siblings")
		}
		if _, err := DecodeProof(append(encoded, 0)); err == nil {
			t.Fatal("trailing proof bytes admitted")
		}
		h.Rank = h.Rank%NumRanks + 1
		if h.Verify(cards) {
			t.Fatal("false rank admitted")
		}
		h.Rank = rank
		h.Proof.Low[0][0] ^= 1
		if h.Verify(cards) {
			t.Fatal("corrupt low segment admitted")
		}
		h.Proof.Low[0][0] ^= 1
		h.Proof.High[0][0] ^= 1
		if h.Verify(cards) {
			t.Fatal("corrupt high segment admitted")
		}
	}
	t.Logf("four complete rank proofs: %s", time.Since(started))
}

func TestDealAndCancellation(t *testing.T) {
	deal := [9]byte{0, 13, 1, 14, 2, 15, 28, 3, 16}
	e, err := Evaluate(context.Background(), deal)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Player1.Verify([7]byte{0, 13, 2, 15, 28, 3, 16}) || !e.Player2.Verify([7]byte{1, 14, 2, 15, 28, 3, 16}) {
		t.Fatal("deal order changed")
	}
	for _, cards := range [][7]byte{{0, 1, 2, 3, 4, 5, 5}, {0, 1, 2, 3, 4, 5, 52}} {
		if _, err := Generate(context.Background(), cards); err != ErrCards {
			t.Fatal("invalid cards admitted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Generate(ctx, [7]byte{0, 1, 2, 3, 4, 5, 6}); !errors.Is(err, context.Canceled) {
		t.Fatal("proof ignored cancellation")
	}
}

func TestCombinationInverse(t *testing.T) {
	for _, index := range []uint64{0, 1, 8191, 8192, 23456789, RealLeafCount - 1} {
		cards := combinationAt(index)
		if validate(cards[:]) != nil || combinationIndex(cards) != index {
			t.Fatalf("combination %d", index)
		}
	}
}
