# Game protocol and implementation record

`../../plan.md` governs scope, approvals and execution order. The behavior
reference is `crates/client/src/{fsm.rs,protocol.rs,codec.rs,session.rs}` and
`crates/client/src/fsm/{setup,reduce,admission}.rs` at the repository root.
The scoped package implementation is complete: setup and every hand transition,
canonical replay, exact saved work, a durable sequential driver and indexer-driven
recovery. Native and browser storage are implemented and qualified separately.
Service subscriptions are implemented and qualified in plan step 4. Full wallet
spending/submission, Nostr delivery and UI wiring remain later plan steps. Core tests use those narrow boundaries with
real shuffle/card proofs, actual upstream transaction builders and the real VM;
they do not claim live service submission or a funded-hand acceptance run.

## Setup ordering

Player 1 creates the invitation and shuffles last. Player 2 joins, offers keys,
shuffles first and deposits first. Each side has independent outgoing/incoming
message counters, starting at zero. A received key offer pins the peer identity.
Identical transport/wallet/encryption keys and cancelling encryption keys are
rejected with correctly bound ownership proofs. Payouts must be P2TR and admit
both the smallest bond refund and the largest timeout payout under the common
service output policy.

| Player 1 state | Fact / next operation | Player 2 state |
| --- | --- | --- |
| Init | Record configuration, then invitation, local secrets and key offer | Init |
| SessionPrepared | Open stored-message transport after recording setup | SessionPrepared |
| AwaitOpponentKeys | P2 publishes sequence 0 KeyOffer | Player2KeysPrepared |
| Player1KeysPrepared | P1 publishes sequence 0 KeyReply | AwaitOpponentKeys |
| AwaitInitialShuffle | P2 generates and records sequence 1 InitialShuffle | InitialShuffle |
| AwaitInitialShuffle | P2 publishes exact recorded shuffle | InitialShufflePrepared |
| FinalShuffle | P1 verifies P2's proof, shuffles, then reads clock | AwaitFinalShuffle |
| FinalShufflePrepared | Attach covenant watch, then publish exact sequence 1 FinalShuffle | AwaitFinalShuffle |
| AwaitInitialDeposit | P2 checks deadline and independently derives agreement | InitialDeposit |

Publication acknowledgements, session-open results and inbound messages are
recorded facts; deciding does not pretend those effects have occurred. Public
`RunEffect` results identify the required driver operation. The final publication
has a distinct `WatchAndPublishFinalEffect` to preserve watch-before-publication.
The driver waits for the subscription boundary to confirm attachment; HTTP 200
or a quiet timeout cannot satisfy that boundary.

Ownership proofs bind the canonical invitation, role, transport identity,
wallet signing key and exact payout script. Shuffle contexts bind the canonical
invitation and both complete ordered key offers, with different initial/final
stage bytes. P1 chooses the initial deadline **after** final proof generation as
recorded time + 60, checked for overflow. P2 admits the inclusive [now+30,
now+90] window with checked additions. The final verified deck maps positions
0,1 to P1 holes; 2,3 to P2 holes; 4,5,6 to flop; 7 to turn; 8 to river. Both sides
call the existing covenant derivation; no separate agreement message is sent.
No private cards become visible during setup.

The reference reducer permits every named abort reason in any pre-lock setup
state (and Player1Funding once gameplay exists). The live handler selects the
appropriate reason. An aborted setup is a distinct outcome, never an accepted
settlement. No further events may resume it.

## Signature boundary

The approved Go ownership decision is authoritative: the FSM verifies only
shuffle/card proofs. Message Identity must already be authenticated by transport.
`WalletOwnershipDigest` supplies the wallet-ownership challenge to the boundary;
opaque `Message.WalletOwnership` bytes are retained exactly and included in the
shuffle transcript, without signature presence, type or validity checks in the
FSM. The driver invokes wallet ownership signing/authentication through the
wallet boundary, which delegates to the Schnorr library. Peer carrier signatures
belong to `ports.PeerTransport` (the later Nostr implementation). Neither setup
records nor a prepared/signed bundle establish service acceptance.

