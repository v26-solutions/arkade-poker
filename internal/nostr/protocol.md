# Public setup delivery

This package implements the driver's `ports.PeerTransport`. It uses the
[NIP-01 event hash/signature and relay framing](https://github.com/nostr-protocol/nips/blob/master/01.md)
with btcec Schnorr authentication. Application kind 2100 is a regular stored
event, not a registered poker NIP. Relay acknowledgement means acceptance of
that publication, not delivery to the other player or guaranteed future storage.

The Go profile has exactly two ordered tags: `["g", session-id-hex]` and
`["p", recipient-xonly-hex]`. Content is `arkade-poker/go/1:` followed by canonical
unpadded standard base64 of `game.EncodeMessage` bytes. Those bytes already
bind the Go protocol version, session, sender role, identity, sequence and public
setup body. ASCII-only tags/content avoid NIP-01 string-escaping differences.
Timestamps are nonnegative signed 64-bit Unix seconds. IDs/keys/signatures use
lowercase hex. Maximum public payload is 64 KiB; the complete JSON carrier fits
within 128 KiB. Rust wire compatibility is outside the approved scope.

`Prepare` accepts only the canonical public message codec, checks every supplied
context field and local author, signs the event, and returns owned copies of
the exact payload and publication carrier. It has no private game-event input.
`Publish` rechecks the saved carrier's signature/context, sends its bytes
verbatim, and waits only for the matching positive `OK`. Other acknowledgements
do not complete it; incoming setup messages are admitted while waiting. Errors
propagate without a hidden retry. A retry uses the caller's exact saved carrier.

`Open` owns a copy of the independent transport secret, preserving the exact
saved secret, role, relay and session across repeated calls. The imported wallet
key never enters this package. A healthy same-session Open is idempotent. A
failed connection needs an explicit Open; there is no reconnect loop. Requests
filter kind, game tag, recipient and known author, with no `since`, `until` or
`limit`, so newest-first history cannot exclude the original sequence zero.

Events from unrelated subscriptions are ignored before body decoding; unrelated
kind/tags/authors are ignored before signature decoding. Matching frames reject
duplicate object fields, invalid JSON types, invalid keys/signatures, malformed
payloads and inconsistent message identity/session/role. The first canonical,
authenticated P2 sequence-zero KeyOffer pins the creator's peer. Subsequent
proof rejection in the game does not select another peer. A restored peer pin
cannot be replaced by stale history, another author or a repeated Open.

Receive returns the same owned payload until the driver advances its durable
cursor. Pending messages are indexed by sequence; old consumed entries retain
SHA-256 payload digests. Equal public bytes with different event timestamps/IDs
are retries; unequal bytes at one sequence are sticky equivocation. Already
consumed history below a restored cursor is ignored while retaining the recorded
peer. Default retention is eight slots and four maximum payloads, including
consumed digests. Exceeding either bound fails rather than dropping messages.

A single lifetime reader owns the socket. An eight-frame queue bounds incoming
traffic; frames are limited to 128 KiB. Quiet caller deadlines do not cancel
coder/websocket Read, since that would close the connection. Receive returns nil
on its bounded quiet wait. Reader EOF, invalid framing, queue overflow and
operation errors require explicit reopen. Close may interrupt a pending
operation; it joins the reader/operation and destroys the copied key. It is
idempotent and final for this transport instance.

## Qualification — 2026-09-14

Event, payload-bound, key ownership, stored ordering, peer pinning, consumed
equivocation, exact ACK/retry, cancellation and queue tests pass natively and in
Node WASM. Race detector and vet pass. Native tests use the actual WebSocket
adapter and a loopback relay to qualify newest-first history, quiet live waits,
exact publication bytes and restoration with a saved carrier. Native/browser
game rules remain in the shared FSM; transport tests use canonical public
messages and do not replace its proof or step validation.

The complete mixed native/Brave funded-hand test and actual host relay
integration remain mandatory later acceptance gates. The prior adapter-level
Brave WebSocket qualification does not claim a funded Nostr game.
