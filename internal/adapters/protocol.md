# Service and WebSocket boundaries

Plan step 4 implements transport behavior only. Indexer notifications are hints;
the wallet/driver's query evidence establishes accepted spends. Wallet spending
policy, participant signing order and Nostr authentication remain step 5.

## Subscriptions

Both adapters use `subscription.Owner` and `indexdata.SubscriptionFrame`. A
subscription accepts 1–16 distinct P2TR scripts, copied before asynchronous work.
The first actual started, heartbeat or valid transaction frame establishes
attachment within 30 seconds. HTTP status, headers, comments and quiet time do
not. `Next` reports `ScriptAttached` and retains transactions received during
attachment. Created/spent logical outpoints are filtered per script, deduplicated
and ordered consistently; the event transaction ID is kept separately. Each
new/spent list is limited to 256 records and wire frames to 20 MiB.

The native generated `GetSubscription` RPC and gateway
`GET /v1/indexer/subscription?filter.scripts.add=...` both use an empty subscription
ID and the initial filter. The pinned server applies the filter before sending
started and releases the listener on cancellation. Queries continue using the
upstream native client library. Its high-level quiet-time READY is not used.

SSE requires `Accept: text/event-stream` and a successful SSE content type. Parsing
is incremental, bounded across both lines and entire frames, supports fragmented
LF/CRLF/CR and multiline data, and rejects malformed/ambiguous ProtoJSON aliases,
unsupported frame kinds and explicit stream errors. An incomplete EOF is not a
frame. The streaming client retains redirect/transport policy but has no unary
whole-request timeout. `bindBody` explicitly closes the Fetch ReadableStream on
context cancellation, because Go's browser transport stops observing the request
context after returning response headers. Unary responses also use this binding
and retain a 30-second request deadline.

The observation queue holds 64 events. Overflow, malformed frames, EOF and service
errors terminate the stream and take priority over queued hints: `Next` exposes
one sticky observation gap, then the terminal error. No automatic reconnect loop
is hidden inside an adapter. The existing driver closes the old stream, confirms
a fresh attachment, and reconciles before further effects. A cancelled individual
`Next` does not cancel the subscription. Subscription Close and Indexer Close are
idempotent; indexer shutdown cancels and joins pending attachments and receivers.

## Requests and WebSockets

Arkd info/submit/finalize and emulator info/sign retain exact opaque bundle fields
and propagate service failures. Request/response bodies and bundle payloads are
bounded at 20 MiB, checkpoint lists at 256, and transaction identity strings at
128 bytes. These are transport bounds, not PSBT or signature admission. HTTP
accepts both protobuf field-name spellings and rejects duplicate aliases.

Delegator discovery uses the shared HTTP adapter on native and WASM builds:
`GET /v1/delegator/info` reads `pubkey`. It inherits the bounded JSON response,
redirect refusal and cancellation behavior. Wallet admission validates the
compressed public key before constructing the default receive tree; there is
no cached key or fallback after a configured delegator request fails.

WebSockets use unmodified coder/websocket v1.8.14 on both hosts, with UTF-8 text-only payloads
and a 20 MiB application message limit. Caller contexts own timeouts. The shared
adapter checks already-cancelled writes explicitly because browser Write is
nonblocking and ignores its context. Browser dial/read return at the caller
deadline independently of remaining network teardown. A successful late dial
gets a best-effort normal close.

Native Close uses upstream CloseNow. Browser Close marks the adapter closed,
interrupts local readers and requests Close(1000) in a goroutine, without waiting
for a peer acknowledgement or reporting remote close errors. Later reads/writes
are rejected locally. Upstream browser CloseNow uses code 1001, which the JS API
rejects; code 1000 is valid. A read worker may remain until upstream teardown
finishes, but it cannot hold up the Nostr reader or the next game.

The user approved best-effort remote cleanup because Nostr sockets serve setup
and shuffle negotiation, and each new game opens a fresh socket. Upstream's
internal reserved-code error/cancellation paths remain unchanged; an exceptional
failure may leave an old browser connection alive. This replaces the earlier
two browser changes in the dependency and removes its preparation machinery.

## Qualification

Generated gRPC/gateway fixtures verify equivalent attachment, early transaction
retention, delayed frames beyond two seconds, disconnect/fresh attachment,
malformed and oversized data, bounded queue pressure, cancellation and repeated
close. Actual-adapter driver tests prove attach → query → sign ordering and no
submission after failed reattachment. Shared parser/lifetime tests also run in
Node WASM. Native WebSocket tests cover text, binary rejection, UTF-8, both size
limits, dial/read/write cancellation and close ownership.

The live regtest checks use Arkd v0.9.16 and emulator v0.0.7. Discovery and repeated
subscription attachment succeed over native gRPC and HTTP. The public-script
query returned zero records; that is not funded source evidence.
`covenant/emulator_regtest_test.go` builds a synthetic source and OP_TRUE path with
the emulator before a separate participant in its outer closure, making it a
non-finalizer fixture. Real main/checkpoint signatures from both service adapters
verify with upstream `script.VerifyTapscriptSigs`, and all non-signature fields
remain exact. Poker covenant leaves now use this same signer order; the wallet
submits and finalizes with Arkd directly after merging emulator signatures. This
fixture does not use a funded wallet or qualify Arkd submission/finalization.

Historical Brave results are retained in `brave-qualification.json`; host: macOS 15.6
(24G84), Brave 150.1.92.141. The harness calls real regtest gateways and a controlled
cross-origin loopback WebSocket/SSE service. It verifies CORS, fragmented delivery,
heartbeat frames, an event delivered after 32 seconds, a healthy live stream over
the service's 60-second heartbeat interval, response-body cancellation, and socket
limits/cancellation/close. Those WebSocket results used the retired dependency
patch; the Fetch fix remains in the HTTP adapter.

`websocket-brave-qualification.json` records the upstream WebSocket recheck in
the installed Brave browser running headless. It covers the same socket bounds
and cancellation, plus three fresh sessions that complete while previous peers
delay responding to close for five seconds. This tests local responsiveness;
it does not require perfect cleanup on every upstream failure path.

Reproduce from the repository root:

```sh
go run ./scripts/build qualify transport
POKER_EMULATOR_FIXTURE_FILE="$PWD/build/transport-qualification/emulator-fixture.json" \
  go test -tags regtest ./internal/covenant -run TestRegtestEmulatorSigningAdapters -count=1
go run ./cmd/transport-fixture
```

Open `http://127.0.0.1:5176` in Brave. The fixture binds only 127.0.0.1 ports
5176/5177; the page stores public test results under ignored build output. A final
`/metrics` query on port 5177 can inspect remaining fixture connections. To run
only the WebSocket checks without the live service tests, open
`http://127.0.0.1:5176/?run=TestBrowserTransport/websocket`. Stop the fixture and
close only the qualification tab after recording results. Live
funded-hand acceptance and recovery remain step 8.