## Version 1 canonical encoding

All integers are little endian. Fixed fields have no length prefix; blobs and
UTF-8 strings have u32 lengths. The decoder bounds lengths against both remaining
bytes and the field limit before allocation. Every decoder rejects trailing
bytes, unknown versions and noncanonical embedded crypto. Native and WASM share
these exact codecs. There is no Rust wire/log compatibility requirement.

- Legacy configuration: `arkade-poker/config\0`, u16(1), service fields, wallet xonly32,
  receive address/script/tapscripts, then Arkd/emulator/indexer URLs. A nonempty
  delegator URL is an optional final string; legacy configs omit it and retain
  their original canonical bytes. An explicit empty tail is noncanonical. Service
  fields are network string, Ark and emulator xonly keys, checkpoint script and
  output-policy minimum/maximum. Service binding hashes those common fields
  under `arkade-poker/services\0` + u16(1), excluding local wallet metadata and
  host-specific endpoint URLs. This encoding remains for owned runtime copies
  and legacy records; new journals do not persist configuration snapshots.
- Invitation: `arkade-poker/invitation\0`, u16(1), four u64 terms, service hash32,
  creator transport xonly32, random join capability32, exact relay string,
  SHA256 checksum over preceding bytes. Session ID is SHA256 of
  `arkade-poker/session/v1` and these complete invitation bytes. Sharing uses
  `arkpg1:` and lowercase hexadecimal. Relay UTF-8 is limited to 2048 bytes;
  syntax admission preserves the reference's ws/wss, authority, whitespace,
  userinfo and fragment checks without URL normalization.
- Public message: `arkade-poker/message\0`, u16(1), session32, role u8 (1 or 2),
  transport xonly32, sequence u64, kind u8 and exactly that kind's payload.
  KeyOffer/KeyReply carry participant (wallet xonly32, shuffle SEC1 key33, payout
  script blob), shuffle ownership proof65 and opaque wallet-ownership blob.
  InitialShuffle/FinalShuffle carry the full deck (52 * 66 bytes) and BG12 proof
  (`shuffle.ShuffleProofSize`). FinalShuffle adds the u64 deadline. The total
  message limit is 64 KiB; irrelevant fields are rejected, not discarded.
- Private event: `arkade-poker/private-event\0`, u16(1), session32, sequence u64,
  previous hash32, observed time u64, kind u8, exact branch payload, checksum32.
  Sequence 0 is Configured, with zero session ID, an empty configuration blob,
  and SHA256(`arkade-poker/wallet-log\0` + wallet xonly32) as Previous. Legacy
  Configured events retain their embedded configuration and its original hash
  as Previous; replay checks those bytes without applying their settings.
  Later Previous values hash the complete prior encoded event. The
  setup record introduces the session routing context through its invitation;
  all subsequent records retain that exact ID. Time is zero except received
  FinalShuffle, accepted deposit/spend/private-opening observations and submission
  attempts. Tag 6 is reserved (agreements are derived, not external facts).
  Unknown tags cannot become accepted facts. Tag 17, PublicationPrepared, carries
  a nonempty exact canonical message blob (at most 64 KiB), then a nonempty opaque
  signed carrier blob (at most 128 KiB). Its message must exactly match the pending
  publication; no carrier signature parser or validity gate runs in the FSM.
  MessagePublished is admitted only after PublicationPrepared and clears that
  pending carrier after the matching publication acknowledgement.
- Session secrets: u16(1), shuffle scalar LE32, independent transport scalar BE32,
  original shuffle ownership proof65. Both scalars are canonical and nonzero.
  The proof is retained to avoid replacing prepared setup work on replay. This
  explicit private codec is never reachable from the public-message codec.

