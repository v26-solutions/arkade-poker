# Host lifecycle

`Client` owns one imported wallet, the physical wallet store and service clients.
`Session` exposes an unbuffered input channel and owned driver updates; the UI
never receives a mutable Game. Native uses gRPC and files; browser uses gateway
HTTP/SSE and IndexedDB. Both use the same actual wallet and Nostr implementation.

Opening the wallet first acquires its local writer lock, then discovers the
network, service keys and receive tree from the current host endpoints. This
happens on every import, whether or not a journal exists. Startup settings remain
authoritative; legacy Configured snapshots are decoded only to validate their
original event chain and wallet identity. Replay uses the newly discovered
configuration. Game effects begin only after admitted replay and matching the
imported public key. Recovery assumes the same wallet, network and services are
supplied again. The host retains the loaded wallet and writer
on a driver error; it exposes no automatic retry or new-game reset. Restart is
the existing bounded recovery attempt. An explicit ClearSavedGame operation
cancels and joins the session, releases its writer, and clears only the requested
wallet's local history. An already imported key transfers to the client before
shutdown and is reused only after all old workers stop. Clear opens a fresh
session and returns it to the UI without requiring wallet ingress. A clear or
reopen failure retains the key for an explicit retry; Close destroys it even if
no replacement session opened. Clear also works after Open fails, without replay
or service calls when no wallet was loaded. It does not refund funds or settle a hand. Close cancels and joins the driver before
closing clients/store and destroying the imported key. No key ingress text is
ever an argument to storage or a service operation.

`AbortSetup` uses the same stop/join and key transfer, but checks the durable
current segment before clearing. Only setup events are allowed; any prepared,
signed, submitted or observed funding/spend record prevents deletion. The check
runs after the worker stops, so a stale UI cannot erase funding recovery data.
An unreadable log also prevents deletion. This check only opens storage and
decodes records; it never runs recovery or contacts transaction services.

After service connection and admitted replay, a separate wallet watcher attaches
to the default receive script before querying the balance. Change events trigger
another query; a stream gap or query failure marks the display unavailable and
retries attachment after one second. Each successful attachment refetches the
balance, covering changes during disconnection. Browser hosts use the existing
HTTP/SSE adapter and native hosts use gRPC. Balance updates are transient and
keep only the latest result, so UI delays never block the watcher or driver.
The watcher remains alive at a terminal outcome or driver error. Close joins it
before closing shared services; NewGame replaces it with the new connections.
This read-only refresh loop does not retry game work or alter recovery policy.

The physical wallet log is a concatenation of canonical game logs. Each starts
with a wallet-bound Configured marker containing no runtime settings. Before
selecting a later segment, earlier segments must replay
to a terminal outcome (accepted settlement or admitted pre-lock setup abort).
The active segment uses relative event/storage indices over the absolute physical
append index. Completed records remain unchanged. NewGame is received only after
the driver returns a terminal outcome; the physical writer stays held while the
next start marker is appended. A crash before that append restores the previous
terminal screen; a crash afterward restores the new game. No separate mutable
"current game" pointer or multi-game coin reservation is involved. NewGame keeps
completed history; explicit ClearSavedGame removes all games for that wallet.

Setup forms may call `ValidateSetup`, which creates a transient configured Game
and invokes its pure Decide admission. This prevents invalid relay/terms/service
bindings from reaching the active driver without creating secrets or effects.
Actual input admission remains in the driver.

Tests cover fresh/restarted configuration, changed host endpoints, imported-key
matching, replay failure after discovery but before game effects, retained writer ownership,
completed-history preservation and rejection of boundaries after unfinished
games. Native race and Node WASM pass. Live funded integration remains step 8.
