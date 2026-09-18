package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"arkade-poker/go/internal/adapters/servicedata"
	"arkade-poker/go/internal/ports"
	enclave "github.com/ArkLabsHQ/enclave/client"
)

type Emulator struct{ *Client }

// NewEmulator delegates HTTPS attestation verification to the enclave client.
// Browsers verify the document but rely on browser HTTPS validation: Fetch does
// not expose the peer certificate for the enclave client's TLS key pinning.
func NewEmulator(endpoint, expectedPCR0 string) (*Emulator, error) {
	c, err := New(endpoint)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(c.base, "https://") {
		return nil, errors.New("attested emulator requires an https:// endpoint")
	}
	verified, err := enclave.New(c.base, enclave.Options{ExpectedPCR0: expectedPCR0})
	if err != nil {
		return nil, err
	}
	c.client.Transport = attestedTransport{verified}
	return &Emulator{c}, nil
}

type attestedTransport struct{ client *enclave.Client }

func (t attestedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.client.Do(req.Context(), req)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode:    response.StatusCode,
		Header:        response.Header,
		Body:          io.NopCloser(bytes.NewReader(response.Body)),
		ContentLength: int64(len(response.Body)),
		Request:       req,
	}, nil
}

// NewUnverifiedEmulator is for transport fixtures and the local regtest harness.
// Application hosts must use NewEmulator.
func NewUnverifiedEmulator(endpoint string) (*Emulator, error) {
	c, err := New(endpoint)
	if err != nil {
		return nil, err
	}
	return &Emulator{c}, nil
}
func (e *Emulator) Info(ctx context.Context) (ports.EmulatorInfo, error) {
	var i struct {
		Signer  string `json:"signerPubkey"`
		Version string `json:"version"`
	}
	err := e.request(ctx, "GET", "/v1/info", nil, &i)
	return ports.EmulatorInfo{Signer: i.Signer, Version: i.Version}, err
}
func (e *Emulator) Sign(ctx context.Context, b ports.Bundle) (ports.Bundle, error) {
	if err := servicedata.Bundle(b); err != nil {
		return ports.Bundle{}, err
	}
	request := struct {
		Ark         string   `json:"arkTx"`
		Checkpoints []string `json:"checkpointTxs"`
	}{b.Ark, b.Checkpoints}
	var response struct {
		Ark         string   `json:"signedArkTx"`
		Checkpoints []string `json:"signedCheckpointTxs"`
	}
	err := e.request(ctx, "POST", "/v1/tx", request, &response)
	if err != nil {
		return ports.Bundle{}, err
	}
	result := ports.Bundle{Ark: response.Ark, Checkpoints: response.Checkpoints}
	if err := servicedata.Bundle(result); err != nil {
		return ports.Bundle{}, err
	}
	return result, nil
}

var _ ports.Emulator = (*Emulator)(nil)