Events are bounded to 4 MiB; scripts to 10,000 bytes; config vectors to 256.
Checksum/chain integrity detects corruption and ordering errors, not a malicious
writer who can rewrite the private store. Replay authenticates protocol evidence
again and never restores a serialized Verified marker. `ApplyContext` computes
an owned candidate, checks cancellation, and replaces state/cursors only after
all admission succeeds. Caller buffers, mutable snapshots and copied secret
handles cannot mutate the reducer's retained state.

Secrets redact formatting and synchronize destruction with serialization. Copies
of a SessionSecrets handle share destruction; Apply owns an independently
decoded handle. Only independently generated session secrets can be stored;
the imported wallet key is never passed to these constructors. Compiler and GC
copies cannot be guaranteed erased. A host-supplied entropy reader must be secure
and return from Read; cancellation cannot interrupt an arbitrary blocked reader.

## Accepted hand and exact work

`admission.go` ports the reference source/checkpoint/main admission and all
ordinary, reveal, all-in and terminal transitions. It reads the actual upstream
extension and emulator packets, admits the selected script/tree, preserves every
previous share and checks new DLEQ proofs in contract/slot context. Cumulative
wagers, check/call/raise rules, short raises at the cap, contributions, change
and payout ordering follow Rust. Deadlines advance by 60 seconds per live
transition, using the same interval as setup and covenant scripts. A wallet/indexer
accepted result provides service acceptance; constructing a transaction or
signing it does not.

`hand_reduce.go` calls every actual covenant builder and reconstructs the
recorded action during replay to compare the exact prepared transaction,
checkpoint, source, selected-path and metadata fields. The wallet helper strips
only input `PSBT_IN_TAP_SCRIPT_SIG` fields before delegating non-signature parsing
to upstream PSBT. Missing, multiple or malformed values in those signature fields
remain opaque. Every other PSBT map and transaction field must match. Exact
base64 strings survive persistence and replay, including those opaque fields.

`hand_codec.go` defines the tagged private action and transaction fields:

- Saved work: route u8; optional covenant outpoint (u8 flag, hash32, vout u32);
  u32-counted wallet outpoints; tagged action; prepared bundle; u8 signed flag
  and optional signed bundle. A bundle is the exact Ark base64 string, u32
  checkpoint count and exact checkpoint base64 strings.
- Actions: kind u8, optional bet kind u8/amount u64, branch funding, reveals,
  showdown, and timeout/showdown observation time u64, in that order. Irrelevant
  fields are rejected. The code defines each branch's exact field set.
- Funding: u32-counted inputs (at most 255), each with outpoint, amount u64,
  serialized source-transaction blob, upstream tapscript type u8, control-block
  blob, revealed-script blob, leaf vector (version u8/script blob), root-hash
  blob, optional compressed full output key, and revealed tapscript strings;
  optional exact change output value u64 and script blob.
- Transactions use bounded upstream native wire encoding in blobs. The decoder
  preflights native counts and script lengths before upstream allocation.
  Accepted observations carry the main transaction and a u32-counted ordered
  checkpoint vector; they never restore a serialized verified marker.
- Reveal witnesses: semantic kind u8 and exactly its compact share/proof fields
  (33 + 98 bytes per card). Private opening records add the two local shares to
  the accepted observation. Showdown witnesses contain deal-order cards9 and
  both rank u16 / fixed Merkle proof fields.
- Submission receipt: recovery flag u8, acknowledged-txid presence u8 and optional
  hash32. Nil txid means uncertain. Failed submission/retry codes retain saved
  work and active ownership; they never imply a terminal result. The recovery
  flag allows the approved immediate exact retry; the driver enforces the
  once-per-recovery-attempt bound and stops on retry failure.

