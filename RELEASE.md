# Build and source distribution

## Tooling

Run commands from the repository root. Native builds, browser builds, local
servers and release packaging use Go and Make. Dependencies require Go 1.26.6
or newer; the pinned Nix shell provides Go 1.26.7.

```sh
nix build               # native TUI at result/bin/poker (also nix build .#poker)
nix run                 # build and launch the native TUI
nix develop             # optional: pinned Go, Make, Node and Docker tools
make all                # build/poker and build/web/
make serve              # build and serve at http://127.0.0.1:5173
make check test-wasm    # native tests, builds, vet and Node WASM tests
make release            # build and package local archives
make clean              # remove build output
```

`GO=/path/to/go make all` selects the Go toolchain. The upstream booba WASM
builder invokes `go` internally, so put the matching toolchain on `PATH` too.

The build tool is `go run ./scripts/build`. It bundles the TypeScript browser
entry point with esbuild's Go API, pinned in `go.mod` and `go.sum`. The root
`node_modules` used to contain only esbuild and its platform executable; there
is no longer a root npm manifest, install step or package directory required.

Node remains for the regtest CLI/funding helper and `make test-wasm`, which runs
Go's JavaScript WASM test runtime. The regtest CLI uses Node's standard library
and needs no `npm install`. The optional solver bootstrap installs its own SDK
dependencies inside its Docker image (`regtest/docker/solver-init`), independently
of the application build.

The default Nix package builds only the native TUI with pure Go dependencies.
Its source includes `go.mod`, `go.sum`, `cmd/` and `internal/`; local environment
files and build output are excluded. When Go dependencies change, update
`vendorHash` in `flake.nix`: temporarily set it to `pkgs.lib.fakeHash`, run
`nix build`, then replace it with the hash reported by Nix and rebuild.

## GitHub Pages

`.github/workflows/pages.yml` builds the native Nix package and runs
`make check test-wasm` in the pinned Nix development shell on pull requests,
pushes to `master`, and manual workflow runs. Go uses the Nix toolchain with
`GOTOOLCHAIN=local`, and CI does not update `flake.lock`.

After checks pass on `master`, CI uploads `build/web/` and deploys it to GitHub
Pages. Pull requests and manual runs on other branches only build and test.
Enable **Settings > Pages > Build and deployment > Source > GitHub Actions**
in the GitHub repository, and allow `master` in the `github-pages` environment
deployment rules. The workflow uses GitHub's built-in token; no separate deploy
token or `gh-pages` branch is needed.

The Pages build uses the public Mutinynet defaults below. To change them, pass
explicit `WEB_FLAGS` to the workflow's Make command. Pages serves static files;
Arkd, emulator, delegator and relay services remain external. Verify service
access from the deployed origin, including API CORS, and smoke-test terminal
startup and WASM loading at the repository's Pages subpath.

The web app is built with `nix develop`, while `nix build` remains the native TUI.
The pinned tools do not remove the WASM reproducibility limitation documented
under Dependencies and browser compatibility.

## Application defaults and overrides

Fresh sessions default to the Mutinynet services. App startup obtains the network
from Arkd info without waiting for a wallet to be added. Native startup reads
`POKER_` environment variables; the web builder accepts explicit flags and embeds
the resolved public configuration in the browser bundle.

| Setting | Default | Native environment variable | Web build flag |
| --- | --- | --- | --- |
| Arkd | `https://mutinynet.arkade.sh` | `POKER_ARKD_URL` | `--arkd-url` |
| Emulator | `https://emulator.mutinynet.arkade.sh` | `POKER_EMULATOR_URL` | `--emulator-url` |
| Delegator | `https://delegator.mutinynet.arkade.sh` | `POKER_DELEGATOR_URL` | `--delegator-url` |
| Indexer | Resolved Arkd URL | `POKER_INDEXER_URL` | `--indexer-url` |
| Nostr relay | `wss://nos.lol` | `POKER_RELAY_URL` | `--relay-url` |
| Stake | 5,000 sats | `POKER_STAKE` | `--stake` |
| Bond | 5,000 sats | `POKER_BOND` | `--bond` |
| Minimum bet | 500 sats | `POKER_MIN_BET` | `--min-bet` |
| Maximum wager | 100,000 sats | `POKER_MAX_WAGER` | `--max-wager` |

Native nonempty environment variables override built-in defaults. Empty variables
use the defaults. Web flags override the same built-in defaults; native runtime
environment variables do not configure a web build. The indexer follows the
selected Arkd URL unless separately supplied. Arkd's `NetworkFromString` resolves
the network name reported by the server, including its Bitcoin fallback for
unknown names. There is no network environment variable or build flag.
The status bar shows the session's network at the bottom right as `mutinynet ●`.

Amounts are positive decimal whole satoshis without commas. The minimum bet must
not exceed the maximum wager, and twice the sum of stake, bond and maximum wager
must fit within Bitcoin's total supply. Invalid amount settings fail native
startup or the web build. Create session prefills these amounts and the relay;
players can edit them before creating a game. Restored sessions retain their
agreed terms; service endpoints always come from the current startup settings.

For a native override:

```sh
POKER_STAKE=7000 POKER_BOND=8000 ./build/poker
```

For a browser build, pass flags directly or through Make's `WEB_FLAGS`:

