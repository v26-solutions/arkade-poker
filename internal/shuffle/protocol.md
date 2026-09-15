# Shuffle protocol and qualification

This package implements the production Rust construction in
`crates/shuffle/src`, specialized to the poker application's 52 cards. It uses
btcec v2.3.5's pure-Go secp256k1 group and scalar operations on native and WASM.
There are no runtime-generated Pedersen bases, FFI, network calls or alternate
cryptographic backends. Arithmetic is deliberately not promised constant-time,
as approved in `go/plan.md`.

## Construction and trust boundaries

`New` validates and loads the fixed parameters, and builds the plaintext mapping
`card index i -> (i+1)*G`, for i=0..51. `parameters.bin` is exactly the 53
compressed points H, G_0, ..., G_51 in `crates/shuffle/src/params_data.rs`.
SHA256 is `6873e6d546d61c3add8b32fc4c8cc8cea42ab72bc1bfbdda2b6ac9b6141fbd9d`.
The loader checks the length, checksum, finite points, uniqueness and exclusion
of the standard generator. The Rust `tools/shuffle-paramgen` owns reproducible
RFC 9380 derivation; these bases must never be replaced by known multiples of G.

Ownership uses Schnorr (`z=w+e*sk`). Verified public keys can be summed, rejecting
empty input, invalid zero wrappers and a sum at infinity. `ShuffleInitial` starts
from `(infinity,(i+1)*G)`. `Shuffle` requires a verified predecessor. Fisher-Yates
choices use rejection-sampled 32-bit words, and re-encryption uses rejection-
sampled scalars; zero re-encryption randomness is allowed on an existing card.
The single scalar cancelling c1 is resampled. Public encrypted cards therefore
always have finite c1, while c2 may be infinity.

`bayer_groth.go` retains the reference's Bayer–Groth m=1 construction: commitments
to the permutation and x^permutation, a multi-exponentiation argument binding the
weighted predecessor and re-encryption, and the single-value product argument
proving a permutation. Verification checks every commitment, vector response,
ciphertext equation, recurrence and endpoint. Pedersen commitments and proof
ciphertexts use complete group algebra, so infinity is allowed in all their
fields. In particular, the initial shuffle's ct_mxp1.c1 is infinity.

Reveal shares are sk*c1. Chaum-Pedersen verification checks BOTH
`t_g = z*G + e*pk` and `t_c1 = z*c1 + e*share`, with `z=w-e*sk`.
`AggregateReveals` rejects empty, invalid and cancelling inputs; `RevealCard`
subtracts their sum from c2 and accepts exactly one of the 52 plaintext points.

Only successful verifiers produce `VerifiedPublicKey`, `VerifiedDeck` and
`VerifiedRevealToken`. Their zero values are invalid. Decoding a persisted deck
or proof does not recover verified status: replay must run the proofs again.
Public accessors and codecs return independent values or owned byte buffers.
As in the reference, the caller must select the expected participants, context
and ordered predecessor chain for the same session, and collect every required
player's verified share for the same card. The wrappers do not choose session
participants or impose setup/FSM rules. DLEQ binds c1, not c2; ciphertext identity
and shuffle provenance come from the verified deck and contract. No new poker
rules or FSM differences are introduced.

## Canonical formats and transcript domains

All lengths are exact: reject truncation and trailing bytes. Points are 33-byte
compressed SEC1, with 33 zero bytes representing infinity only where allowed.
Affine points are `x_le32 || y_le32`; (0,0) is infinity under the same policy.
Coordinates must be in the base field and on the curve. Scalars are exactly
32 little-endian bytes below the curve order n, without reduction. Zero is
allowed for proof responses and forbidden for secret keys. Entropy draws are
canonical big-endian candidates, rejected if >=n; nonzero secrets and nonces
also reject zero. Only public SHA256 challenges are reduced modulo n.

| Value | Bytes | Field order |
| --- | ---: | --- |
| Session secret | 32 | nonzero scalar LE32; private local data |
| Public key / reveal share | 33 | finite point |
| Ownership proof | 65 | finite a, z |
| Masked card | 66 | finite c1, c2 |
| Deck | 3432 | 52 cards in order |
| Reveal proof | 98 | finite t_g, finite t_c1, z |
| Shuffle proof | 5547 | field order below |
| Affine key/share/card/reveal proof | 64 / 64 / 128 / 160 | same point order, affine coordinates; scalar unchanged |

The shuffle proof order is c_pi, c_xpi, c_alpha, c_beta, ct_mxp0(c1,c2),
ct_mxp1(c1,c2), o_alpha[52], o_r, beta, o_beta, tau, c_d, c_sdelta, c_cdelta,
a_tilde[52], b_tilde[52], r_tilde, s_tilde: 11 points and 162 scalars. These are
reference compact v2 encodings. Their version is bound by the transcript domains,
not by an extra byte prepended to each object.

