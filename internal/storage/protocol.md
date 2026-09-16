# Private log storage

The regtest policy in `../../plan.md` permits unencrypted local session data.
These APIs take a public wallet identity and opaque canonical game records;
they never take or persist an imported wallet private key. One OS/Web Lock owner
may write each wallet store. Independent stores/devices are not coordinated.
The game journal owns event admission and the event hash chain.

Explicit `Clear(location, publicKey)` removes all saved games for one wallet,
including records that cannot load. It takes the same exclusive lock as Open and
rejects an active writer. No private wallet key is needed. Normal Open, Close and
append never clear history automatically.

Each storage frame is `arkade-poker/log\0`, version u16(1), wallet32, index u64,
payload length u32, nonempty payload (at most 4 MiB), and SHA256 of all preceding
bytes. Integers are little endian. Host-independent framing detects truncation,
reordering, wallet substitution and damage; it is not authentication against a
writer able to rewrite the whole private store. Load returns an intact contiguous
sequence or an error, with owned buffers. No lost-data reconstruction is promised.

## Native

`Open(directory, publicKey)` uses the key's lowercase hexadecimal subdirectory
and a persistent `writer.lock` inode held with nonblocking `flock`. Process death
releases the kernel lock; the lock file must not be removed. New directories use
0700, files 0600. Directory creation synchronizes each new parent entry.

A record is `%020d.record` at its zero-based index. Append writes a same-directory
`.append-*` staging file, syncs and closes it, renames it to the record name, then
syncs the directory before acknowledgement. Restart discards only uncommitted
staging files while holding the lock; malformed or unexpected committed files
fail without truncating the log. A failed append poisons the writer until Close
and re-open. A failure after rename may leave a complete unacknowledged record;
replay resumes that intact prefix rather than overwriting it. Close retains data.
The native implementation is qualified on macOS; Windows is outside this scope.
Clear deletes record/staging files from the end and syncs the directory before
success, retaining the writer.lock inode. Unexpected files are rejected. An
interrupted or failed clear may leave records and must be explicitly retried.

Native `ClearAll` enumerates the existing lowercase wallet-key directories and
acquires every wallet lock before removing any record. It validates all directory
contents first, rejects wallet-directory symlinks, and uses the same reverse-order
record/staging removal as `Clear`. Root files (including the native `last.log`)
and unrelated directories remain untouched. The native maintenance command asks
for explicit confirmation before calling it. Wallet directories and their lock
inodes remain even when empty, so later writers still coordinate on the same lock.

## Browser

The database/lock name is `arkade-poker/log/v1/<database>/<public-key-hex>`.
An exclusive Web Lock uses `ifAvailable` and stays held until Close or page
termination. An unavailable lock/storage API returns an error without a fallback.
IndexedDB schema v1 has `records` and `meta` stores. Record keys are the same
20-digit strings; `meta["next"]` is a canonical decimal uint64 string.

Append checks the index and atomically adds the framed record and advances
`next` in a readwrite transaction with `durability: "strict"`. Unsupported strict
durability is rejected. Success is reported only by transaction `complete`,
never an individual request success. Errors/cancellation abort and await the
terminal transaction event before releasing callbacks. Failed writers must close
and reload. Late open/lock callbacks retain cleanup ownership until the browser
finishes them; version-change events close stale DB connections. Load uses one
readonly transaction and checks all cursor keys, frames and the final count.
Clear atomically empties records and resets `next` to zero in a strict readwrite
transaction without decoding the old frames.

## Qualification

Native shared tests and `go test -race ./internal/storage` pass, including process
exit without Close, duplicate writer rejection, concurrent stale-index appends,
caller-buffer ownership, interruption before/after atomic rename, an injected
directory-sync failure and malformed/gapped/wrong-wallet logs.

`qualification.json` retains the actual Brave results on macOS 15.6 (24G84),
Brave 150.1.92.141. The shared storage tests plus real IndexedDB abort and damaged
record tests pass. Cross-tab and refresh checks run separately:

1. From the repository root, run `make qualify-storage` to build and serve
   the harness (localhost only, port 5175).
2. Run the ordinary suite at `/`. For a unique disposable test identifier,
   open `/?mode=hold&store=<id>`; it acknowledges record 0 and holds the lock.
3. Open `/?mode=probe&store=<id>` in a second tab. It must report another tab
   excluded, with exit 0. Refresh the holding tab; it must recover record 0,
   acknowledge record 1 and hold ownership again.
4. Navigate the holding tab to `/?mode=cleanup&store=<id>`. It must reacquire,
   verify both records, close, remove only that disposable test database and
   report exit 0. Close the test tabs and stop the fixture server.

The observed run performed all four steps. This qualifies the storage boundary,
not complete game-driver recovery, real service submission or a funded hand.
