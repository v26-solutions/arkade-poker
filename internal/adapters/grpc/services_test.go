//go:build !js

package grpc

import (
	"context"
	"net"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	web "arkade-poker/go/internal/adapters/http"
	"arkade-poker/go/internal/adapters/servicedata"
	"arkade-poker/go/internal/ports"
	arkv1 "github.com/arkade-os/arkd/api-spec/protobuf/gen/ark/v1"
	emuv1 "github.com/arkade-os/emulator/api-spec/protobuf/gen/emulator/v1"
	"github.com/meshapi/grpc-api-gateway/gateway"
	rpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type arkServiceFixture struct {
	arkv1.UnimplementedArkServiceServer
	fail atomic.Bool
	t    *testing.T
}

func (s *arkServiceFixture) GetInfo(context.Context, *arkv1.GetInfoRequest) (*arkv1.GetInfoResponse, error) {
	if s.fail.Load() {
		return nil, status.Error(codes.Unavailable, "fixture unavailable")
	}
	return &arkv1.GetInfoResponse{Network: "regtest", SignerPubkey: "signer", ForfeitPubkey: "forfeit", CheckpointTapscript: "checkpoint", UnilateralExitDelay: 512, Dust: 330, VtxoMinAmount: 330, VtxoMaxAmount: -1, Version: "fixture", Digest: "digest"}, nil
}
func (s *arkServiceFixture) SubmitTx(_ context.Context, q *arkv1.SubmitTxRequest) (*arkv1.SubmitTxResponse, error) {
	if s.fail.Load() {
		return nil, status.Error(codes.Unavailable, "fixture unavailable")
	}
	if q.SignedArkTx != "exact-ark" || !reflect.DeepEqual(q.CheckpointTxs, []string{"cp-1", "cp-2"}) {
		s.t.Error("submit translation changed")
	}
	return &arkv1.SubmitTxResponse{ArkTxid: "identity", FinalArkTx: "returned-ark", SignedCheckpointTxs: []string{"returned-cp-1", "returned-cp-2"}}, nil
}
func (s *arkServiceFixture) FinalizeTx(_ context.Context, q *arkv1.FinalizeTxRequest) (*arkv1.FinalizeTxResponse, error) {
	if s.fail.Load() {
		return nil, status.Error(codes.Unavailable, "fixture unavailable")
	}
	if q.ArkTxid != "identity" || !reflect.DeepEqual(q.FinalCheckpointTxs, []string{"returned-cp-1", "returned-cp-2"}) {
		s.t.Error("finalize translation changed")
	}
	return &arkv1.FinalizeTxResponse{}, nil
}

type emulatorServiceFixture struct {
	emuv1.UnimplementedEmulatorServiceServer
	fail atomic.Bool
	t    *testing.T
}

func (s *emulatorServiceFixture) GetInfo(context.Context, *emuv1.GetInfoRequest) (*emuv1.GetInfoResponse, error) {
	if s.fail.Load() {
		return nil, status.Error(codes.Unavailable, "fixture unavailable")
	}
	return &emuv1.GetInfoResponse{SignerPubkey: "emulator", Version: "fixture"}, nil
}
func (s *emulatorServiceFixture) SubmitTx(_ context.Context, q *emuv1.SubmitTxRequest) (*emuv1.SubmitTxResponse, error) {
	if s.fail.Load() {
		return nil, status.Error(codes.Unavailable, "fixture unavailable")
	}
	if q.ArkTx != "exact-ark" || !reflect.DeepEqual(q.CheckpointTxs, []string{"cp-1", "cp-2"}) {
		s.t.Error("emulator request changed")
	}
	return &emuv1.SubmitTxResponse{SignedArkTx: "emulator-ark", SignedCheckpointTxs: []string{"emulator-cp-1", "emulator-cp-2"}}, nil
}

func TestServiceGeneratedGatewayParity(t *testing.T) {
	ark, emu := &arkServiceFixture{t: t}, &emulatorServiceFixture{t: t}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := rpc.NewServer()
	arkv1.RegisterArkServiceServer(s, ark)
	emuv1.RegisterEmulatorServiceServer(s, emu)
	go s.Serve(listener)
	defer s.Stop()
	conn, err := rpc.NewClient(listener.Addr().String(), rpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	arkMux, emuMux := gateway.NewServeMux(), gateway.NewServeMux()
	arkv1.RegisterArkServiceHandler(context.Background(), arkMux, conn)
	emuv1.RegisterEmulatorServiceHandler(context.Background(), emuMux, conn)
	ah, eh := httptest.NewServer(arkMux), httptest.NewServer(emuMux)
	defer ah.Close()
	defer eh.Close()
	na, err := NewArkd("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer na.Close()
	ne, err := NewEmulator("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer ne.Close()
	wa, _ := web.NewArkd(ah.URL)
	defer wa.Close()
	we, _ := web.NewEmulator(eh.URL)
	defer we.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := ports.Bundle{Ark: "exact-ark", Checkpoints: []string{"cp-1", "cp-2"}}
	oversized := []ports.Bundle{{Ark: strings.Repeat("x", servicedata.MaxBytes+1)}, {Checkpoints: make([]string, servicedata.MaxCheckpoints+1)}}
	var info ports.ArkInfo
	var submitted ports.Submitted
	for i, a := range []ports.Arkd{na, wa} {
		for _, bad := range oversized {
			if _, err := a.Submit(ctx, bad); err == nil {
				t.Fatal("submit size limit")
			}
			if len(bad.Checkpoints) > 256 {
				if err := a.Finalize(ctx, "id", bad.Checkpoints); err == nil {
					t.Fatal("finalize count limit")
				}
			}
		}
		v, err := a.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			info = v
		} else if !reflect.DeepEqual(info, v) {
			t.Fatal("info differs", info, v)
		}
		r, err := a.Submit(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			submitted = r
		} else if !reflect.DeepEqual(submitted, r) {
			t.Fatal("submit differs")
		}
		if err := a.Finalize(ctx, r.TxID, r.Bundle.Checkpoints); err != nil {
			t.Fatal(err)
		}
		ark.fail.Store(true)
		if _, err := a.Info(ctx); err == nil {
			t.Fatal("missing info error")
		}
		if _, err := a.Submit(ctx, input); err == nil {
			t.Fatal("missing submit error")
		}
		if err := a.Finalize(ctx, r.TxID, r.Bundle.Checkpoints); err == nil {
			t.Fatal("missing finalization error")
		}
		ark.fail.Store(false)
		cancelled, stop := context.WithCancel(ctx)
		stop()
		if _, err := a.Submit(cancelled, input); err == nil {
			t.Fatal("submit ignored cancellation")
		}
	}
	var emulatorInfo ports.EmulatorInfo
	var signed ports.Bundle
	for i, e := range []ports.Emulator{ne, we} {
		for _, bad := range oversized {
			if _, err := e.Sign(ctx, bad); err == nil {
				t.Fatal("emulator size limit")
			}
		}
		v, err := e.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			emulatorInfo = v
		} else if emulatorInfo != v {
			t.Fatal("emulator info differs")
		}
		r, err := e.Sign(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			signed = r
		} else if !reflect.DeepEqual(signed, r) {
			t.Fatal("emulator sign differs")
		}
		emu.fail.Store(true)
		if _, err := e.Info(ctx); err == nil {
			t.Fatal("missing emulator info error")
		}
		if _, err := e.Sign(ctx, input); err == nil {
			t.Fatal("missing emulator submit error")
		}
		emu.fail.Store(false)
		cancelled, stop := context.WithCancel(ctx)
		stop()
		if _, err := e.Sign(cancelled, input); err == nil {
			t.Fatal("emulator ignored cancellation")
		}
	}
}
