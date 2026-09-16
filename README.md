<div align="center">

<pre>
▄▀█ █▀█ █▄▀ ▄▀█ █▀▄ █▀▀
█▀█ █▀▄ █ █ █▀█ █▄▀ ██▄

█▀█ █▀█ █▄▀ █▀▀ █▀█
█▀▀ █▄█ █ █ ██▄ █▀▄

HEADS-UP // SATS HOLD'EM
</pre>

</div>

Arkade Poker is heads-up Texas Hold'em played with bitcoin on Arkade.
[Play in your browser](https://v26-solutions.github.io/arkade-poker/)
or run the terminal UI locally.

## Run the terminal UI

With Nix installed and flakes enabled, run:

```sh
nix run github:v26-solutions/arkade-poker
```

Both the browser and terminal UI connect to Mutinynet by default. Add a wallet,
fund its receive address with Mutinynet sats, then create a session or join one
using an invitation from another player.

## TL;DR

1. **Agree on a game.** One player creates a session with a stake, bond, minimum
   bet and maximum wager, then shares the invitation with their opponent. The
   clients exchange signed setup messages through a Nostr relay.
2. **Shuffle together.** Both players shuffle an encrypted deck and provide
   cryptographic proofs that their shuffle is valid. Cards are revealed with
   verifiable decryption shares, keeping each player's hole cards private until
   showdown.
3. **Fund and play.** Players deposit their stake and bond through Arkade, then
   check, bet, raise or fold through the usual preflop, flop, turn and river
   rounds. Bets come from each player's wallet. Poker covenant scripts, checked
   by the Arkade emulator service, govern valid moves and payouts.
4. **Settle the hand.** A fold, showdown or timeout determines the payout under
   the agreed rules. At showdown, Merkle proofs establish each hand's rank.
   The clients verify accepted Arkade transactions before reporting a result.

The terminal and browser clients share the same Go game engine, with the browser
running it as WebAssembly. Game progress is saved locally so a session can be
resumed with the same wallet, network and services.

If setup gets stuck before funding, click **Abort setup** or press **B**, then
confirm. This stops setup, deletes the wallet's saved game files on this device,
and returns to Create/Join with the wallet still loaded. Each player must abort
on their own device and use a new invitation. If funding has already begun,
abort retains the saved game so it can be resumed.

Click **[?] Help** at the far left of the status bar, or press **?**, for a quick
guide to setup, play and recovery. Scroll with the arrow keys, Page Up/Down or
mouse wheel; **Esc** returns to your table or form without losing input.
**[L] Copy Logs** stays at the top of Help, with copy success or failure below it.

Press **L** (or **l**) to copy diagnostic logs in either the terminal or browser.
The shortcut works during waits and in confirmation dialogs; text fields keep
both letters as normal input. Outside Help, copy feedback appears in the status bar.
Logs cover the current run and retain up to 2,048 recent entries (1 MiB), with
private-key fields, key encodings and mnemonic phrases redacted before retention.
Long hexadecimal values are conservatively redacted too. The browser also writes
the same redacted entries to its developer console. Diagnostic logs exclude
private recovery records; the clipboard buffer resets when the application restarts.

Native also saves the full run's redacted logs to `last.log` alongside the session
data: `POKER_DATA_DIR`, or `arkade-poker-go` under the OS user configuration
directory. The file remains after exit and is replaced on the next normal launch.
These commands run without opening the TUI or importing a wallet:

```sh
poker --show-last-logs
poker --clear-session-data
```

Clear shows the data directory and requires typing `DELETE` before removing saved
sessions for all wallets. It keeps `last.log` and the persistent wallet lock files,
and refuses to clear if a saved session is in use. Neither command replaces the
last logs.

## How it works

### Game configuration

The creator chooses the following settings. The invitation includes them so the
joining player can review the terms before accepting. All amounts are in sats
and apply equally to both players.

| Setting | Default | Purpose |
| --- | --- | --- |
| Stake | 5,000 sats | Each player's initial contribution to the pot, like an ante. It is at risk even if they make no further bets. |
| Bond | 5,000 sats | A separate deposit that encourages players to finish the protocol. Both players get their bonds back on a fold or normal showdown; a player who times out forfeits their bond along with their stake and wagers. |
| Minimum bet | 500 sats | The smallest opening bet and the base minimum raise increment. When facing a raise, the next raise must at least match its increment, unless it reaches the maximum wager. |
| Maximum wager | 100,000 sats | The most each player can wager across the entire hand, excluding their stake and bond. Choosing **All in** raises their cumulative wager to this limit. |
| Nostr relay | `wss://nos.lol` | The relay both clients use to exchange signed setup messages, public keys, encrypted decks and shuffle proofs. |

With the defaults, each player initially deposits 10,000 sats: a 5,000-sat stake
plus a 5,000-sat bond. They can contribute up to another 100,000 sats in bets
over the hand, for a maximum total contribution of 110,000 sats each. Bets are
funded from the wallet as they are made. Amounts must be positive whole sats,
and the minimum bet cannot exceed the maximum wager.

### Setup over Nostr, before funding

The players negotiate their public keys and shuffled deck over Nostr **before
either player funds the covenant script**. The creator is Player 1; the joining
player is Player 2. Setup proceeds in this order:

1. **Invitation.** Player 1 shares an invitation containing the game settings,
   relay and session identity. Player 2 reviews and accepts it.
2. **Key exchange.** Player 2 sends a key offer and Player 1 replies. Each offer
   includes a wallet public key, a session encryption public key, a payout
   script and proofs of key ownership. Nostr messages use a separate session
   signing key; private wallet and encryption keys stay on the player's device.
3. **First shuffle.** Player 2 shuffles and encrypts the 52-card deck under the
   combined encryption key, then publishes the encrypted deck and shuffle proof.
4. **Second shuffle.** Player 1 verifies that proof, shuffles and re-encrypts the
   deck, then publishes the final deck, a new proof and the initial deadline.
   Player 2 verifies the final shuffle and checks the deadline.
5. **Derive the covenant.** Both clients independently derive the same covenant
   from the agreed terms, player keys, payout scripts, encrypted dealt cards and
   deadline. Funding begins only after this setup has been verified locally.

The ownership and shuffle proofs are bound to this session and its participants.
The relay carries the setup messages; each client verifies them and derives the
covenant itself.

The initial covenant deadline is five minutes after Player 1 generates the final
shuffle proof. Funding requires at least 30 seconds remaining, leaving about
four and a half minutes for final verification, relay delivery and both funding
steps. Live actions continue to advance the covenant deadline by 60 seconds.

### Verifiable shuffling and card reveals

The shuffle implementation was ported to Go from
[Ziffle](https://github.com/v26-solutions/ziffle/blob/master/README.md), which uses
**Bayer–Groth (2012) zero-knowledge shuffle proofs**. Each proof establishes that
the output deck is a permutation and re-encryption of the input deck: no cards
were added, removed or replaced. It does so without revealing the permutation
or the secret re-encryption randomness.

Both players contribute a shuffle, and decrypting a card requires a share from
each player's encryption key. For private hole cards, the opponent supplies
their share and the owner completes decryption locally. For community cards
and showdown, the required shares are published. Each published share comes
with a Chaum–Pedersen proof that it was computed correctly for that card and
player's key.

This lets the players shuffle and deal without a trusted dealer. The Go
implementation and proof formats are described in the
[shuffle protocol notes](internal/shuffle/protocol.md).

### Hand evaluation with Merkle proofs

At showdown, each player's hand consists of their two hole cards and the five
community cards. Its rank is the strength of the best five-card combination
among those seven cards, including kickers. Higher ranks mean stronger hands;
equal ranks mean a split pot.

The game uses a fixed Merkle tree committing to all 133,784,560 possible
seven-card combinations and their best-five ranks. Each leaf binds a specific
set of seven cards to its rank. The tree's root hash is embedded in the covenant
script, fixing the ranking table before the game is funded.

The client preparing settlement supplies the revealed cards, both players'
ranks and a Merkle inclusion proof for each rank. The covenant then:

1. Checks that the nine revealed cards are distinct and match the encrypted
   deal using the accepted decryption shares.
2. Constructs each player's seven-card set from their hole cards and the shared
   board.
3. Hashes each card set together with its claimed rank and follows the supplied
   Merkle proof to the fixed root. Changing either the cards or the rank makes
   the proof fail.
4. Compares the two proven ranks and enforces the corresponding payouts.

Each proof contains 27 sibling hashes, totalling 864 bytes. This lets the
covenant verify a compact proof of the hand's rank without running the full
poker evaluator in script. The client-side evaluation and proof generation live
in the [hand-rank implementation](internal/merkel/proof.go).

### Funding, betting and settlement

Once setup is complete, Player 2 deposits their stake and bond into the agreed
covenant, then Player 1 adds theirs. The covenant fixes the encrypted deal: two
hole cards for each player and five community cards. Card reveals and betting
then advance the hand through Arkade transactions.

A covenant constrains how its funds can be spent. Here, its scripts check the
allowed game transitions, wager amounts, card reveal proofs, deadlines and
payouts. The Arkade emulator service executes these scripts, and Arkd processes
the transactions. Clients follow accepted transactions to establish the current
game state.

The pot consists of both stakes plus all wagers; bonds are accounted for
separately. Settlement follows these rules:

- **Fold:** the opponent receives the pot, and each player receives their bond
  back.
- **Showdown:** the scripts verify the revealed cards and Merkle hand-rank proofs.
  The stronger hand wins the pot, or the pot is split on a tie. Both bonds are
  returned.
- **Timeout:** if a player misses a required action's deadline, their opponent
  can claim all funds currently locked in the covenant, including the missing
  player's bond.

See [Build and source distribution](RELEASE.md) for development commands,
configuration and local regtest setup.

## Licence

[MIT](LICENSE).
