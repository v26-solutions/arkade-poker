package game

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestJournalStartDoesNotPersistRuntimeConfig(t *testing.T) {
	c := testConfig(1)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Destroy()
	step, err := g.Decide(Input{Kind: Progress})
	if err != nil || step.Event == nil || step.Event.Config != nil {
		t.Fatal("journal start includes configuration", err)
	}
	before, err := EncodeEvent(*step.Event)
	if err != nil {
		t.Fatal(err)
	}
	c.ArkdURL = "https://other-ark.example"
	c.DelegatorURL = "https://delegate.example"
	c.Wallet.Network = "mutinynet"
	c.Wallet.ArkSigningKey = publicScalar(7)
	c.Wallet.Receive = testConfig(2).Wallet.Receive
	other, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Destroy()
	step, err = other.Decide(Input{Kind: Progress})
	if err != nil {
		t.Fatal(err)
	}
	after, err := EncodeEvent(*step.Event)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("runtime settings affect persisted journal start", err)
	}
	if err := step.Event.CheckWallet(testConfig(2).Wallet.WalletPublicKey); !errors.Is(err, ErrWalletIdentity) {
		t.Fatal("journal lost wallet binding", err)
	}
}

func TestLegacyJournalUsesRuntimeConfig(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "delegated"}[delegated], func(t *testing.T) {
			old := testConfig(1)
			if delegated {
				old.DelegatorURL = "https://old-delegate.example"
			}
			// Recreate the canonical pre-change Configured event and its chain.
			configBytes, err := encodeConfig(old)
			if err != nil {
				t.Fatal(err)
			}
			event := Event{Kind: Configured, Config: &old, Previous: digest("", configBytes)}
			raw, err := EncodeEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			log := &journalLog{records: [][]byte{raw}}
			current := old
			current.ArkdURL = "https://current-ark.example"
			current.DelegatorURL = "https://current-delegate.example"
			current.Wallet.Receive = testConfig(2).Wallet.Receive
			j, err := loadJournal(context.Background(), current, log)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { j.game.Destroy() }()
			if !equalConfig(j.game.config, current) || !bytes.Equal(log.records[0], raw) {
				t.Fatal("legacy snapshot replaced startup settings or was rewritten")
			}
			prepared, err := j.game.PrepareSession(context.Background(), nil, Input{Kind: StartSession, Terms: testTerms, RelayURL: "ws://relay.example"})
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Secrets.Destroy()
			if err := j.append(context.Background(), prepared); err != nil {
				t.Fatal(err)
			}
			restored, err := loadJournal(context.Background(), current, log)
			if err != nil {
				t.Fatal("same-runtime recovery", err)
			}
			defer restored.game.Destroy()
			if _, err := loadJournal(context.Background(), old, log); err == nil {
				t.Fatal("incompatible payout script accepted after game started")
			}
		})
	}
}
