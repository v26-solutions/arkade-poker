package game

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// Independently encoded fixture with public test keys and deterministic bytes.
const sharedInvitationFixture = "arkpg1:iCeIJ_QDoI0GAAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh95vmZ--dy7rFWgYpXOhwsHApv82y3OKNlZ8oFbFvgXmCAhIiMkJSYnKCkqKywtLi8wMTIzNDU2Nzg5Ojs8PT4_DXdzczovL25vcy5sb2w4Wfq0YppFfA"
const canonicalInvitationFixture = "61726b6164652d706f6b65722f696e7669746174696f6e00010088130000000000008813000000000000f401000000000000a086010000000000000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f0d0000007773733a2f2f6e6f732e6c6f6c3859fab4629a457cc2bb29fe1a80d971b619218b328ed0f5dc15f9e4248e7317"
const invitationSessionFixture = "7ce7add0716d960af8d32801f96ef91c3b69c27d6f6e2e795c81d57fe4e3a97b"

func TestCompactInvitationVector(t *testing.T) {
	inv, err := DecodeInvitation(sharedInvitationFixture)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Terms != (Terms{5000, 5000, 500, 100000}) || inv.RelayURL != "wss://nos.lol" || inv.CreatorTransportKey != publicScalar(1) {
		t.Fatal("invitation fields changed")
	}
	for i := range 32 {
		if inv.ServiceBinding[i] != byte(i) || inv.JoinCapability[i] != byte(i+32) {
			t.Fatal("service binding or join capability changed")
		}
	}
	if hex.EncodeToString(inv.SessionID[:]) != invitationSessionFixture {
		t.Fatal("compact sharing changed the session ID")
	}
	b, err := invitationBinary(inv)
	if err != nil || hex.EncodeToString(b) != canonicalInvitationFixture {
		t.Fatal("compact sharing changed the committed internal bytes", err)
	}
	stored, err := decodeInvitationBinary(b)
	if err != nil || stored != inv {
		t.Fatal("shared and stored invitation differ", err)
	}
	text, err := EncodeInvitation(inv)
	if err != nil || text != sharedInvitationFixture || len(text) != 177 {
		t.Fatal("compact sharing vector changed", err)
	}
	inv.SessionID[0] ^= 1
	if _, err := EncodeInvitation(inv); err == nil {
		t.Fatal("accepted mismatched session ID")
	}
}

func TestCompactInvitationRoundTrip(t *testing.T) {
	base, err := DecodeInvitation(sharedInvitationFixture)
	if err != nil {
		t.Fatal(err)
	}
	relays := []string{
		"ws://localhost:7777/custom/path?foo=BAR&x=%2f",
		"wss://Relay.Example:443/路径?query=é",
		"wss://relay.example/" + strings.Repeat("x", 127-len("wss://relay.example/")),
		"wss://relay.example/" + strings.Repeat("x", 128-len("wss://relay.example/")),
		"wss://relay.example/" + strings.Repeat("x", 2048-len("wss://relay.example/")),
	}
	for _, amount := range []int64{1, 127, 128, 16383, 16384, maxMoney / 6} {
		for i, relay := range relays {
			t.Run(fmt.Sprintf("amount_%d/relay_%d", amount, i), func(t *testing.T) {
				inv := base
				inv.Terms = Terms{amount, amount, 1, amount}
				inv.RelayURL = relay
				inv.SessionID, err = invitationID(inv)
				if err != nil {
					t.Fatal(err)
				}
				text, err := EncodeInvitation(inv)
				if err != nil {
					t.Fatal(err)
				}
				got, err := DecodeInvitation(text)
				if err != nil || got != inv {
					t.Fatal("invitation did not roundtrip exactly", err)
				}
			})
		}
	}
	for name, mutate := range map[string]func(*Invitation){
		"zero amount":     func(i *Invitation) { i.Terms.Stake = 0 },
		"negative amount": func(i *Invitation) { i.Terms.Bond = -1 },
		"excess amount":   func(i *Invitation) { i.Terms.MaxWager = maxMoney },
		"bet limits":      func(i *Invitation) { i.Terms.MinBet = i.Terms.MaxWager + 1 },
		"invalid key":     func(i *Invitation) { i.CreatorTransportKey = [32]byte{} },
		"long relay":      func(i *Invitation) { i.RelayURL = "wss://" + strings.Repeat("x", 2043) },
		"invalid UTF8":    func(i *Invitation) { i.RelayURL = "wss://host/\xff" },
	} {
		t.Run(name, func(t *testing.T) {
			inv := base
			mutate(&inv)
			if _, err := EncodeInvitation(inv); err == nil {
				t.Fatal("encoded invalid invitation")
			}
		})
	}
}

