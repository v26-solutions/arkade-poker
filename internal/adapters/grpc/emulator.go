//go:build !js

package grpc

import (
	"context"
	"errors"

	"arkade-poker/go/internal/adapters/servicedata"
	"arkade-poker/go/internal/ports"
	client "github.com/arkade-os/emulator/pkg/client"
	gogrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type Emulator struct {
	client client.TransportClient
	conn   *gogrpc.ClientConn
}

func NewEmulator(endpoint string) (*Emulator, error) {
	u, err := ports.Endpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("gRPC endpoint must not have a path")
	}
	var creds credentials.TransportCredentials = credentials.NewTLS(nil)
	if u.Scheme == "http" {
		creds = insecure.NewCredentials()
	}
	conn, err := gogrpc.NewClient(u.Host, gogrpc.WithTransportCredentials(creds),
		gogrpc.WithDefaultCallOptions(gogrpc.MaxCallRecvMsgSize(20<<20)))
	if err != nil {
		return nil, err
	}
	return &Emulator{client.NewGRPCClient(conn), conn}, nil
}

func (e *Emulator) Info(ctx context.Context) (ports.EmulatorInfo, error) {
	i, err := e.client.GetInfo(ctx)
	if err != nil {
		return ports.EmulatorInfo{}, err
	}
	if i == nil {
		return ports.EmulatorInfo{}, errors.New("invalid emulator info")
	}
	return ports.EmulatorInfo{Signer: i.SignerPublicKey, Version: i.Version}, nil
}
func (e *Emulator) Sign(ctx context.Context, b ports.Bundle) (ports.Bundle, error) {
	if err := servicedata.Bundle(b); err != nil {
		return ports.Bundle{}, err
	}
	ark, checkpoints, err := e.client.SubmitTx(ctx, b.Ark, b.Checkpoints)
	if err != nil {
		return ports.Bundle{}, err
	}
	result := ports.Bundle{Ark: ark, Checkpoints: checkpoints}
	if err := servicedata.Bundle(result); err != nil {
		return ports.Bundle{}, err
	}
	return result, nil
}
func (e *Emulator) Close() error { return e.conn.Close() }

var _ ports.Emulator = (*Emulator)(nil)
