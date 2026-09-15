package game

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/shuffle"
	"github.com/btcsuite/btcd/btcec/v2"
)

func TestMutinynetConfigRoundTrip(t *testing.T) {
	c := testConfig(1)
	c.Wallet.Network = "mutinynet"
	c.ArkdURL, c.IndexerURL = "https://mutinynet.arkade.sh", "https://mutinynet.arkade.sh"
	c.EmulatorURL = "https://emulator.mutinynet.arkade.sh"
	c.DelegatorURL = "https://delegator.mutinynet.arkade.sh"
	encoded, err := encodeConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeConfig(encoded)
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("Mutinynet configuration changed: %+v, %v", got, err)
	}
}

func TestDelegatorConfigCompatibility(t *testing.T) {
	legacy := testConfig(1)
	before, err := encodeConfig(legacy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeConfig(before)
	if err != nil || !reflect.DeepEqual(got, legacy) {
		t.Fatal("legacy config changed", err)
	}
	delegated := legacy
	delegated.DelegatorURL = "https://delegator.example"
	encoded, err := encodeConfig(delegated)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(encoded, before) {
		t.Fatal("legacy configuration fields changed")
	}
	got, err = decodeConfig(encoded)
	if err != nil || !reflect.DeepEqual(got, delegated) {
		t.Fatal("delegator config lost", err)
	}
	for _, bad := range [][]byte{encoded[:len(encoded)-1], append(bytes.Clone(encoded), 0), append(bytes.Clone(before), 0, 0, 0, 0)} {
		if _, err := decodeConfig(bad); err == nil {
			t.Fatal("noncanonical delegator extension accepted")
		}
	}
	a, _ := serviceBinding(legacy)
	b, _ := serviceBinding(delegated)
	if a != b {
		t.Fatal("local delegator endpoint changed shared service binding")
	}
}

func TestCanonicalSetupCodecs(t *testing.T) {
	f := completeSetup(t)
	for _, log := range f.logs {
		for _, encoded := range log {
			event, err := DecodeEvent(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if event.Secrets != nil {
				defer event.Secrets.Destroy()
			}
			round, err := EncodeEvent(event)
			if err != nil || !bytes.Equal(encoded, round) {
				t.Fatal("event canonical roundtrip", err)
			}
			if fmt.Sprintf("%#v", event) != "<private game event>" {
				t.Fatal("private event diagnostics")
			}
			for _, bad := range [][]byte{nil, encoded[:len(encoded)-1], append(bytes.Clone(encoded), 0), bytes.Repeat([]byte{0}, len(encoded))} {
				if e, err := DecodeEvent(bad); err == nil {
					if e.Secrets != nil {
						_ = e.Secrets.Destroy()
					}
					t.Fatal("bad event encoding")
				}
			}
			bad := bytes.Clone(encoded)
			bad[0] ^= 1
			sum := sha256.Sum256(bad[:len(bad)-32])
			copy(bad[len(bad)-32:], sum[:])
			if _, err := DecodeEvent(bad); err == nil {
				t.Fatal("checksummed foreign domain")
			}
			if event.Message != nil {
				m, err := EncodeMessage(*event.Message)
				if err != nil {
					t.Fatal(err)
				}
				// Exercise every truncation, including each fixed crypto field and length.
				for i := range m {
					if _, err := DecodeMessage(m[:i]); err == nil {
						t.Fatalf("accepted message prefix %d", i)
					}
				}
				if _, err := DecodeMessage(append(bytes.Clone(m), 0)); err == nil {
					t.Fatal("message trailing bytes")
				}
				decoded, err := DecodeMessage(m)
				if err != nil {
					t.Fatal(err)
				}
				clear(m)
				a, _ := EncodeMessage(decoded)
				b, _ := EncodeMessage(*event.Message)
				if !bytes.Equal(a, b) {
					t.Fatal("decoder retained caller buffer")
				}
				mixed := *event.Message
				if mixed.Participant != nil {
					d := shuffle.Deck{}
					mixed.Deck = &d
				} else {
					mixed.WalletOwnership = []byte{1}
				}
				if _, err := EncodeMessage(mixed); err == nil {
					t.Fatal("mixed message payload")
				}
			}
		}
	}
	if _, err := DecodeMessage(make([]byte, MaxMessageBytes+1)); err == nil {
		t.Fatal("oversized message")
	}
	if _, err := DecodeEvent(make([]byte, MaxEventBytes+1)); err == nil {
		t.Fatal("oversized event")
	}
	// Decoder lengths must be checked before allocation, including config arrays.
	c, _ := encodeConfig(testConfig(1))
	for i := len(configDomain) + 2; i < len(c)-4; i++ {
		bad := bytes.Clone(c)
		binary.LittleEndian.PutUint32(bad[i:], 0xffffffff)
		_, _ = decodeConfig(bad)
	}
}

func TestOwnedStateAndStrictCommands(t *testing.T) {
	c := testConfig(1)
	g, log := initialized(t, c)
	c.Wallet.Receive.Script[0] ^= 1
	c.Wallet.CheckpointScript[0] ^= 1
	e, err := g.PrepareSession(context.Background(), nil, Input{Kind: StartSession, Terms: testTerms, RelayURL: "ws://host"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Secrets.Destroy()
	applyRecord(t, g, e, &log)
	_ = e.Secrets.Destroy()
	e.Invitation.RelayURL = "ws://changed"
	e.Message.Participant.PayoutScript[0] ^= 1
	if g.setup.invitation.RelayURL != "ws://host" || g.setup.local.Participant.PayoutScript[0] != 0x51 {
		t.Fatal("Apply retained caller ownership")
	}
	if _, err := g.setup.secrets.MarshalBinary(); err != nil {
		t.Fatal("caller destroyed reducer-owned secrets")
	}
	defer g.setup.secrets.Destroy()
	snap, _ := g.Snapshot()
	snap.Invitation.RelayURL = "ws://other"
	if g.setup.invitation.RelayURL != "ws://host" {
		t.Fatal("snapshot retained mutable state")
	}
	for _, input := range []Input{{}, {Kind: Bet}, {Kind: Progress, RelayURL: "ws://host"}, {Kind: JoinSession}, {Kind: Progress, Bet: covenant.BettingAction{Kind: covenant.Check}}, {Kind: Concede}, {Kind: ClaimTimeout}} {
		if _, err := g.Decide(input); err == nil {
			t.Fatal("invalid setup command")
		}
	}
	before := g.nextEvent
	e2 := g.NewEvent(SessionOpened)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.ApplyContext(ctx, e2); !errors.Is(err, context.Canceled) || g.nextEvent != before {
		t.Fatal("cancelled Apply mutated game")
	}
	// An explicit abort is legal in every pre-lock setup state for each reference
	// abort reason; it is not legal in Init or after an already recorded abort.
	for _, reason := range []string{"invalid_peer_evidence", "initial_deadline_expired", "funding_unavailable"} {
		a := replayBytes(t, g.config, log)
		abort := a.NewEvent(SetupAborted)
		abort.FailureCode = reason
		if err := a.Apply(abort); err != nil {
			t.Fatal(err)
		}
		step, err := a.Decide(Input{Kind: Progress})
		if err != nil || step.Kind != Finished || step.Outcome.Kind != Aborted {
			t.Fatal("abort outcome")
		}
		snapshot, _ := a.Snapshot()
		if snapshot.Invitation != nil {
			t.Fatal("aborted invitation projection")
		}
		if err := a.Apply(a.NewEvent(SessionOpened)); err == nil {
			t.Fatal("resumed aborted session")
		}
	}
}

func TestDuplicateAndCancellingParticipantAdmission(t *testing.T) {
	f := completeSetup(t)
	g := replayBytes(t, f.configs[0], f.logs[0][:3])
	recorded, err := DecodeEvent(f.logs[1][1])
	if err != nil {
		t.Fatal(err)
	}
	defer recorded.Secrets.Destroy()
	offer := *recorded.Message
	localSecret, err := g.setup.secrets.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(localSecret)
	secretBE := bytes.Clone(localSecret[2:34])
	slices.Reverse(secretBE)
	defer clear(secretBE)
	for _, kind := range []string{"duplicate wallet", "duplicate identity", "duplicate shuffle", "cancelling shuffle"} {
		t.Run(kind, func(t *testing.T) {
			m := offer
			p := *m.Participant
			p.PayoutScript = bytes.Clone(p.PayoutScript)
			m.Participant = &p
			keyBytes := bytes.Repeat([]byte{0}, 32)
			keyBytes[31] = 12
			switch kind {
			case "duplicate wallet":
				m.Participant.SigningKey = g.setup.local.Participant.SigningKey
			case "duplicate identity":
				m.Identity = g.setup.local.Identity
			case "duplicate shuffle":
				keyBytes = bytes.Clone(secretBE)
			case "cancelling shuffle":
				var scalar btcec.ModNScalar
				scalar.SetByteSlice(secretBE)
				scalar.Negate()
				v := scalar.Bytes()
				scalar.Zero()
				keyBytes = bytes.Clone(v[:])
			}
			defer clear(keyBytes)
			binding, err := ownershipContext(g.setup.invitation, m.Sender, m.Identity, p)
			if err != nil {
				t.Fatal(err)
			}
			entropy := append(bytes.Clone(keyBytes), bytes.Repeat([]byte{1}, 32)...)
			defer clear(entropy)
			sk, key, proof, err := shuffle.GenerateKey(context.Background(), bytes.NewReader(entropy), binding)
			if err != nil {
				t.Fatal(err)
			}
			defer sk.Destroy()
			m.Participant.EncryptionKey = key
			m.Ownership = &proof
			// Each rejection starts with a valid context-specific ownership proof.
			if _, err := validateKeys(g.config, g.setup.invitation, m, covenant.Player2, nil); err != nil {
				t.Fatalf("incomplete fixture: %v", err)
			}
			e := g.NewEvent(MessageReceived)
			e.Message = &m
			cursor := g.nextEvent
			if err := g.Apply(e); err == nil {
				t.Fatal("accepted duplicate/cancelling participant")
			}
			if g.nextEvent != cursor {
				t.Fatal("advanced cursor")
			}
		})
	}
}

func TestSecretsConcurrentDestruction(t *testing.T) {
	_, s, err := CreateInvitation(context.Background(), nil, testConfig(1), testTerms, "ws://host")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			for range 30 {
				b, err := s.MarshalBinary()
				if err != nil && !errors.Is(err, ErrSecrets) {
					t.Error(err)
				}
				clear(b)
			}
		})
	}
	wg.Go(func() { _ = s.Destroy() })
	wg.Wait()
	if _, err := s.MarshalBinary(); !errors.Is(err, ErrSecrets) {
		t.Fatal("secret remained usable")
	}
}

func FuzzSetupDecoders(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("arkpg1:00"))
	f.Add(bytes.Repeat([]byte{0xff}, 200))
	c, _ := encodeConfig(testConfig(1))
	f.Add(c)
	inv, s, err := CreateInvitation(context.Background(), nil, testConfig(1), testTerms, "ws://host")
	if err != nil {
		f.Fatal(err)
	}
	defer s.Destroy()
	ib, _ := invitationBinary(inv)
	f.Add(ib)
	m, _ := s.KeyMessage(testConfig(1), inv, covenant.Player1)
	mb, _ := EncodeMessage(m)
	f.Add(mb)
	event, err := EncodeEvent(Event{Kind: SessionPrepared, SessionID: inv.SessionID, Role: covenant.Player1, Invitation: &inv, Secrets: s, Message: &m})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(event)
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > MaxEventBytes+1 {
			return
		}
		// Let checksum-valid mutations reach branch decoding and allocation
		// bounds, rather than spending the entire event corpus on SHA rejection.
		if bytes.HasPrefix(b, []byte(eventDomain)) && len(b) >= 32 {
			b = bytes.Clone(b)
			sum := sha256.Sum256(b[:len(b)-32])
			copy(b[len(b)-32:], sum[:])
		}
		if m, err := DecodeMessage(b); err == nil {
			out, err := EncodeMessage(m)
			if err != nil || !bytes.Equal(out, b) {
				t.Fatal("message canonicality")
			}
		}
		if e, err := DecodeEvent(b); err == nil {
			if e.Secrets != nil {
				defer e.Secrets.Destroy()
			}
			out, err := EncodeEvent(e)
			defer clear(out)
			if err != nil || !bytes.Equal(out, b) {
				t.Fatal("event canonicality")
			}
		}
		if inv, err := decodeInvitationBinary(b); err == nil {
			out, err := invitationBinary(inv)
			if err != nil || !bytes.Equal(out, b) {
				t.Fatal("invitation canonicality")
			}
		}
		if c, err := decodeConfig(b); err == nil {
			out, err := encodeConfig(c)
			if err != nil || !bytes.Equal(out, b) {
				t.Fatal("config canonicality")
			}
		}
		if s, err := DecodeSessionSecrets(b); err == nil {
			defer s.Destroy()
			out, err := s.MarshalBinary()
			defer clear(out)
			if err != nil || !bytes.Equal(out, b) {
				t.Fatal("secret canonicality")
			}
		}
	})
}