Private opening shares are generated only after the locked opposing opening is
admitted, and become visible only after recording. Snapshots show one's own holes
and the complete public board prefix. The opponent's holes become visible only
after both of that player's own reveal shares have been accepted. Settlement
retains the last accepted state and projected cards as display-only data, while
releasing the live hand and offering no further hand choices. Replay reconstructs
the same final table; a fold never reveals cards by itself. Merkle evaluation
and proof admission use the existing `merkel` implementation. The winner (P1 on
a tie) may settle immediately; the other player waits 30 seconds after the
recorded accepted showdown state. Accepted terminal spends alone finish a hand;
competing accepted spends supersede any pending local action.

## Durable journal

`journal.go` replays storage records using `ApplyContext`, restoring recorded
public configuration and independently owned session secrets. It matches the
supplied wallet public identity before replaying the session and has no wallet
private-key ingress or persistence. It admits a candidate, appends exact canonical
bytes at the current index, then publishes that candidate only after storage
acknowledges durability. Invalid events are never written; any append failure
stops that writer without changing live state. A later reload accepts the intact
committed prefix, including a complete record whose acknowledgement was lost.
Cancellation after a successful durable append does not discard that fact.

The shared storage frame and both implementations are documented in
`../storage/protocol.md`. Real Brave qualification includes cross-tab exclusion,
refresh, acknowledged-record recovery, strict transaction aborts and corrupt-log
rejection. The driver uses this journal for every durable transition. Host UI
wiring and production wallet/Nostr implementations remain later plan steps.
Service subscriptions are qualified separately in `../adapters/protocol.md`.

## Sequential execution and service evidence

`Driver.Run` is the single owner of the FSM and its effects. It accepts transient
commands and emits owned snapshots; betting/reveal/concession choices become
visible only after source observation. It emits `Shuffling` progress around replay
and actual cryptographic work, with errors clearing that progress. Rendering and
UI controls are outside the driver. Invalid user choices preserve state; failed
network/admission/durable operations stop automatic advancement.

`Driver.Restore` replays once using the supplied runtime configuration and matches
the re-imported wallet before game effects. Hosts may discover service metadata
before replay. With `DriverConfig.Connect`, the factory receives an independently
owned copy of runtime configuration and must bind its returned wallet/indexer/
subscription/transport clients to it. The returned public wallet policy must
match exactly. Without a factory, prebuilt clients must match the supplied Game
configuration. Journals never choose endpoint URLs or the receive tree. Recovery
assumes the same wallet, network and services were supplied on restart; existing
protocol admission can reject an incompatible configuration. A factory owns
cleanup if construction fails; the host
owns shared wallet/indexer client lifetimes. The driver closes its transport,
subscription and log. `Close` cancels and awaits the active owner, destroys local
session secrets and releases the writer; it never concedes or deletes records.
An error halts that driver. Close/reopen with an intact log starts a new explicit
recovery attempt; concurrent Run/Restore operations are rejected.

Setup records independent session keys and wallet ownership before opening the
peer transport. Each outgoing canonical message and its exact signed carrier are
recorded before publication; a restored publication reuses those carrier bytes
without another timestamp, serializer or signature. `PeerTransport.Open` is
idempotent for the recorded session and pins the admitted peer. Transport receives
are bounded, authenticate identity before delivery, and retain session/order/
equivocation checks in the FSM. The imported wallet key never crosses this port.

The driver attaches the covenant script subscription before final-shuffle
publication, initial coin selection, signing and any submission/retry. A watcher
retains bounded transaction hints separately from coalesced wakeups; a stream gap,
queue overflow or interruption requires fresh confirmed attachment. Malformed
hint kinds/scripts stop advancement. Hints never establish acceptance. Bounded
waits reconcile the indexer before/after observations; a matching hint can trigger
accepted-transaction lookup before broad script/source views catch up. A source
hint must match the recorded logical outpoint, and a covenant continuation must
also name the new output. Terminal payouts need no new covenant tip. P1 discovers
accepted deposits before checking expiry, including this hinted lookup; ambiguous
valid deposits fail instead of choosing one. Initial/P1 funding freshness is
checked before preparation, signing, submission and saved retries. Timeout
maturity and winner/P1-tie settlement priority preserve Rust's handlers.

