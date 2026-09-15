# Wallet spending boundary

The wallet composes the admitted service ports and the caller-owned imported key.
The key stays in memory; its owner stops signing before `Destroy`. The driver
owns funding freshness, poker layout, deadlines, durable signing/submission work,
accepted indexer evidence and retry decisions. None of these checks are replaced
by a wallet service response.

Key import tries NIP-19 nsec, then hexadecimal, then BIP39 on decode errors.
Decoded raw keys must be exactly 32 bytes and a nonzero scalar below secp256k1's
order. Mnemonics use `go-bip39` validation/seed derivation with an empty passphrase,
then `btcutil/hdkeychain` derives `m/86'/coinType'/0'/0/0`. Coin type is 0 for
Arkd's `bitcoin` network and 1 for `testnet`, `testnet4`, `signet`, `mutinynet` and
`regtest`. Missing/unknown networks fail rather than taking the upstream mainnet
fallback. Mnemonic whitespace is canonicalized before validation and derivation;
all English BIP39 word counts (12/15/18/21/24) are accepted. This imports the first
receiving key, not an HD wallet scan. The derived key is bound to its discovered
network, so later receive discovery cannot silently switch networks. Seed and
intermediate extended-key buffers are cleared; input text stays out of errors.

`New` checks network, compressed service keys, distinct Arkd/emulator keys,
canonical positive BIP68 exit/checkpoint policy, the checkpoint's forfeit key,
and dust/minimum/maximum output limits. Maximum `-1` means Bitcoin MAX_MONEY.
The receive tree/address come from upstream Ark helpers. When a delegator is
configured, discovery fetches and validates its compressed public key and adds
the owner + delegate + Arkd multisig leaf after the default exit and owner + Arkd
leaves, matching the SDK's delegated tree. Failure to discover a configured
delegate fails import. Every wallet import rediscovers this tree from the current
runtime endpoints; a journal never selects the wallet's address. Address prefixes use
`clientlib.NetworkFromString`, including Mutinynet and upstream's Bitcoin
fallback for unknown network names. Hosts pass an empty configuration to `New`;
discovery gets the network from Arkd info. An optional expected policy passed by
another caller must match discovery exactly. Config returns owned public slices
and does not rotate during a running game. Recovery assumes the same wallet,
network and services were supplied again at startup.
`AdmitAgreement` checks the reference's smallest bond refund and largest timeout
payout for both P2TR recipients and matches the local participant's payout.
Poker consumes only offchain VTXOs through Submit/Finalize and the emulator.
RegisterIntent fees and onchain-input fees are unrelated to this path; their
configured values do not gate poker. Exact input/output conservation is required.

Funding selection queries the default receive script and reads time after the
query. Missing/invalid expiry, duplicate records, mismatched scripts/transactions,
and invalid amounts fail. Expired, spent, swept, unrolled, successor-marked or
asset-bearing outputs are excluded. Preconfirmed is not required: ordinary
settled funding is allowed. Source bodies are fetched once per transaction and
checked against indexed outpoint/value/script. Selection follows Rust: exact
single coin first, then descending value with outpoint tie-breaking; greedily
skip oversized change, retain usable change, never burn dust or reserve coins.
Returned sources use upstream trees/proofs and own their data. Funding explicitly
selects the owner + Arkd leaf from either receive tree, and change uses the same
default receive script. The additional delegate leaf requires no delegate
signature for ordinary funding. This does not register or schedule delegation.

`Prepare` serializes the upstream builder's PSBTs; it does not reconstruct Ark
transactions or checkpoints. `Sign` requires every input's witness UTXO, one
selected leaf and default sighash, then uses btcd's Taproot sighash and btcec's
Schnorr signer. Only the imported key's script-signature map entries change.
Raw PSBT maps preserve opaque metadata and explicitly encoded default fields
that the upstream serializer may omit. The result is compared with the exact
prepared bundle before returning for durable recording.

`Submit` validates the saved route against the first selected leaf, matching
ordinary funding to the wallet's owner + Arkd leaf. Covenant leaves put the
emulator first, the participant second and Arkd last, so the emulator returns
signatures without submitting or finalizing. The wallet rejects covenant leaves
that put the participant elsewhere before contacting the emulator. Ordinary
funding goes directly to Arkd Submit, then Finalize. Continuations first obtain
and merge emulator signatures, then the client calls Arkd Submit and Finalize.
The persisted PSBT strings are sent verbatim to the first service; Arkd receives
the merged emulator signatures on continuations. Service transaction identities,
all non-signature maps and all existing local signatures must be preserved.
Checkpoints are matched by transaction ID, rejecting duplicates and omissions.
Arkd alone may return upstream-rebuilt checkpoints containing only witness UTXO,
selected leaf and `0xde taptree` input fields; their signatures are merged into
the saved complete maps before Finalize, retaining both participant and emulator
signatures. Changed values, paths, opaque metadata or existing signatures fail. Native builds additionally use the pinned client
library to verify Arkd signatures; browser builds delegate transaction-signature
verification to Arkd/emulator as approved in `plan.md`. The FSM has no new
signature gate.

Service errors identify emulator signing, Arkd submission or Arkd finalization
and preserve the wrapped cause. `RetrySaved` calls Submit exactly once without
selection, rebuilding or signing. Only the driver's prior explicit-unspent
observation authorizes calling it; successful submission still needs accepted
indexer evidence.

## Qualification — 2026-09-14

- Native wallet tests and vet pass; race detector and Node WASM tests pass.
- Policy tests cover snapshots, service-key/locktime/amount rejection and both
  canonical block/seconds checkpoints. Funding tests cover exact/greedy change,
  all exclusion markers and missing/mismatched source evidence.
- Real `offchain.BuildTxs` bundles are locally signed; upstream
  `script.VerifyTapscriptSigs` verifies participant and finalized checkpoint
  signatures. Tests preserve explicit defaults and saved opaque metadata,
  reject service mutations, and exercise reordered/rebuilt checkpoints.
- Controlled Arkd/emulator tests cover emulator → Arkd Submit → Arkd Finalize,
  both participant/emulator signatures after rebuilt checkpoints, errors at each
  boundary, exact retry without hidden retries, and wrong-route/leaf rejection.
  Native tests reject missing/corrupt Arkd signatures before Finalize.
- `TestDriverActualWalletSignsCompleteHand` runs actual wallet signing through a
  complete fresh driver hand with upstream signature verification and the real
  emulator VM. Funding/indexing/services remain controlled in this test.

Live funded wallet submission, final payout spendability and mixed native/Brave
host integration remain mandatory later acceptance gates. These tests do not
claim them complete.

## Header balance

`Balance` queries the default receive script and sums indexed sats using the
existing funding eligibility filters. It includes preconfirmed outputs and
excludes expired, spent/spending, swept, unrolled, settled-away and asset-bearing
outputs. It rejects duplicate/foreign records and invalid or overflowing sums.
The display query does not fetch transaction bodies; funding selection retains
its existing transaction authentication and spending checks.
