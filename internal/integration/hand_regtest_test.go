//go:build !js && regtest_hand

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	native "arkade-poker/go/internal/adapters/grpc"
	gateway "arkade-poker/go/internal/adapters/http"
	"arkade-poker/go/internal/adapters/websocket"
	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/nostr"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/storage"
	"arkade-poker/go/internal/wallet"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/btcsuite/btcd/wire"
)

const fundingAmount int64 = 100000

type handReport struct {
	Scenario                     string
	Started                      time.Time
	ElapsedSeconds               float64
	Passed                       bool
	ArkdVersion, EmulatorVersion string
	Players                      [2]playerReport
	TerminalTxID                 string
	Settlement                   game.Settlement
}

type playerReport struct {
	Adapter, PublicKey, Address string
	FundedSatoshis              int64
	Records                     int
	Actions                     []string
	Payout                      string
	PayoutSatoshis              int64
	SweepTxID                   string
}

type livePlayer struct {
	key        *wallet.Key
	secret     [32]byte // Test ingress held only in RAM, also checked against saved records.
	wallet     *wallet.Wallet
	config     game.Config
	index      ports.Indexer
	subscriber ports.ScriptSubscriber
	peer       ports.PeerTransport
	log        storage.Log
	driver     *game.Driver
	inputs     chan game.Input
	updates    chan game.Update
}

// Run explicitly with make test-regtest-hands. Each scenario uses two fresh
// wallets and real services; it never installs a fixture ledger or peer.
func TestRegtestFullHands(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		allIn int
	}{
		{"ordinary", -1}, {"player1_all_in", 0}, {"player2_all_in", 1},
	} {
		t.Run(scenario.name, func(t *testing.T) { runLiveHand(t, scenario.name, scenario.allIn) })
		if t.Failed() {
			break
		} // Preserve the first failure and avoid funding more failed runs.
	}
}

func endpoint(t *testing.T, name, fallback string) string {
	t.Helper()
	s := os.Getenv(name)
	if s == "" {
		s = fallback
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		t.Fatalf("%s must be a loopback regtest endpoint", name)
	}
	return s
}

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newPlayer(t *testing.T, ctx context.Context, directory string, i int, report *handReport) *livePlayer {
	t.Helper()
	p := &livePlayer{inputs: make(chan game.Input, 1), updates: make(chan game.Update, 128)}
	arkURL := endpoint(t, "POKER_ARKD_URL", "http://localhost:7070")
	emuURL := endpoint(t, "POKER_EMULATOR_URL", "http://localhost:7073")
	indexURL := endpoint(t, "POKER_INDEXER_URL", arkURL)
	var ark ports.Arkd
	var emu ports.Emulator
	var err error
	if i == 0 {
		report.Players[i].Adapter = "native gRPC"
		ark, err = native.NewArkd(arkURL)
		require(t, err)
		emu, err = native.NewEmulator(emuURL)
		require(t, err)
		index, e := native.NewIndexer(indexURL)
		require(t, e)
		p.index, p.subscriber = index, index
	} else {
		report.Players[i].Adapter = "HTTP/SSE on native Go (not WASM)"
		ark, err = gateway.NewArkd(arkURL)
		require(t, err)
		emu, err = gateway.NewEmulator(emuURL)
		require(t, err)
		index, e := gateway.NewIndexer(indexURL)
		require(t, e)
		p.index, p.subscriber = index, index
	}
	t.Cleanup(func() { p.index.Close(); emu.Close(); ark.Close() })
	info, err := ark.Info(ctx)
	require(t, err)
	if info.Network != "regtest" {
		t.Fatal("headless test requires regtest")
	}
	emuInfo, err := emu.Info(ctx)
	require(t, err)
	if i == 0 {
		report.ArkdVersion, report.EmulatorVersion = info.Version, emuInfo.Version
	}
	for p.key == nil {
		_, err = rand.Read(p.secret[:])
		require(t, err)
		p.key, _ = wallet.ParseKey(hex.EncodeToString(p.secret[:]), "")
	}
	t.Cleanup(func() { p.key.Destroy(); clear(p.secret[:]) })
	p.wallet, err = wallet.New(ctx, wallet.Services{Arkd: ark, Emulator: emu, Indexer: p.index}, p.key, wallet.Config{Network: "regtest"})
	require(t, err)
	cfg, err := p.wallet.Config()
	require(t, err)
	p.config = game.Config{Wallet: cfg, ArkdURL: arkURL, EmulatorURL: emuURL, IndexerURL: indexURL}
	report.Players[i].PublicKey = fmt.Sprintf("%x", p.key.PublicKey())
	report.Players[i].Address = cfg.Receive.Address
	p.peer, err = nostr.New(nostr.Config{Dial: websocket.Dial})
	require(t, err)
	p.log, err = storage.Open(ctx, filepath.Join(directory, fmt.Sprintf("player%d", i+1)), p.key.PublicKey())
	require(t, err)
	p.driver, err = game.NewDriver(game.DriverConfig{Game: p.config, Log: p.log, Wallet: p.wallet, Indexer: p.index, Subscriptions: p.subscriber, Transport: p.peer})
	require(t, err)
	t.Cleanup(func() { p.driver.Close() })
	return p
}

