package indexdata

import (
	"bytes"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

func TestSubscriptionLogicalOutpoints(t *testing.T) {
	a := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{3}, 32)...)
	b := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{4}, 32)...)
	scripts, err := WatchScripts([][]byte{a, b})
	if err != nil {
		t.Fatal(err)
	}
	id := chainhash.Hash{9}
	point := EventVtxo{TxID: chainhash.Hash{2}.String(), Script: hex.EncodeToString(a), Vout: 3}
	other := EventVtxo{TxID: chainhash.Hash{4}.String(), Script: hex.EncodeToString(b), Vout: 5}
	f := SubscriptionFrame{Event: &TransactionEvent{TxID: id.String(), New: []EventVtxo{point, other, point}, Spent: []EventVtxo{other}}}
	events, err := f.Events(scripts)
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	if events[0].TxID != id || len(events[0].NewVtxos) != 1 || len(events[0].SpentVtxos) != 0 || events[0].NewVtxos[0].Index != 3 || events[0].NewVtxos[0].Hash != chainhash.Hash([32]byte{2}) {
		t.Fatal(events)
	}
	if len(events[1].NewVtxos) != 1 || !slices.Equal(events[1].NewVtxos, events[1].SpentVtxos) {
		t.Fatal(events)
	}
	for _, bad := range []SubscriptionFrame{{}, {Started: "a", Heartbeat: true}, {Started: "bad/id"}, {Started: strings.Repeat("a", 129)}, {Event: &TransactionEvent{TxID: "01"}}, {Event: &TransactionEvent{TxID: id.String(), New: make([]EventVtxo, 257)}}, {Event: &TransactionEvent{TxID: id.String(), Spent: []EventVtxo{{TxID: point.TxID, Script: "xx"}}}}} {
		if _, err := bad.Events(scripts); err == nil {
			t.Fatal("malformed frame accepted")
		}
	}
	for _, bad := range [][][]byte{nil, {a, a}, {{0x51}}, make([][]byte, 17)} {
		if _, err := WatchScripts(bad); err == nil {
			t.Fatal("invalid filter accepted")
		}
	}
}