Accepted evidence uses `wallet.Accepted`, `InspectSpend` and `AcceptedByScript`
over the existing indexer's VTXO/transaction APIs. These authenticate finalized
outputs and every source/checkpoint/main edge, including outputs spent onward.
The game independently admits the poker transition and proof/value accounting,
then records the accepted event. Building, signing, subscription hints and submit
acknowledgements are never accepted terminal outcomes.

## Exact recovery

After replay or an uncertain submission response, reconciliation first checks the
saved transaction identity, the covenant source and every wallet funding source.
Each accepted edge is validated and recorded; accepted competing spends supersede
saved local work. After a local accepted spend, the driver follows each opponent
response from the resulting logical outpoint, records missing private opening
shares in reference order and keeps watching the unresolved successor. Accepted
terminal spends stop normally.

Missing, pending, unavailable or failed queries never authorize retry. Explicit
unspent evidence for every source permits one retry of exact saved signed work.
The attempt flag is set before the wallet call. No coins, transaction, proof or
signature are regenerated. A successful acknowledgement is recorded and queried
again; if indexing lags, the driver keeps watching without another recovery
retry. A retry error records the bounded `retry_failed` code and stops, preserving
saved work and game ownership. A failed durable append also stops immediately.

If no action was saved, normal progression resumes after source reconciliation.
Unsigned saved work resumes its first signing operation: durable-before-submit
ordering proves it could not already have been sent. This does not authorize
re-signing any saved signed work. During ordinary uninterrupted acknowledged
submission, Rust's five-second exact resubmission interval remains unchanged;
restart or an uncertain response enters the separately approved bounded recovery.

## Verification and remaining integration

Setup tests generate both actual 52-card shuffles and replay every encoded setup
prefix. Gameplay tests drive complete ordinary and either-actor/every-street
all-in histories using actual builders and the real emulator, then replay exact
prepared/signed/private-opening/terminal prefixes. Concession and timeout
branches, competing spends, short cap raises, exact source/metadata comparisons,
private visibility and win/loss/tie payouts are exercised. Controlled test-only
permutation entropy produces full valid shuffle proofs for fixed showdown
outcomes; production crypto and VM admission are not bypassed. Malformed opaque
signature fields survive replay while non-signature tampering is rejected.

Driver tests run fresh invitation through authenticated setup and accepted
settlement, complete ordinary/either-actor all-in hands, and interrupted prefixes.
The fixture ledger exposes actual existing indexer operations and admits built
transactions only after the real emulator executes them. Its peer boundary uses
real library Schnorr signatures over a test carrier, and asserts durable-before-
publish ordering. Fixture wallets assert exact durable work and attachment before
signing/submission; they deliberately omit transaction signatures to exercise the
approved boundary. These fixtures are not production wallet or Nostr adapters.

Failure tests cover absent/failed durable acknowledgements, lost submit responses,
accepted catch-up/competing spends, every funding source, once-per-attempt retry,
delayed indexing, exact saved carriers, subscription attachment/gaps, lagged hint
lookup, malformed hints, funding freshness, ordinary retry timing, timeout
maturity, wrong wallet/configuration and caller-buffer ownership. Full native
and Node WASM suites, vet, builds and targeted race checks are recorded in
`../../IMPLEMENTATION.md`. Prior native file-store and actual Brave cross-tab/
refresh qualification remain in `../storage/protocol.md`.

Next follow plan step 4: implement actual attached/reconnecting service
subscriptions and finish service/WebSocket adapters. Step 5 must implement
`DriverWallet` spending policy/signing/submission and Nostr's `PeerTransport`
contract. Hosts must provide runtime-config connection construction and wire the
shared UI in their later steps. Live zero-fee mixed native/Brave funded-hand
acceptance and live interrupted-submit recovery remain unqualified; this package
completion does not satisfy those application milestone gates.