func TestCompactInvitationRejectsMalformed(t *testing.T) {
	payload := strings.TrimPrefix(sharedInvitationFixture, "arkpg1:")
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"old hex format":        "arkpg1:" + canonicalInvitationFixture,
		"unknown version":       "arkpg2:" + payload,
		"missing prefix":        payload,
		"padding":               sharedInvitationFixture + "==",
		"CRLF":                  "arkpg1:" + payload[:20] + "\r\n" + payload[20:],
		"space":                 sharedInvitationFixture + " ",
		"standard alphabet":     "arkpg1:" + base64.RawStdEncoding.EncodeToString(b),
		"nonzero trailing bits": sharedInvitationFixture[:len(sharedInvitationFixture)-1] + "B",
		"oversized":             "arkpg1:" + strings.Repeat("A", 8192),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeInvitation(text); err == nil {
				t.Fatal("accepted malformed invitation")
			}
		})
	}
	reject := func(b []byte) {
		t.Helper()
		if _, err := DecodeInvitation("arkpg1:" + base64.RawURLEncoding.EncodeToString(b)); err == nil {
			t.Fatal("accepted malformed payload")
		}
	}
	// Every prefix truncates a field or checksum; every bit is covered by the
	// checksum or structural validation, including each of the three 32-byte fields.
	for i := range b {
		reject(b[:i])
		for bit := range 8 {
			bad := bytes.Clone(b)
			bad[i] ^= 1 << bit
			reject(bad)
		}
	}
	// Overlong forms preserve the decoded values and their valid checksum.
	for _, end := range []int{1, 3, 5, 8, 105} {
		bad := append(bytes.Clone(b[:end+1]), 0)
		bad[end] |= 0x80
		bad = append(bad, b[end+1:]...)
		reject(bad)
	}
	for _, amount := range [][]byte{
		bytes.Repeat([]byte{0x80}, 11),
		binary.AppendUvarint(nil, ^uint64(0)),
		binary.AppendUvarint(nil, 1<<63),
	} {
		reject(append(bytes.Clone(amount), b[2:]...))
	}
	// The fixture's relay length is at offset 105, before its 13 relay bytes.
	for _, length := range []uint64{0, 12, 14, 2049, ^uint64(0)} {
		bad := binary.AppendUvarint(bytes.Clone(b[:105]), length)
		reject(append(bad, b[106:]...))
	}
	// Extra data before the unchanged checksum must not be ignored.
	bad := append(bytes.Clone(b[:len(b)-8]), 0)
	reject(append(bad, b[len(b)-8:]...))
}

func FuzzCompactInvitation(f *testing.F) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sharedInvitationFixture, "arkpg1:"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b)
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 128))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > maxSharedInvitationBytes+1 {
			return
		}
		// Mutate binary fields directly so base64 validity cannot mask parser bugs.
		text := "arkpg1:" + base64.RawURLEncoding.EncodeToString(b)
		if inv, err := DecodeInvitation(text); err == nil {
			got, err := EncodeInvitation(inv)
			if err != nil || got != text {
				t.Fatal("accepted noncanonical invitation", err)
			}
		}
	})
}
