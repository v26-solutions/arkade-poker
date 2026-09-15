package http

import (
	"arkade-poker/go/internal/adapters/servicedata"
	"arkade-poker/go/internal/ports"
	"context"
)

type Emulator struct{ *Client }

func NewEmulator(endpoint string) (*Emulator, error) {
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
