# Native and browser shuffle qualification — 2026-09-14

Host: macOS 15.6 (24G84), Brave 150.1.92.141, Go 1.26.7. The Browser UA reports
Chrome/150 and an intentionally coarsened macOS version; the application version
and actual OS above were read from the installed bundle and `sw_vers`.

The `cmd/shuffle-qualify` harness uses the production shuffle, merkel and shared
Bubble Tea UI through the same pinned booba/frontend/renderer build pipeline.
Its only browser addition is the start button and timing/frame report. It uses
fresh `crypto/rand` session keys, 12 initial/second-shuffle pairs with verification,
then decrypts all 52 cards and generates/verifies both seven-card Merkle proofs.
It contacts no wallet, relay, Arkd or emulator and handles no funds. Its result
POST is to its loopback test server and contains only measurements.

| Operation | Native observed range | Brave observed range (median) |
| --- | --- | --- |
| Initial shuffle + proof | 40.56–43.18 ms | 576.30–624.80 ms (600.45) |
| Verify initial | 27.94–30.21 ms | 195.90–218.30 ms (203.25) |
| Second shuffle + proof | 40.71–41.96 ms | 571.20–614.80 ms (596.60) |
| Verify second | 32.67–33.32 ms | 202.70–225.80 ms (216.50) |
| All 52 reveals + verification | 83.74 ms | 103.60 ms |
| Both Merkle hand proofs (including cold initialization) | 27.60 ms | 1852.10 ms |

Native PTY (120×40): 24 UI view calls displaying the active status, all ten
spinner glyphs. The actual terminal output showed the `shuffling...` glyph
changing and then `Shuffle qualification passed`.

Brave: 259 active UI view calls, all ten spinner glyphs, 2550 browser animation
frames during the run, maximum requestAnimationFrame gap 100 ms. A screenshot
during execution showed the real status-row spinner; a subsequent screenshot
showed `Shuffle qualification passed` and the measurement report. The report's
error field is empty. The measured frame counts confirm continued event-loop
and UI progress while the crypto commands run; no worker adaptation was needed.
These observations are qualification evidence, not latency thresholds.

Raw Brave report is retained beside this file as `brave-qualification.json`.
The full-flow regression also passes in Node WASM with the exact Rust digest.
Node timings were approximately 212 ms initial/proof, 89 ms initial verification,
210 ms second/proof, 94 ms second verification and 587 ms for both hand proofs.
Node timing is separate from actual Brave/terminal responsiveness qualification.

The harness exercises package scheduling and the existing progress message/UI
contract. Integration of those commands and progress messages into the game
FSM remains step 3 and later UI work, as specified by the approved plan.
