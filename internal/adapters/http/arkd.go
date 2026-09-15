package http

import (
	"arkade-poker/go/internal/adapters/servicedata"
	"arkade-poker/go/internal/ports"
	"context"
	"errors"
)

type Arkd struct{ *Client }

func NewArkd(endpoint string) (*Arkd, error) {
	c, err := New(endpoint)
	if err != nil {
		return nil, err
	}
	return &Arkd{c}, nil
}

func (a *Arkd) Info(ctx context.Context) (ports.ArkInfo, error) {
	var i struct {
		Network    string  `json:"network"`
		Signer     string  `json:"signerPubkey"`
		Forfeit    string  `json:"forfeitPubkey"`
		Checkpoint string  `json:"checkpointTapscript"`
		ExitDelay  decimal `json:"unilateralExitDelay"`
		Dust       decimal `json:"dust"`
		Min        decimal `json:"vtxoMinAmount"`
		Max        decimal `json:"vtxoMaxAmount"`
		Version    string  `json:"version"`
		Digest     string  `json:"digest"`
	}
	err := a.request(ctx, "GET", "/v1/info", nil, &i)
	if err != nil {
		return ports.ArkInfo{}, err
	}
	if i.Dust < 0 {
		return ports.ArkInfo{}, errors.New("invalid Arkd info")
	}
	return ports.ArkInfo{Network: i.Network, Signer: i.Signer, Forfeit: i.Forfeit, CheckpointScript: i.Checkpoint,
		ExitDelay: int64(i.ExitDelay), Dust: int64(i.Dust), MinVtxo: int64(i.Min), MaxVtxo: int64(i.Max),
		Version: i.Version, Digest: i.Digest}, err
}
func (a *Arkd) Submit(ctx context.Context, b ports.Bundle) (ports.Submitted, error) {
	if err := servicedata.Bundle(b); err != nil {
		return ports.Submitted{}, err
	}
	request := struct {
		Ark         string   `json:"signedArkTx"`
		Checkpoints []string `json:"checkpointTxs"`
	}{b.Ark, b.Checkpoints}
	var response struct {
		ID          string   `json:"arkTxid"`
		Ark         string   `json:"finalArkTx"`
		Checkpoints []string `json:"signedCheckpointTxs"`
	}
	err := a.request(ctx, "POST", "/v1/tx/submit", request, &response)
	if err != nil {
		return ports.Submitted{}, err
	}
	result := ports.Submitted{TxID: response.ID, Bundle: ports.Bundle{Ark: response.Ark, Checkpoints: response.Checkpoints}}
	if len(response.ID) > 128 {
		return ports.Submitted{}, errors.New("service transaction identity exceeds limit")
	}
	if err := servicedata.Bundle(result.Bundle); err != nil {
		return ports.Submitted{}, err
	}
	return result, nil
}
func (a *Arkd) Finalize(ctx context.Context, id string, checkpoints []string) error {
	if err := servicedata.Strings(id, checkpoints); err != nil {
		return err
	}
	if len(id) > 128 {
		return errors.New("service transaction identity exceeds limit")
	}
	request := struct {
		ID          string   `json:"arkTxid"`
		Checkpoints []string `json:"finalCheckpointTxs"`
	}{id, checkpoints}
	var response struct{}
	return a.request(ctx, "POST", "/v1/tx/finalize", request, &response)
}

var _ ports.Arkd = (*Arkd)(nil)