func waitFor(t *testing.T, ctx context.Context, description string, check func() (bool, error)) {
	t.Helper()
	wait, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		ok, err := check()
		require(t, err)
		if ok {
			return
		}
		select {
		case <-wait.Done():
			t.Fatalf("waiting for %s: %v", description, wait.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func fundPlayer(t *testing.T, ctx context.Context, p *livePlayer, r *playerReport) {
	t.Helper()
	before, err := p.index.Vtxos(ctx, ports.VtxoQuery{Script: p.config.Wallet.Receive.Script})
	require(t, err)
	if len(before) != 0 {
		t.Fatal("new test wallet already has history")
	}
	command := exec.CommandContext(ctx, "node", "../../scripts/fund-regtest.mjs", r.Address, fmt.Sprint(fundingAmount))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("external regtest funding: %v: %.2048s", err, output)
	}
	waitFor(t, ctx, "external Ark VTXO funding", func() (bool, error) {
		rows, err := p.index.Vtxos(ctx, ports.VtxoQuery{Script: p.config.Wallet.Receive.Script})
		if err != nil {
			return false, err
		}
		var amount int64
		for _, v := range rows {
			if usable(v) {
				amount += v.Amount
			}
		}
		return amount == fundingAmount, nil
	})
	r.FundedSatoshis = fundingAmount
	_, err = p.wallet.SelectFunding(ctx, 70000)
	require(t, err)
	t.Logf("funded %s with %d regtest sats", r.Adapter, fundingAmount)
}

func usable(v ports.Vtxo) bool {
	return v.Amount > 0 && v.ExpiresAt > time.Now().Unix() && !v.Spent && !v.Swept && !v.Unrolled && v.SpentBy == nil && v.ArkTxID == nil && v.SettledBy == nil && len(v.Assets) == 0
}

func runLiveHand(t *testing.T, scenario string, allIn int) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	root := os.Getenv("POKER_REGTEST_ARTIFACTS")
	if root == "" {
		root = "../../build/regtest-hands"
	}
	require(t, os.MkdirAll(root, 0700))
	dir, err := os.MkdirTemp(root, scenario+"-")
	require(t, err)
	dir, err = filepath.Abs(dir)
	require(t, err)
	r := handReport{Scenario: scenario, Started: time.Now().UTC()}
	t.Cleanup(func() {
		r.ElapsedSeconds = time.Since(r.Started).Seconds()
		r.Passed = !t.Failed()
		data, err := json.MarshalIndent(r, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
		t.Logf("retained report and private game logs: %s", dir)
	})
	var players [2]*livePlayer
	for i := range players {
		players[i] = newPlayer(t, ctx, dir, i, &r)
	}
	for i, p := range players {
		fundPlayer(t, ctx, p, &r.Players[i])
	}
	relay := endpoint(t, "POKER_RELAY_URL", "ws://localhost:7777")
	type completion struct {
		player int
		err    error
	}
	done := make(chan completion, 2)
	for i, p := range players {
		go func() { done <- completion{i, p.driver.Run(ctx, p.inputs, p.updates)} }()
	}
	var last [2]string
	var outcomes [2]*game.Outcome
	var stopped [2]bool
	started, joined, raised := false, false, false
	consume := func(i int, u game.Update) {
		if u.Err != nil {
			t.Fatalf("player %d stage %d: %v", i+1, u.Snapshot.Stage, u.Err)
		}
		s := u.Snapshot
		if s.Outcome != nil {
			outcomes[i] = s.Outcome
			return
		}
		if i == 0 && s.Invitation != nil && !joined {
			inv := *s.Invitation
			players[1].inputs <- game.Input{Kind: game.JoinSession, Invitation: &inv}
			joined = true
		}
		if s.Choice == nil {
			return
		}
		if s.Stage == game.StageInit {
			if i == 0 && !started {
				players[0].inputs <- game.Input{Kind: game.StartSession, Terms: game.Terms{Stake: 10000, Bond: 10000, MinBet: 1000, MaxWager: 50000}, RelayURL: relay}
				started = true
			}
			return
		}
		position := fmt.Sprintf("%d:%v", s.Stage, s.State)
		if last[i] == position {
			return
		}
		for _, kind := range s.Choice.Allowed {
			input := game.Input{Kind: kind}
			label := "showdown reveal"
			switch kind {
			case game.Bet:
				input.Bet.Kind = covenant.Check
				label = "check"
				if s.Choice.CanCall {
					input.Bet.Kind = covenant.Call
					label = "call"
				}
				if !raised && s.Choice.MaxRaiseTo > 0 && (i == allIn || allIn < 0 && i == 0) {
					amount := s.Choice.MinRaiseTo
					if allIn >= 0 {
						amount = s.Choice.MaxRaiseTo
					}
					input.Bet = covenant.BettingAction{Kind: covenant.RaiseTo, Amount: amount}
					label = fmt.Sprintf("raise to %d", amount)
					raised = true
				}
			case game.RevealShowdown:
			default:
				continue // Never choose a concession or timeout to hide a failed hand.
			}
			last[i] = position
			r.Players[i].Actions = append(r.Players[i].Actions, label)
			t.Logf("player %d stage %d: %s", i+1, s.Stage, label)
			players[i].inputs <- input
			return
		}
	}
	for !stopped[0] || !stopped[1] {
		select {
		case <-ctx.Done():
			t.Fatalf("full hand timed out: %v; last positions %v", ctx.Err(), last)
		case c := <-done:
			require(t, c.err)
			stopped[c.player] = true
			// Final snapshots were enqueued before Run returned.
			for len(players[c.player].updates) > 0 {
				consume(c.player, <-players[c.player].updates)
			}
		case u := <-players[0].updates:
			consume(0, u)
		case u := <-players[1].updates:
			consume(1, u)
		}
	}
	if !raised {
		t.Fatal("scenario never exercised its raise")
	}
	for i, out := range outcomes {
		if out == nil || out.Transaction == nil || out.Settlement.Kind != game.SettlementShowdown {
			t.Fatalf("player %d did not reach accepted showdown", i+1)
		}
		if i == 0 {
			r.TerminalTxID = out.Transaction.TxHash().String()
			r.Settlement = out.Settlement
		} else if out.Transaction.TxHash().String() != r.TerminalTxID || out.Settlement != r.Settlement {
			t.Fatal("players disagree on accepted outcome")
		}
		verifyReplay(t, ctx, players[i], out, &r.Players[i])
	}
	for i, p := range players {
		spendPayout(t, ctx, p, outcomes[i], i, &r.Players[i])
	}
	t.Logf("accepted showdown %s; both payouts spent successfully", r.TerminalTxID)
}

func verifyReplay(t *testing.T, ctx context.Context, p *livePlayer, out *game.Outcome, r *playerReport) {
	t.Helper()
	records, err := p.log.Load(ctx)
	require(t, err)
	r.Records = len(records)
	var replay *game.Game
	defer func() {
		if replay != nil {
			replay.Destroy()
		}
	}()
	for _, record := range records {
		if bytes.Contains(record, p.secret[:]) || bytes.Contains(record, []byte(hex.EncodeToString(p.secret[:]))) {
			t.Fatal("imported wallet key leaked to game log")
		}
		event, err := game.DecodeEvent(record)
		require(t, err)
		if event.Secrets != nil {
			defer event.Secrets.Destroy()
		}
		if replay == nil {
			if event.Kind != game.Configured {
				t.Fatal("missing journal start")
			}
			replay, err = game.New(p.config)
			require(t, err)
		}
		require(t, replay.ApplyContext(ctx, event))
		clear(record)
	}
	if replay == nil {
		t.Fatal("empty game log")
	}
	s, err := replay.Snapshot()
	require(t, err)
	if s.Stage != game.StageFinished || s.Outcome == nil || s.Outcome.Transaction == nil || s.Outcome.Transaction.TxHash() != out.Transaction.TxHash() || s.Outcome.Kind != out.Kind {
		t.Fatal("durable replay outcome mismatch")
	}
}

// Sweep all ordinary wallet outputs back to the same test wallet, asserting that
// the exact payout is among the inputs. An indexed payout alone is not this test.
func spendPayout(t *testing.T, ctx context.Context, p *livePlayer, out *game.Outcome, i int, r *playerReport) {
	t.Helper()
	point := out.Payouts.Player1
	if i == 1 {
		point = out.Payouts.Player2
	}
	r.Payout = point.String()
	var rows []ports.Vtxo
	waitFor(t, ctx, "accepted payout VTXO", func() (bool, error) {
		var err error
		rows, err = p.index.Vtxos(ctx, ports.VtxoQuery{Script: p.config.Wallet.Receive.Script})
		if err != nil {
			return false, err
		}
		for _, v := range rows {
			if v.Outpoint == point && usable(v) {
				r.PayoutSatoshis = v.Amount
				return true, nil
			}
		}
		return false, nil
	})
	var total int64
	for _, v := range rows {
		if usable(v) {
			total += v.Amount
		}
	}
	funding, err := p.wallet.SelectFunding(ctx, total)
	require(t, err)
	var inputs []offchain.VtxoInput
	found := false
	for _, source := range funding.Inputs {
		inputs = append(inputs, source.Vtxo)
		found = found || *source.Vtxo.Outpoint == point
	}
	if !found || funding.Change != nil {
		t.Fatal("sweep did not select the exact payout")
	}
	main, checkpoints, err := offchain.BuildTxs(inputs, []*wire.TxOut{wire.NewTxOut(total, p.config.Wallet.Receive.Script)}, p.config.Wallet.CheckpointScript)
	require(t, err)
	prepared, err := p.wallet.Prepare(&covenant.Unsigned{Ark: main, Checkpoints: checkpoints})
	require(t, err)
	signed, err := p.wallet.Sign(ctx, prepared)
	require(t, err)
	submitted, err := p.wallet.Submit(ctx, wallet.ArkdRoute, signed)
	require(t, err)
	r.SweepTxID = submitted.TxID
	waitFor(t, ctx, "payout spend accepted by indexer", func() (bool, error) {
		rows, err := p.index.Vtxos(ctx, ports.VtxoQuery{Outpoints: []wire.OutPoint{point, {Hash: main.UnsignedTx.TxHash(), Index: 0}}})
		if err != nil {
			return false, err
		}
		spent, received := false, false
		for _, v := range rows {
			if v.Outpoint == point {
				spent = v.Spent && v.ArkTxID != nil && v.ArkTxID.String() == submitted.TxID
			}
			if v.Outpoint.Hash == main.UnsignedTx.TxHash() && v.Outpoint.Index == 0 {
				received = usable(v) && v.Amount == total && bytes.Equal(v.Script, p.config.Wallet.Receive.Script)
			}
		}
		return spent && received, nil
	})
}
