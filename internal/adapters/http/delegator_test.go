package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDelegatorInfo(t *testing.T) {
	const pubkey = "032903b15efe236d9609da10e536fb32cdf1d144778797bbf32a9b94e86601be6a"
	d, err := NewDelegator("https://delegate.example/")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	body := `{"pubkey":"` + pubkey + `","fee":"0","delegatorAddress":"ignored","delegateAddress":"ignored"}`
	status := http.StatusOK
	d.client.Transport = streamRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.String() != "https://delegate.example/v1/delegator/info" || r.Body != nil || r.Header.Get("Accept") != "application/json" {
			t.Fatal("unexpected delegator request")
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("missing request deadline")
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	info, err := d.Info(context.Background())
	if err != nil || info.PubKey != pubkey {
		t.Fatal("delegator public key", info, err)
	}
	for name, input := range map[string]string{
		"missing": `{}`, "empty": `{"pubkey":""}`, "null": `null`, "array": `[]`,
		"wrong type": `{"pubkey":42}`, "short": `{"pubkey":"02"}`,
		"duplicate": `{"pubkey":"` + pubkey + `","pubkey":"` + pubkey + `"}`,
		"trailing":  `{"pubkey":"` + pubkey + `"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			body = input
			if _, err := d.Info(context.Background()); err == nil {
				t.Fatal("invalid info accepted")
			}
		})
	}
	status = http.StatusServiceUnavailable
	if _, err := d.Info(context.Background()); !errors.Is(err, HTTPStatusError(status)) {
		t.Fatal("service failure lost", err)
	}
	d.client.Transport = streamRoundTrip(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Info(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}