`transcript.go` specifies the full u16/u32 big-endian label, count, index and
length framing. Ownership and shuffle contexts are at most u32::MAX bytes,
including empty contexts as in the reference; applications must supply their
unique session binding. Initial domain is `ziffle/transcript/v2`, challenge
hash prefix is `ziffle/challenge/v2`; scalar domains are `ziffle/DLOG/v2`,
`ziffle/BG12/x/v2`, `ziffle/BG12/yz/v2`, `ziffle/BG12/multiexp/x/v2`, and
`ziffle/BG12/product/x/v2`. The aggregate key, ordered input/output decks and
commitments enter the transcript. The two subarguments fork the same state.

Covenant DLEQ uses its existing separate transcript:
`SHA256("ziffle/DLEQ/v2" || binding || pk_affine || share_affine || c1_affine ||
t_g_affine || t_c1_affine)`, interpreted big-endian modulo n. The five fixed-size
suffix fields make the variable binding unambiguous. `RevealBinding` returns
`"arkade/poker/reveal/v1" || contract_id[32] || semantic_slot[1]` (55 bytes),
accepting the 18 covenant slots. Both the Go verifier and actual emulator scripts
consume this exact layout. Different publishers can use different slot bindings
for their shares of the same card.

## Secrets, errors and scheduling

Nil entropy selects `crypto/rand.Reader` (Web Crypto on Go's browser target).
A supplied reader must be cryptographically secure. Test-only deterministic
readers and public fixture seeds must not be used for actual games. Entropy
failures and context cancellation propagate as errors and discard partial
keys/decks/proofs. Readers must not block indefinitely: cancellation cannot
interrupt arbitrary `io.Reader.Read` implementations.

Session keys are independent of wallet keys. `SecretKey` is a shared handle;
copying a handle does not copy the stored secret. `Destroy` is idempotent,
synchronized, clears its scalar, and invalidates future use through every copy.
An in-flight operation can finish with its snapshot. Formatting redacts secrets;
explicit `MarshalBinary` exports private local bytes for session persistence.
Arithmetic witnesses/nonces and temporary buffers are cleared where owned.
Go cannot guarantee erasure of all compiler, stack or GC copies; callers must
explicitly destroy session keys and protect/clear any exported bytes themselves.

Long prover/verifier loops check cancellation and cooperate every eight work
checkpoints. Native cooperates with the scheduler; WASM uses a short timer to
return to the JS event loop. Execute heavy work in a goroutine/Bubble Tea command,
never inside a synchronous syscall/js callback. There is no proof-generation
latency gate. The separate `cmd/shuffle-qualify` harness runs the production
package in the actual shared UI, with no wallet, services or game driver.

## Evidence and reproduction

- `protocol_test.go`: exact frozen Rust full-flow digest for two 52-card shuffles,
  all outputs/encodings/proofs and all 52 revealed cards; independent transcript
  vectors for every challenge stage; recovered cards feed Merkle hand proofs.
- `group_test.go`: independent Python integer/affine oracle digests (2048 scalar
  operations and 256 wide ECDH products), complete group/scalar boundaries,
  parameter validation, rejection sampling, secret destruction and race checks.
- `adversarial_test.go`: every one of 11 point fields, 162 scalar responses and
  104 ciphertext components mutated in both shuffle stages; key/context/order/
  predecessor substitution; invalid permutation/remask witnesses; valid zero
  blindings and c2 infinity; both DLEQ equations, all semantic slots, cancelling
  aggregates; in-flight cancellation and entropy failure discard partial work.
- `codec*_test.go`: complete length/scalar/point admission, buffer ownership,
  affine conversion and seeded fuzzing of canonical decoding and proof admission.
- `../covenant/shuffle_integration_test.go`: fresh secure-random keys and two
  shuffles, all 18 generated slot proofs through an ordinary hand in the actual
  PSBT/emulator harness, and real Merkle settlement proofs. This is a local
  cryptographic/script test, not a funded or service-accepted hand.

From the repository root, with a writable GOCACHE where needed:

```sh
go test ./...
go vet ./...
go test -race ./internal/shuffle
go test ./internal/shuffle -run '^$' -fuzz FuzzCanonicalDecoders -fuzztime=20s -parallel=2
make test-wasm
make all
go run ./cmd/shuffle-qualify
make qualify-shuffle
```

Open http://127.0.0.1:5174 in Brave and click **Start shuffle qualification**.
The separate build directory is `build/shuffle-qualification`; the local server
saves timings and frame counts to `brave-result.json`. The native harness runs
automatically, exits when finished and prints its report. See
`qualification.md` for the recorded macOS/Brave observations. Full gameplay,
network acceptance and persistence qualification belong to later plan stages.
