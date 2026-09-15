//go:build !js

// Package grpc adapts the upstream native transport clients to application ports.
package grpc

import (
	"context"
	"errors"
	"math"

	"arkade-poker/go/internal/adapters/servicedata"
	"arkade-poker/go/internal/ports"
	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	"github.com/arkade-os/arkd/pkg/client-lib/client"
)

type Arkd struct{ client clientlib.Client }

func NewArkd(endpoint string) (*Arkd, error) {
	u, err := ports.Endpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("gRPC endpoint must not have a path")
	}
	c, err := client.NewClient(u.Scheme+"://"+u.Host, "arkade-poker-go/1")
	if err != nil {
		return nil, err
	}
	return &Arkd{c}, nil
}

func (a *Arkd) Info(ctx context.Context) (ports.ArkInfo, error) {
	i, err := a.client.GetInfo(ctx)
	if err != nil {
		return ports.ArkInfo{}, err
	}
	if i == nil || i.Dust > math.MaxInt64 {
		return ports.ArkInfo{}, errors.New("invalid Arkd info")
	}
	return ports.ArkInfo{Network: i.Network, Signer: i.SignerPubKey, Forfeit: i.ForfeitPubKey,
		CheckpointScript: i.CheckpointTapscript, ExitDelay: i.UnilateralExitDelay,
		Dust: int64(i.Dust), MinVtxo: i.VtxoMinAmount, MaxVtxo: i.VtxoMaxAmount,
		Version: i.Version, Digest: i.Digest}, nil
}

func (a *Arkd) Submit(ctx context.Context, b ports.Bundle) (ports.Submitted, error) {
	if err := servicedata.Bundle(b); err != nil {
		return ports.Submitted{}, err
	}
	id, ark, checkpoints, err := a.client.SubmitTx(ctx, b.Ark, b.Checkpoints)
	if err != nil {
		return ports.Submitted{}, err
	}
	result := ports.Submitted{TxID: id, Bundle: ports.Bundle{Ark: ark, Checkpoints: checkpoints}}
	if len(id) > 128 {
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
	return a.client.FinalizeTx(ctx, id, checkpoints)
}
func (a *Arkd) Close() error { a.client.Close(); return nil }

var _ ports.Arkd = (*Arkd)(nil)
