// Package ports defines the host boundaries used by the shared application.
package ports

import "context"

// Bundle contains the exact base64 PSBTs prepared for signing/submission. A
// returned signed bundle is not evidence of service acceptance. The wallet and
// game retain all non-signature fields and checkpoint linkage when comparing it.
type Bundle struct {
	Ark         string
	Checkpoints []string
}
type Submitted struct {
	TxID   string
	Bundle Bundle
}

// Info contains public service data, before wallet policy admission.
type ArkInfo struct {
	Network, Signer, Forfeit, CheckpointScript string
	ExitDelay, Dust, MinVtxo, MaxVtxo          int64
	Version, Digest                            string
}
type EmulatorInfo struct{ Signer, Version string }
type DelegatorInfo struct{ PubKey string }

type Delegator interface {
	Info(context.Context) (DelegatorInfo, error)
	Close() error
}

type Arkd interface {
	Info(context.Context) (ArkInfo, error)
	Submit(context.Context, Bundle) (Submitted, error)
	Finalize(context.Context, string, []string) error
	Close() error
}
type Emulator interface {
	Info(context.Context) (EmulatorInfo, error)
	// Sign calls /v1/tx. The caller must select a leaf with the emulator before
	// the participant; the endpoint otherwise also submits/finalizes with Arkd.
	Sign(context.Context, Bundle) (Bundle, error)
	Close() error
}
