package http

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestIndexerProtoNamesPreserveSpendEvidence(t *testing.T) {
	id := strings.Repeat("01", 32)
	base := fmt.Sprintf(`"outpoint":{"txid":%q},"script":"5120%s","amount":"1000"`, id, strings.Repeat("03", 32))
	var camel, snake indexerVtxo
	camelJSON := fmt.Sprintf(`{%s,"isSpent":true,"arkTxid":%q,"spentBy":%q,"expiresAt":"2000000000","assets":[{"assetId":"test","amount":"18446744073709551615"}]}`, base, id, id)
	snakeJSON := fmt.Sprintf(`{%s,"is_spent":true,"ark_txid":%q,"spent_by":%q,"expires_at":"2000000000","assets":[{"asset_id":"test","amount":"18446744073709551615"}]}`, base, id, id)
	if err := decodeObject([]byte(camelJSON), &camel); err != nil {
		t.Fatal(err)
	}
	if err := decodeObject([]byte(snakeJSON), &snake); err != nil {
		t.Fatal(err)
	}
	c, err := camel.decode()
	if err != nil {
		t.Fatal(err)
	}
	s, err := snake.decode()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, s) || !s.Spent || s.ArkTxID == nil || s.SpentBy == nil || s.ExpiresAt != 2_000_000_000 || s.Assets[0].Amount != ^uint64(0) {
		t.Fatal("protobuf field spelling lost spend evidence", c, s)
	}
	for _, suffix := range []string{
		`,"isSpent":true,"is_spent":false`,
		`,"isSpent":true,"IS_SPENT":true`,
		`,"arkTxid":"","ark_txid":""`,
		`,"expiresAt":-1`,
		`,"assets":[{"assetId":"a","asset_id":"a"}]`,
		`,"isSpent":"true"`,
	} {
		var dto indexerVtxo
		err := decodeObject([]byte("{"+base+suffix+"}"), &dto)
		if err == nil {
			_, err = dto.decode()
		}
		if err == nil {
			t.Fatal("ambiguous or invalid evidence admitted", suffix)
		}
	}
}

func TestIndexerMalformedAmountsAndIdentity(t *testing.T) {
	id := strings.Repeat("01", 32)
	for _, amount := range []string{`"-1"`, `1.5`, `"1e3"`, `"18446744073709551616"`, `"2100000000000001"`, `null`, `"\u0031"`} {
		var dto indexerVtxo
		data := fmt.Sprintf(`{"outpoint":{"txid":%q},"script":"51","amount":%s}`, id, amount)
		err := decodeObject([]byte(data), &dto)
		if err == nil {
			_, err = dto.decode()
		}
		if err == nil {
			t.Fatal("invalid amount admitted", amount)
		}
	}
	for _, point := range []string{`null`, `{}`, `{"txid":"1"}`, fmt.Sprintf(`{"txid":%q,"vout":4294967296}`, id)} {
		var dto indexerVtxo
		data := fmt.Sprintf(`{"outpoint":%s,"script":"51","amount":"1000"}`, point)
		err := decodeObject([]byte(data), &dto)
		if err == nil {
			_, err = dto.decode()
		}
		if err == nil {
			t.Fatal("invalid outpoint admitted", point)
		}
	}
}
