# Shared UI

Bubble Tea Update owns transient UI state. Commands connect a wallet or send one
unbuffered player input; a single waiting command receives sequential driver
updates. Pending actions stay disabled until a changed game position or input
error arrives. Repeated observations of the old position do not enable duplicate
clicks. Forms never mutate the game and only snapshot-projected choices are shown.

The table shows the local player's known cards, known board cards, cumulative
wagers, remaining wager capacity and the pot (two stakes plus both wagers), with
bonds separately identified. Unknown cards remain hidden. The Raise by form
accepts the increase over the opponent's wager and shows the call amount, total
additional sats and resulting total bet across the hand. Its minimum/maximum
are increments derived from the driver's cumulative limits; submission converts
the increment back to a cumulative target. A changed game position closes the
form so its amount cannot be applied against a different wager. All in uses the
driver's maximum permitted raise target.
Fold, call/check, raise, reveal and timeout controls exist only when permitted.
The driver projects its already supported ClaimTimeout after source observation
and deadline maturity; Progress does not choose a claim. Snapshot Role and Terms
are public projections, not additional durable or consensus state.

Player 2 sees their hole cards after Player 1's funding/reveal is accepted.
Player 1 sees theirs after Player 2's opening check or raise is accepted and
the private opening is recorded. While awaiting that move, the table explains
why Player 1's cards are hidden and the status identifies Player 2's opening turn.

A countdown above the table (also visible in the raise and exit forms) identifies
whose move is due and shows the time remaining until the accepted state's absolute
deadline. Existing UI clock ticks refresh it independently of driver updates;
it uses minutes/seconds, adding hours for longer waits, and clamps at 00:00.
It respects cumulative deadlines instead of restarting a one-minute timer on
each turn. Expiry explains the opponent's timeout right; the waiting player's
claim-available message and button still require the driver's projected choice.
Setup without an accepted state, showdown evaluation, completed games and stopped
drivers show no countdown. The clock is transient display state only.

Create prefills editable per-player terms and a relay from the host's application
configuration (`internal/appconfig`). Its built-in terms are stake/bond 5,000
sats each, minimum bet 500 and maximum wager 100,000, with `wss://nos.lol`.
Create has labels and inputs in
consistent side-by-side columns inside one centered form block. Join decodes an invitation,
shows its terms and relay as aligned, read-only rows without input controls, then
asks to deposit the stake plus bond and join on Enter. Long relay URLs are
shortened with an ellipsis to keep the confirmation visible. The entire encoded invitation and
receive address are copied; only their display is shortened/wrapped. Long valid
invitations preserve their entire payload and keep review/copy controls visible. The terminal
screen shows the accepted payout transaction and local payout output. A stopped
driver retains its table and log, with no new-game control.

After a wallet import attempt, the bottom bar offers `X: Clear saved game`, also
when restore fails before connecting or after the driver stops. Only the wallet's
public identity is retained after a failed import. Clear has keyboard/mouse
confirmation defaulting to Cancel and explains the loss of recovery data and
that funds in a hand are not refunded. Confirmation stops the owned session and
clears all local games for that wallet, then returns to wallet entry. Import and
duplicate clear requests are disabled until completion; errors keep the clear
option available for retry. Results from the old session cannot repopulate the
screen after clear or after another import. There is no automatic deletion.

The header receive address copies on click without a Y shortcut or copy hint.
After it, the wallet balance uses grouped whole sats (for example `123,456 sats`).
Balance updates have their own waiting command and never mutate game state.
Before the first query the header shows `... sats`; a failed query or interrupted
subscription shows `Balance unavailable` until a successful refresh arrives.
Only the address's visible cells copy it; clicking the balance does nothing.

Wallet entry accepts nsec, raw hexadecimal keys and English BIP39 mnemonics. It is
password-redacted and cleared on import/cancellation. Mnemonics use the network
reported by startup Arkd discovery. If it is still missing or unavailable, Enter
retries discovery and keeps the redacted input for the next import attempt.
Native environment mnemonic import queries Arkd with a 30-second timeout before
starting the UI; raw/nsec environment imports need no network lookup. Native
Ctrl+C keeps the driver running behind Cancel-default exit confirmation. Escape
returns to the prior form without losing its input; only confirmed exit cancels
the runtime. Browser Ctrl+C/Q have no exit binding. Exit confirmation remains
visible below the usual 76x30 minimum grid. Other resize handling preserves form
contents. Keyboard, paste and action/header/exit mouse controls use the same model.
The 80-ms status-row spinner displays exactly `shuffling...` during driver proof
work and replay. App startup queries Arkd info asynchronously, independently of
wallet import. The status bar right-aligns the discovered network as
`mutinynet ●`, with `… ●` while the request is pending and `unavailable ●` on
failure. A connected session's network takes precedence over startup discovery;
reopened wallets display the network freshly discovered from their configured
Arkd endpoint. Saved journals do not override that network or the receive address.
Long status messages truncate to keep the network visible. Rendering does no
networking or proof generation.

Native tests cover secret redaction, environment/modal parser sharing, copy,
exit, legal actions, cumulative raises, stale-update gating, real card/value
projection, action mouse hit regions and minimum-grid layout. Native and WASM
builds and native race tests pass. Actual native service discovery, create,
invitation, relay wait, confirmed exit and intact-log restoration passed with a
distinct unfunded test wallet. Actual Brave wallet import, refresh/re-import of the public configuration,
duplicate-writer rejection, release/reconnection and invalid join input now pass.
See `brave-qualification.json`. After explicit user approval, actual browser
Create reached the creator's opponent wait with the approved terms. Invitation
display and copy feedback passed. Refresh and tab close/reopen each required
key re-import and restored the same invitation, terms and opponent wait. Live
table controls and the mixed funded-hand qualification remain incomplete.

Additional native qualification verified invalid-key error/redaction, Escape
clearing, and nsec modal ingress restoring the existing isolated test session.
Saved records exclude the raw key, its hex/nsec text and the invalid entered text.
A valid invitation over 4096 characters uncovered an overflowing relay review;
the bounded read-only field fixes it. Complete invite copy/join payloads,
minimum-grid layout, native race/vet and both host builds pass after that fix.

Actual Brave rendered inverse action selection as an unreadable solid green
rectangle. Selection now uses an explicit marker with bold/underlined text;
the complete selected label is visibly readable in the same browser build.
