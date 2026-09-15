package merkel

import "testing"

func TestDescribeUsesRankingOrder(t *testing.T) {
	for _, tt := range []struct {
		cards []byte
		want  string
	}{
		{[]byte{51, 36, 49, 35, 5, 20, 10}, "THREE OF A KIND · QUEENS"},
		{[]byte{50, 24, 49, 35, 5, 20, 10}, "TWO PAIR · KINGS & QUEENS"},
		{[]byte{12, 0, 1, 2, 3, 20, 30}, "STRAIGHT FLUSH"},
		{[]byte{8, 9, 10, 11, 12, 20, 30}, "ROYAL FLUSH"},
		{[]byte{12, 25, 38, 11, 24, 0, 1}, "FULL HOUSE · ACES OVER KINGS"},
	} {
		label, best, err := Describe(tt.cards)
		if err != nil || label != tt.want {
			t.Fatalf("%v: %s %v", tt.cards, label, err)
		}
		rank, _ := Rank5(best)
		seven, _ := Rank7([7]byte(tt.cards))
		if rank != seven {
			t.Fatal("description selected a weaker hand")
		}
	}
	for _, bad := range [][]byte{{1, 2, 3, 4}, {1, 1, 2, 3, 4}, {1, 2, 3, 4, 52}} {
		if _, _, err := Describe(bad); err == nil {
			t.Fatal("invalid cards described")
		}
	}
}
