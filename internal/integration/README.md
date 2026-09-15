# Headless regtest full hands

Run from `go/` with the existing local Arkd, emulator and Nostr relay running:

```sh
make test-regtest-hands
```

This explicitly funds six fresh test wallets with 100,000 regtest satoshis each
from the stack's `ark` CLI faucet. The ordinary scenario runs first, followed by
Player 1 all-in and Player 2 all-in; the suite stops at the first failed scenario.
To isolate one scenario:

```sh
CGO_ENABLED=0 go test -tags=purego,regtest_hand ./internal/integration \
  -run 'TestRegtestFullHands/ordinary$' -v -count=1 -timeout=8m
```

Each hand starts from a fresh invitation and uses real shuffle/card proofs,
wallet funding/signatures, Arkd/emulator submission, indexer evidence, subscriptions
and Nostr delivery. Player 1 uses gRPC and Player 2 uses HTTP/SSE. Both run native
Go; this does not qualify WASM, browser storage or UI behavior.

The tests require matching accepted showdown outcomes, replay both durable logs,
check the logs exclude imported wallet keys, and spend the exact payout VTXOs
back to their owning test wallets through real Submit/Finalize. No fixture ledger,
fake service acceptance, accelerated clock or automatic fold hides a failure.

The poker path never calls RegisterIntent or uses onchain inputs. Its transactions
conserve value; unrelated intent-fee settings are left unchanged. External faucet
funding is a test setup operation. If the faucet has insufficient spendable VTXOs
(including an expired balance), `scripts/fund-regtest.mjs` replenishes it with a
1,000,000-test-satoshi note, subtracts the server-quoted intent fee, and retries
the send once. Other send failures are not retried. Replenishment requires Go
and the pinned dependencies, downloaded through normal Go module resolution.

Defaults are `http://localhost:7070`, `http://localhost:7073` and
`ws://localhost:7777`. `POKER_ARKD_URL`, `POKER_EMULATOR_URL`, `POKER_INDEXER_URL`
and `POKER_RELAY_URL` can override them, but only loopback endpoints and regtest
are admitted. The funding helper uses the existing regtest environment loader
and Docker container naming. It does not receive poker private keys.

Reports and private native game logs remain under `go/build/regtest-hands/` in
unique scenario directories, including failures. `POKER_REGTEST_ARTIFACTS` can
choose another artifact root. Imported wallet keys are random and held only in
RAM, then destroyed; failed test wallets cannot be reopened after the run. Use
this only with disposable regtest funds. Public reports contain identities,
actions, timings, accepted transaction IDs and payout-spend evidence, not keys.

Headless full hands supplement the mixed native/Brave acceptance run. Live
interrupted submissions and restart/catch-up require additional scenarios.

Recorded run, 2026-09-14: all three scenarios passed (ordinary 5.06 s, Player 1
all-in 3.12 s, Player 2 all-in 3.36 s) against Arkd v0.9.16 and emulator v0.0.7.
All six payout spends were accepted. See `qualification.json` for public evidence
and the external faucet preparation needed for this run. The poker application
needed no behavior fixes, and server fee settings remained unchanged.