```sh
go run ./scripts/build web --stake 7000 --bond 8000
make web WEB_FLAGS='--stake 7000 --bond 8000'
make serve WEB_FLAGS='--stake 7000 --bond 8000'
make release WEB_FLAGS='--stake 7000 --bond 8000'
go run ./scripts/build web --help
```

`WEB_FLAGS` configures the web artifact built by `web`, `serve`, `all` and
`release`. Native release binaries read their settings when launched.

For the local regtest stack:

```sh
POKER_ARKD_URL=http://localhost:7070 \
POKER_EMULATOR_URL=http://localhost:7073 \
POKER_DELEGATOR_URL=http://localhost:7012 \
POKER_RELAY_URL=ws://localhost:7777 \
./build/poker

make serve WEB_FLAGS='--arkd-url http://localhost:7070 --emulator-url http://localhost:7073 --delegator-url http://localhost:7012 --relay-url ws://localhost:7777'
```

The local stack needs its `delegate` profile enabled for these examples.

Native `POKER_DATA_DIR` selects the session storage directory and
`POKER_WALLET_KEY` imports a wallet key at startup. The default storage directory
is `arkade-poker-go` under the operating system's user configuration directory.
Wallet keys are entered on the player's device and are never web build settings.
Both `POKER_WALLET_KEY` and Add Wallet accept nsec, 64 hexadecimal digits, or an
English BIP39 mnemonic (12/15/18/21/24 words, no extra passphrase). Mnemonics import
the first BIP86 receiving key at `m/86'/coinType'/0'/0/0`; Arkd supplies the network
used to select coin type 0 for Bitcoin mainnet or 1 for test networks. Arkd must
be reachable for mnemonic import. Additional HD addresses are not scanned.

Adding a wallet fetches the configured delegator's public key from
`/v1/delegator/info`. The default VTXO tree includes the owner + delegate + Arkd
leaf, matching the delegated receive address used by the Arkade web wallet.
This applies to all three key forms. A configured delegator must be reachable
and return a valid compressed public key; discovery errors fail wallet import.
Every import discovers the address and receive tree again, including when a
journal already exists. Journals store game events and exact prepared/signed
transactions, with a wallet-bound start marker instead of a configuration
snapshot. Legacy snapshots remain readable but never override startup settings.
Restart with the same wallet, network and services to resume a game; incompatible
configuration changes can prevent replay or recovery. The mnemonic and imported
wallet private key remain in memory only.

## Dependencies and browser compatibility

Arkd/emulator use versioned replacements in `go.mod` and checksums in `go.sum`.
Go downloads them into its module cache; sibling checkouts are unnecessary.

The booba frontend, Ghostty renderer and renderer WASM all come from the same
unmodified Go module selected by `go.mod`. Go's WASM runtime comes from the
selected toolchain. Browser assets have content fingerprints and a manifest;
the entry page is written after its assets.

Native builds use `CGO_ENABLED=0`, `-tags=purego`, `-trimpath` and
`-buildvcs=false`. Pure Go removes a gnark assembly include path that survives
Go's normal trimpath handling.

Browser builds retain upstream booba's WASM builder and signal/TTY stubs. The
build tool stages application sources and a copy of the pinned clipboard module
with the browser backend in a temporary directory. Native dependencies and the
module cache are left intact.

Upstream booba records its random temporary Bubble Tea replacement path in WASM
build information. Repeated WASM builds and resulting web archives can therefore
differ even with unchanged inputs. Packaging itself is deterministic for fixed
input files.

## Browser qualification

```sh
make qualify-shuffle    # build and serve on 127.0.0.1:5174
make qualify-storage    # build and serve on 127.0.0.1:5175
make qualify-transport  # build and run the transport fixture on ports 5176/5177
```

Each harness has its own directory under `build/`. The shuffle and storage
servers save bounded JSON test reports there. Stop servers with Ctrl+C. For
build-only use, run `go run ./scripts/build qualify <shuffle|storage|transport>`.
Transport fixture setup and browser test procedures are documented in
`internal/adapters/protocol.md`, `internal/storage/protocol.md` and
`internal/shuffle/protocol.md`.

`make test-regtest-hands` explicitly funds fresh test wallets using the running
local regtest faucet; see `internal/integration/README.md`.

## Archives

`make source-release` creates the source archive alone. `make release` also
builds and packages the native and browser applications. Nothing is published.
`build/releases/` receives these gzip/tar archives and SHA-256 sidecars:

- `arkade-poker-go-source.tar.gz`: Go sources/tests, browser sources, build tools,
  regtest sources/defaults, Go manifests and Nix flake/lock. Local environment
  overrides, signer state, game data and build output are excluded.
- `arkade-poker-<GOOS>-<GOARCH>.tar.gz`: native executable, toolchain version,
  dependency metadata and supplied license notices.
- `arkade-poker-web.tar.gz`: the web manifest's hash-verified files plus toolchain
  and dependency notices. Old fingerprints and qualification fixtures are excluded.

Each archive includes a content manifest, sorted paths and fixed ownership and
timestamps. Extract the source archive anywhere and run `make all` in its
top-level directory. Public dependencies resolve through checksum-pinned Go
modules using the network or the local module cache. `go mod verify` checks
those cached modules.

Serve the web archive as static files. Wallet keys and game execution remain on
the player's device. Producing release archives does not run funded-hand or
interactive browser acceptance tests.
