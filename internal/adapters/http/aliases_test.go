package http

import (
	"strings"
	"testing"
)

func TestUnaryProtoJSONAliases(t *testing.T) {
	var response struct {
		Signer string   `json:"signerPubkey"`
		CP     []string `json:"signedCheckpointTxs"`
		Dust   decimal  `json:"dust"`
	}
	if err := decodeProtoFields([]byte(`{"signer_pubkey":"key","signed_checkpoint_txs":["cp"],"dust":"330"}`), &response); err != nil || response.Signer != "key" || len(response.CP) != 1 || response.Dust != 330 {
		t.Fatal(response, err)
	}
	for _, data := range []string{`{"signerPubkey":"a","signer_pubkey":"b"}`, `{"signedCheckpointTxs":[],"signed_checkpoint_txs":[]}`, `{"dust":"` + strings.Repeat("9", 32) + `"}`} {
		if err := decodeProtoFields([]byte(data), &response); err == nil {
			t.Fatal("ambiguous/overflowed info admitted")
		}
	}
}
