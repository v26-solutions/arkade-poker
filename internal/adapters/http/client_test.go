//go:build !js

package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkade-poker/go/internal/ports"
)

func TestGatewayInfoAndSubmissionWire(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Accept") != "application/json" {
			t.Error("missing JSON response negotiation")
		}
		if r.Method == http.MethodGet {
			if r.Header.Get("Content-Type") != "" {
				t.Error("bodyless read requests unnecessary CORS preflight")
			}
		} else if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing JSON request content type")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/info":
			w.Write([]byte(` {"network":"regtest","signerPubkey":"pub","unilateralExitDelay":"5","dust":"330","vtxoMinAmount":1,"vtxoMaxAmount":"-1"} `))
		case "/v1/tx/submit":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["signedArkTx"] != "exact-signed-psbt" || len(body["checkpointTxs"].([]any)) != 1 {
				t.Error("changed prepared work")
			}
			w.Write([]byte(`{"arkTxid":"id","finalArkTx":"returned","signedCheckpointTxs":["checkpoint"]}`))
		case "/v1/tx/finalize":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["arkTxid"] != "id" {
				t.Error("wrong finalization identity")
			}
			w.Write([]byte(`{}`))
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	a, err := NewArkd(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	i, err := a.Info(context.Background())
	if err != nil || i.ExitDelay != 5 || i.MaxVtxo != -1 {
		t.Fatalf("info: %+v %v", i, err)
	}
	s, err := a.Submit(context.Background(), ports.Bundle{Ark: "exact-signed-psbt", Checkpoints: []string{"prepared-checkpoint"}})
	if err != nil || s.Bundle.Ark != "returned" {
		t.Fatal("lost submit response", err)
	}
	if err := a.Finalize(context.Background(), s.TxID, s.Bundle.Checkpoints); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatal(paths)
	}
}

func TestMalformedGatewayData(t *testing.T) {
	for _, data := range []string{
		`null`, `[]`, `{}`, // empty info has no validated identity, handled by wallet
		`{"dust":"1.0"}`, `{"dust":"1e3"}`, `{"dust":9223372036854775808}`,
		`{"dust":330,"dust":331}`, `{"dust":330,"DUST":331}`,
		`{"dust":330} {}`, `{"outer":{"a":1,"a":2}}`,
	} {
		var out struct {
			Dust decimal `json:"dust"`
		}
		err := decodeObject([]byte(data), &out)
		if data == `{}` {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil {
			t.Errorf("accepted malformed data %s", data)
		}
	}
	for _, endpoint := range []string{"localhost:7070", "ftp://example.com", "http://user:secret@localhost", "http://localhost/#fragment", "http://localhost/?key=secret"} {
		if _, err := New(endpoint); err == nil {
			t.Error("accepted endpoint", endpoint)
		}
	}
}

func TestLimitsErrorsAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large":
			w.Write([]byte(`{"payload":"`))
			w.Write([]byte(strings.Repeat("x", 20<<20)))
			w.Write([]byte(`"}`))
		case "/failure":
			w.WriteHeader(503)
			w.Write([]byte("sensitive request data"))
		case "/redirect":
			http.Redirect(w, r, "/failure", 307)
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	c, _ := New(server.URL)
	for _, path := range []string{"/large", "/failure", "/redirect"} {
		var out struct{}
		err := c.request(context.Background(), "GET", path, nil, &out)
		if err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatal("response error handling", path, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out struct{}
	if err := c.request(ctx, "GET", "/", nil, &out); err == nil {
		t.Fatal("ignored cancellation")
	}
}
