#!/usr/bin/env node
// arkade-regtest orchestrator — cross-platform, zero-dependency.
//
//   node regtest.mjs start [--env <path>] [--clean] [--profile <name>...]
//   node regtest.mjs stop
//   node regtest.mjs clean [--prune]
//   node regtest.mjs faucet <address> <amountBtc>
//   node regtest.mjs mine [n]
//   node regtest.mjs rpc <args...>     (bitcoin-cli passthrough, in the bitcoin container)
//   node regtest.mjs rotate-signer [--cutoff <secs>] [--new-key <hex>]  (rotate operator signer; deprecate the previous)
//   node regtest.mjs set-signers --active <priv> [--deprecated <priv>[:<cutoff>],...]  (apply an explicit signer set)
//   node regtest.mjs signer-info       (print the active + deprecated signer set)
//
// Profiles (and their dependencies) let you bring up a subset of the stack:
//   ark → base,  delegate → ark,  lightning → ark,  emulator → ark,
//   solver → ark + emulator,  intent-solver → ark + emulator + lightning + nostr,
//   sync → base,  nostr → base. `--profile lightning` brings up base+ark+lnd-peer;
//   `--profile sync` / `--profile nostr` skip the Ark stack entirely.
//   Selection precedence: --profile flags > REGTEST_PROFILES env (comma-list)
//   > full stack.
//
// Replaces the old bash scripts + the nigiri binary entirely.
import { randomBytes } from 'node:crypto';
import { loadEnv, env } from './lib/env.mjs';
import { log, warn, fail } from './lib/log.mjs';
import { ROOT, composeUp, composeStop, composeDown } from './lib/compose.mjs';
import { docker, dockerExec, containerName } from './lib/proc.mjs';
import { DEFAULT_PROFILES, PROFILE_DEPS, resolveProfiles } from './lib/profiles.mjs';
import { sleep, waitForOrFail, httpOk, fetchJson } from './lib/wait.mjs';
import { bitcoinCli, bootstrapChain, mine, faucet, reorg } from './lib/chain.mjs';
import { setupArkd, applyArkdFees } from './lib/setup/arkd.mjs';
import { setupDelegator } from './lib/setup/fulmine.mjs';
import { setupLightning } from './lib/setup/lightning.mjs';
import { setupSolver } from './lib/setup/solver.mjs';
import { createInvoice, payInvoice } from './lib/invoice.mjs';
import { rotateSigner, setSigners, signerInfo, clearSignerState } from './lib/setup/signer.mjs';
import { evmRpc, receiveRfqRequest, sendRfqRequest } from './lib/evm.mjs';

function parseArgs(argv) {
  const opts = { command: argv[0], env: '', clean: false, prune: false, confirm: false, cutoff: undefined, newKey: undefined, active: undefined, deprecated: undefined, profiles: [], positional: [] };
  for (let i = 1; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--env') opts.env = argv[++i] || fail('--env requires a path');
    else if (a === '--clean') opts.clean = true;
    else if (a === '--prune') opts.prune = true;
    else if (a === '--confirm') opts.confirm = true;
    else if (a === '--profile') {
      const val = argv[++i] || fail('--profile requires a name');
      opts.profiles.push(...val.split(',').map((s) => s.trim()).filter(Boolean));
    }
    else if (a === '--cutoff') opts.cutoff = argv[++i] || fail('--cutoff requires a value (unix seconds, or +N/-N seconds from now)');
    else if (a === '--new-key') opts.newKey = argv[++i] || fail('--new-key requires a 32-byte hex value');
    else if (a === '--active') opts.active = argv[++i] || fail('--active requires a 32-byte hex private key');
    else if (a === '--deprecated') opts.deprecated = argv[++i] ?? fail('--deprecated requires <priv>[:<cutoff>],...');
    else if (a === '--build') { /* legacy no-op: there is no build artifact anymore */ }
    else opts.positional.push(a);
  }
  return opts;
}

// Resolve a --cutoff CLI value to a Unix-seconds timestamp for arkd. An absolute
// value (e.g. 1781344800) is used as-is; a signed value (+N / -N) is N seconds
// from now (future => MIGRATABLE, past => EXPIRED). Absent => no cutoff (DUE_NOW).
function resolveCutoff(raw) {
  if (raw == null) return undefined;
  const n = Number(raw);
  if (!Number.isFinite(n)) {
    fail(`--cutoff must be a number: unix seconds (e.g. 1781344800) or +N/-N seconds from now (got "${raw}")`);
  }
  const relative = /^[+-]/.test(String(raw).trim());
  return relative ? Math.floor(Date.now() / 1000) + n : Math.trunc(n);
}

async function startEmulator() {
  if (!env('EMULATOR_IMAGE')) {
    log('Emulator disabled (EMULATOR_IMAGE empty), skipping...');
    return;
  }
  const port = env('EMULATOR_PORT', '7073');
  log(`Starting emulator overlay (${env('EMULATOR_IMAGE')})...`);
  composeUp(['emulator'], { profiles: ['emulator'] });
  await waitForOrFail('emulator /v1/info', () => httpOk(`http://localhost:${port}/v1/info`));
  const { json } = await fetchJson(`http://localhost:${port}/v1/info`);
  log(`Emulator up at http://localhost:${port} (signerPubkey: ${json?.signerPubkey || '?'})`);
}

async function startCovclaimd() {
  if (!env('COVCLAIMD_IMAGE')) {
    log('covclaimd disabled (COVCLAIMD_IMAGE empty), skipping...');
    return;
  }
  const port = env('COVCLAIMD_HTTP_PORT', '7271');
  log(`Starting covclaimd overlay (${env('COVCLAIMD_IMAGE')})...`);
  composeUp(['covclaimd'], { profiles: ['covclaimd'] });
  await waitForOrFail('covclaimd pubkey', () =>
    httpOk(`http://localhost:${port}/v1/preimage/covclaimd-pubkey`),
  );
  log(`covclaimd up at http://localhost:${port} (reveal enabled)`);
}

// The swap solver, started last of the app tiers: its Lightning side is the
// base `lnd`, which only has a funded, balanced channel once setupLightning() has
// run. `serve` answers /healthz, so that — not a bare open port — is what
// readiness is measured on.
async function startIntentSolver() {
  if (!env('INTENT_SOLVER_IMAGE')) {
    log('intent-solver disabled (INTENT_SOLVER_IMAGE empty), skipping...');
    return;
  }
  const port = env('INTENT_SOLVER_PORT', '8787');
  log(`Starting intent-solver overlay (${env('INTENT_SOLVER_IMAGE')})...`);
  composeUp(['intent-solver'], { profiles: ['intent-solver'] });
  await waitForOrFail('intent-solver /healthz', () => httpOk(`http://localhost:${port}/healthz`), {
    attempts: 45,
    intervalMs: 2000,
  });
  log(`intent-solver up at http://localhost:${port} (LN backend: lnd)`);
  fundIntentSolver();
}

// Give the solver Arkade float.
//
// Its Lightning side is funded by setupLightning(), but nothing funded the
// Arkade side, and the two are not interchangeable: the RECEIVE corridors have
// the solver fund a lockup out of its own Arkade balance, so with zero float it
// quotes and then cannot fill. The send direction hides this — there the client
// funds and the solver only needs LN outbound — which is why an unfunded solver
// still looks healthy.
//
// The address cannot be hardcoded even though INTENT_SOLVER_MNEMONIC is fixed:
// an Arkade address commits to the operator pubkey, which is per-stack. So ask
// the container, via the same CLI entrypoint the image runs.
function fundIntentSolver(service = 'intent-solver', sats = env('INTENT_SOLVER_FLOAT_SATS', '5000000')) {
  if (sats === '0') return log('intent-solver float disabled (INTENT_SOLVER_FLOAT_SATS=0)');

  const cli = ['node', '--enable-source-maps', '--experimental-eventsource', 'packages/solver-app/dist/cli.js'];
  const out = dockerExec(service, [...cli, 'balances'], { capture: true });
  const address = /arkade address:\s*(\S+)/.exec(out.stdout)?.[1];
  if (!address) return warn(`${service} float skipped: no address in \`balances\` (${out.stderr || out.stdout})`);

  // Idempotent across restarts: the datadir volume survives `stop`/`start`.
  if (/arkade balance:[\s\S]*?"available"\s*:\s*(?!0\b)\d+/.test(out.stdout)) {
    return log(`${service} already holds Arkade float`);
  }

  log(`Funding ${service} with ${sats} sats of Arkade float (${address})...`);
  const send = dockerExec(
    'arkd',
    ['ark', 'send', '--to', address, '--amount', sats, '--password', env('ARKD_PASSWORD', 'secret')],
    { capture: true },
  );
  if (send.code !== 0) warn(`${service} float failed: ${send.stderr || send.stdout}`);
}

async function startEvmInfrastructure() {
  log('Starting isolated Anvil, EVM price feed, and runtime initializer...');
  const up = composeUp(['anvil', 'evm-pricefeed', 'evm-init'], { profiles: ['evm-e2e'] });
  if (up.code !== 0) fail('EVM infrastructure compose up failed');

  const rpcUrl = `http://localhost:${env('EVM_RPC_PORT', '28545')}`;
  await waitForOrFail('Anvil chain id 31337', async () => (await evmRpc(rpcUrl, 'eth_chainId')) === '0x7a69');
  await waitForOrFail('EVM price feed', async () => {
    const { json } = await fetchJson(`http://localhost:${env('EVM_PRICEFEED_PORT', '28088')}/btc-weth`);
    return json?.btc?.weth === '1';
  });
  let initState;
  await waitForOrFail('EVM runtime initialization', () => {
    const state = docker(['inspect', '--format', '{{json .State}}', containerName('evm-init')], { capture: true });
    if (state.code !== 0) return false;
    initState = JSON.parse(state.stdout);
    return initState.Status === 'exited';
  }, { attempts: 60, intervalMs: 1000 });
  if (initState.ExitCode !== 0) {
    const logs = docker(['logs', containerName('evm-init')], { capture: true });
    fail(`EVM runtime initialization failed: ${logs.stderr || logs.stdout}`);
  }
}

function arkProbeAddress() {
  const result = dockerExec('arkd', ['ark', 'receive'], { capture: true });
  if (result.code !== 0) fail(`ark probe address failed: ${result.stderr || result.stdout}`);
  try {
    const parsed = JSON.parse(result.stdout);
    const address = parsed.offchain_address || parsed.address || parsed.boarding_address;
    if (address) return address;
  } catch {}
  const address = /\b(?:tark|ark)1[0-9a-z]+\b/.exec(result.stdout)?.[0];
  if (!address) fail(`ark probe address missing: ${result.stdout}`);
  return address;
}

async function assertEvmRfqReady(service, port, direction, arkAddress) {
  const token = '0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2';
  const base = {
    token,
    arkAddress,
    evmAddress: env('EVM_CLIENT_ADDRESS', '0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc'),
    paymentHash: randomBytes(32).toString('hex'),
    rfqId: randomBytes(32).toString('hex'),
  };
  const request = direction === 'send'
    ? sendRfqRequest(base)
    : receiveRfqRequest({
        ...base,
        evmAmount: '1000000000000000',
        timeoutBlock: Number(BigInt(await evmRpc(`http://localhost:${env('EVM_RPC_PORT', '28545')}`, 'eth_blockNumber'))) + 10800,
      });
  const { ok, status, json, text } = await fetchJson(`http://localhost:${port}/v1/swap`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(request),
  });
  if (!ok || json?.type !== 'rfq_quote' || json?.pair !== request.pair) {
    fail(`${service} RFQ readiness failed (${status}): ${JSON.stringify(json || text)}`);
  }
  log(`${service} RFQ ready: ${JSON.stringify(json)}`);
}

async function assertEvmCard(service, port, pair) {
  const { ok, status, json, text } = await fetchJson(`http://localhost:${port}/api/card`);
  if (!ok || !json?.cardOmitted?.some((note) => note.startsWith(`${pair} is served`))) {
    fail(`${service} card readiness failed (${status}): ${JSON.stringify(json || text)}`);
  }
  log(`${service} card ready: ${JSON.stringify(json)}`);
}

async function startEvmSolvers() {
  const services = ['intent-solver-evm-send', 'intent-solver-evm-receive'];
  log('Starting direction-isolated EVM solvers...');
  const up = composeUp(services, { profiles: ['evm-e2e'] });
  if (up.code !== 0) fail('EVM solver compose up failed');
  const sendPort = env('EVM_SEND_SOLVER_PORT', '28787');
  const receivePort = env('EVM_RECEIVE_SOLVER_PORT', '28788');
  const sendAdminPort = env('EVM_SEND_SOLVER_ADMIN_PORT', '28789');
  const receiveAdminPort = env('EVM_RECEIVE_SOLVER_ADMIN_PORT', '28790');
  await waitForOrFail('EVM send solver /healthz', () => httpOk(`http://localhost:${sendPort}/healthz`), { attempts: 60, intervalMs: 2000 });
  await waitForOrFail('EVM receive solver /healthz', () => httpOk(`http://localhost:${receivePort}/healthz`), { attempts: 60, intervalMs: 2000 });
  fundIntentSolver(services[0]);
  fundIntentSolver(services[1]);
  const address = arkProbeAddress();
  const token = '0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2';
  await assertEvmCard(services[0], sendAdminPort, `arkade:BTC->ethereum:${token}`);
  await assertEvmCard(services[1], receiveAdminPort, `ethereum:${token}->arkade:BTC`);
  await assertEvmRfqReady(services[0], sendPort, 'send', address);
  await assertEvmRfqReady(services[1], receivePort, 'receive', address);
}

// The bucket sync server needs no post-boot setup — clients create their own
// buckets on demand — so this only waits until it answers, and the banner never
// advertises a port that isn't serving yet. Its image carries no curl/wget (it's
// a native binary on runtime-deps), so this host-side poll stands in for the
// compose healthcheck it can't have.
async function waitForBucketSync() {
  const port = env('BUCKET_SYNC_PORT', '7100');
  await waitForOrFail('bucket-sync /health', () => httpOk(`http://localhost:${port}/health`));
  log(`Bucket sync up at http://localhost:${port} (Postgres DB: ${env('BUCKET_SYNC_DB', 'bucketsync')})`);
}

// strfry needs no post-boot setup — clients publish and subscribe on their own
// keys — so this only waits until it answers. It's polled over HTTP rather than
// a websocket: the relay serves its NIP-11 document on the same port, which
// keeps this on Node 18's fetch instead of a websocket client we'd have to ship.
async function waitForStrfry() {
  const port = env('STRFRY_PORT', '7777');
  const nip11 = () => fetchJson(`http://localhost:${port}/`, { headers: { Accept: 'application/nostr+json' } });
  await waitForOrFail('strfry NIP-11 document', async () => (await nip11()).json?.name != null);
  const { json } = await nip11();
  log(`strfry up at ws://localhost:${port} (strfry ${json?.version || '?'}, NIPs: ${(json?.supported_nips || []).join(',')})`);
}

function banner(active) {
  const lines = [
    '',
    '========================================',
    ' Regtest environment ready',
    '========================================',
    '',
    `  Bitcoin RPC     http://localhost:${env('BITCOIN_RPC_PORT', '18443')}  (admin1 / 123)`,
    `  Mempool / API   http://localhost:${env('MEMPOOL_WEB_PORT', '3000')}  (Esplora REST under /api)`,
    `  Fulcrum         localhost:${env('FULCRUM_TCP_PORT', '50001')}`,
    `  NBXplorer       http://localhost:${env('NBXPLORER_PORT', '32838')}`,
    `  Postgres        localhost:${env('POSTGRES_PORT', '39372')}  (trust auth; DBs: arkd, nbxplorer)`,
  ];
  if (active.has('ark')) {
    lines.push(`  Arkd            http://localhost:${env('ARKD_PORT', '7070')}   (admin :${env('ARKD_ADMIN_PORT', '7071')})`);
    lines.push(`  Arkd Wallet     http://localhost:${env('ARKD_WALLET_PORT', '6060')}`);
    lines.push(`  Web Wallet      http://localhost:${env('WALLET_PORT', '3003')}`);
    lines.push(`  Explorer        http://localhost:${env('EXPLORER_PORT', '7080')}`);
  }
  if (active.has('delegate')) {
    lines.push(`  Delegator API   http://localhost:${env('DELEGATOR_API_PORT', '7011')}`);
  }
  if (active.has('lightning')) {
    lines.push(`  Peer LND        localhost:${env('LND_PEER_RPC_PORT', env('BOLTZ_LND_RPC_PORT', '10010'))}`);
  }
  if (active.has('emulator')) {
    lines.push(`  Emulator        http://localhost:${env('EMULATOR_PORT', '7073')}`);
  }
  if (active.has('covclaimd')) {
    lines.push(`  covclaimd       http://localhost:${env('COVCLAIMD_HTTP_PORT', '7271')}`);
  }
  if (active.has('solver')) {
    lines.push(`  Solver HTTP     http://localhost:${env('SOLVER_HTTP_PORT', '7091')}`);
    lines.push(`  Solver gRPC     localhost:${env('SOLVER_GRPC_PORT', '7090')}`);
  }
  if (active.has('intent-solver')) {
    lines.push(`  Intent solver   http://localhost:${env('INTENT_SOLVER_PORT', '8787')}  (swap solver; LN backend: lnd)`);
  }
  if (active.has('evm-e2e')) {
    lines.push(`  Anvil           http://localhost:${env('EVM_RPC_PORT', '28545')}  (chain 31337)`);
    lines.push(`  EVM pricefeed   http://localhost:${env('EVM_PRICEFEED_PORT', '28088')}/btc-weth`);
    lines.push(`  EVM send        http://localhost:${env('EVM_SEND_SOLVER_PORT', '28787')}  (admin :${env('EVM_SEND_SOLVER_ADMIN_PORT', '28789')})`);
    lines.push(`  EVM receive     http://localhost:${env('EVM_RECEIVE_SOLVER_PORT', '28788')}  (admin :${env('EVM_RECEIVE_SOLVER_ADMIN_PORT', '28790')})`);
  }
  if (active.has('sync')) {
    lines.push(`  Bucket Sync     http://localhost:${env('BUCKET_SYNC_PORT', '7100')}  (DB: ${env('BUCKET_SYNC_DB', 'bucketsync')})`);
  }
  if (active.has('nostr')) {
    lines.push(`  Nostr relay     ws://localhost:${env('STRFRY_PORT', '7777')}  (strfry; NIP-11 over http://)`);
  }
  lines.push(
    '',
    `  Active profiles: ${[...active].join(', ')}`,
    `  Arkd password:   ${env('ARKD_PASSWORD', 'secret')}`,
    '',
  );
  console.log(lines.join('\n'));
}

async function start(opts) {
  if (opts.clean) await clean(opts);

  // Profile selection precedence: --profile flags > REGTEST_PROFILES env > all.
  const fromEnv = env('REGTEST_PROFILES')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
  // Default to every resolvable tier (base..solver). Not ALL_PROFILES: that set
  // also carries solver-init, a one-shot init container run on demand inside
  // setupSolver() — it is not a startable tier and resolveProfiles() rejects it.
  const requested = opts.profiles.length ? opts.profiles : fromEnv.length ? fromEnv : DEFAULT_PROFILES;
  const active = new Set(resolveProfiles(requested));

  // Emulator opt-out: clearing EMULATOR_IMAGE disables it — and the solver +
  // covclaimd, which both require the emulator.
  if (!env('EMULATOR_IMAGE')) {
    if (active.delete('emulator')) log('Emulator disabled (EMULATOR_IMAGE empty)');
    if (active.delete('solver')) warn('Solver needs the emulator; skipping it (EMULATOR_IMAGE is empty)');
    if (active.delete('covclaimd')) warn('covclaimd needs the emulator; skipping it (EMULATOR_IMAGE is empty)');
    if (active.delete('intent-solver')) warn('intent-solver needs the emulator; skipping it (EMULATOR_IMAGE is empty)');
  }

  // covclaimd opt-out: clearing COVCLAIMD_IMAGE disables it independently.
  if (!env('COVCLAIMD_IMAGE') && active.delete('covclaimd')) {
    log('covclaimd disabled (COVCLAIMD_IMAGE empty)');
  }

  // intent-solver opt-out: same idiom, but empty is the DEFAULT here — the
  // image is not published yet, so the profile stays off (full stack included)
  // until an override names a build. Nothing else in the stack depends on it.
  if (!env('INTENT_SOLVER_IMAGE') && active.delete('intent-solver')) {
    log('intent-solver disabled (INTENT_SOLVER_IMAGE empty; set it to enable the profile)');
  }

  const profiles = [...active];
  log(`Starting arkade-regtest stack (profiles: ${profiles.join(', ')})...`);
  
  // The waves below pass an empty service list, so compose starts EVERY service
  // in every profile they enable. intent-solver must not be one of them: its
  // Lightning side is the base `lnd`, which has no funds and no channel until
  // setupBoltz() has run, and it carries `restart: unless-stopped`, so starting
  // it here would crash-loop it (or leave it serving against a dead corridor)
  // for the whole of arkd + boltz setup. startIntentSolver() brings it up on its
  // own, last, and waits on /healthz. Nothing else depends on it and it declares
  // no depends_on of its own, so holding its profile back costs nothing.
  const waveProfiles = profiles.filter((p) => p !== 'intent-solver' && p !== 'evm-e2e');

  // Stagger startup when the closure is more than just base. Bringing up all
  // ~18 containers at once overwhelms Docker's embedded DNS (arkd <-> arkd-wallet
  // "server misbehaving") and races arkd-wallet against nbxplorer's first-boot
  // migration, crash-looping both. So bring up base (bitcoind, nbxplorer,
  // fulcrum, mempool, postgres) FIRST, settle the chain + explorer, and only
  // THEN start the app layer (ark, lightning, ...) against a healthy base.
  const phased = active.has('base') && active.size > 1;

  const firstWave = composeUp([], { profiles: phased ? ['base'] : waveProfiles });
  if (firstWave.code !== 0) fail('docker compose up failed');

  // base (always in any closure): wait for bitcoind RPC, fund the node wallet,
  // and wait for the explorer's Esplora API before anything tries to sync.
  await waitForOrFail('Bitcoin Core RPC', () =>
    bitcoinCli(['getblockchaininfo'], { capture: true }).code === 0,
  );
  await bootstrapChain();
  await waitForOrFail('mempool Esplora API', () =>
    httpOk(`http://localhost:${env('MEMPOOL_WEB_PORT', '3000')}/api/blocks/tip/height`),
    { attempts: 60, intervalMs: 3000 },
  );

  // Second wave: the rest of the closure, now that base is healthy. composeUp is
  // additive, so this only starts the app-layer containers.
  if (phased) {
    const appWave = composeUp([], { profiles: waveProfiles });
    if (appWave.code !== 0) fail('docker compose up failed');
  }

  // Only depends on `base`, so it is ready earliest — check it before arkd's
  // slow setup, so a bad image or unreachable database fails fast.
  if (active.has('sync')) await waitForBucketSync();
  if (active.has('nostr')) await waitForStrfry();

  if (active.has('ark')) await setupArkd();
  if (active.has('delegate')) await setupDelegator();
  if (active.has('lightning')) await setupLightning();
  if (active.has('emulator')) await startEmulator();
  if (active.has('covclaimd')) await startCovclaimd();
  if (active.has('solver')) await setupSolver();
  if (active.has('intent-solver')) await startIntentSolver();
  if (active.has('evm-e2e')) {
    await startEvmInfrastructure();
    await startEvmSolvers();
  }
  // Apply the configured arkd intent fees last — every wallet above settles/
  // redeems with fees zeroed, so this must run after all of them.
  if (active.has('ark')) await applyArkdFees();

  banner(active);
}

async function stop() {
  log('Stopping arkade-regtest stack (data preserved)...');
  composeStop();
  log('Environment stopped.');
}

async function clean(opts) {
  log(`Removing ${env('REGTEST_PROJECT', 'arkade-regtest')} containers and volumes...`);
  composeDown({ volumes: true });
  // arkd-wallet's volume is gone, so the persisted signer set no longer applies.
  clearSignerState();
  if (opts.prune) {
    log('Pruning dangling images and volumes...');
    docker(['image', 'prune', '-f']);
    docker(['volume', 'prune', '-f']);
  }
  log('Clean-up complete.');
}

async function main() {
  const argv = process.argv.slice(2);
  const opts = parseArgs(argv);
  loadEnv(ROOT, opts.env);
  const passthrough = argv.slice(1).filter((arg, index, args) => arg !== '--env' && args[index - 1] !== '--env');

  // `ark` / `arkd` are raw passthroughs into the arkd container, so forward
  // every following token verbatim (flags included) without our own parsing.
  if (argv[0] === 'ark' || argv[0] === 'arkd') {
    const res = docker(['exec', containerName('arkd'), argv[0], ...passthrough]);
    process.exitCode = res.code;
    return;
  }

  // `rpc` is a bitcoin-cli passthrough into the bitcoin container — the in-house
  // replacement for `nigiri rpc`. So `node regtest.mjs rpc getblockcount` maps to
  // `bitcoin-cli -regtest … getblockcount`, keeping downstream migrations a
  // find-replace (`nigiri rpc …` → `node regtest.mjs rpc …`, same arg shape).
  if (argv[0] === 'rpc') {
    const res = docker(['exec', containerName('bitcoin'), 'bitcoin-cli', '-regtest', '-rpcuser=admin1', '-rpcpassword=123', ...passthrough]);
    process.exitCode = res.code;
    return;
  }

  if (!opts.command) {
    fail('usage: node regtest.mjs <start|stop|clean|faucet|mine|reorg|rpc|ark|arkd|rotate-signer|set-signers|signer-info> [options]');
  }

  switch (opts.command) {
    case 'start':
      await start(opts);
      break;
    case 'stop':
      await stop();
      break;
    case 'clean':
      await clean(opts);
      break;
    case 'faucet': {
      const [address, amount] = opts.positional;
      if (!address || !amount) fail('usage: node regtest.mjs faucet <address> <amountBtc> [--confirm]');
      if (!faucet(address, amount, { confirm: opts.confirm })) fail('faucet failed');
      log(`Sent ${amount} BTC to ${address}${opts.confirm ? ' and mined 1 block' : ' (unconfirmed; mine to confirm)'}`);
      break;
    }
    case 'mine': {
      const n = parseInt(opts.positional[0] || '1', 10);
      if (!mine(n)) fail('mine failed');
      log(`Mined ${n} block(s)`);
      break;
    }
    case 'reorg': {
      const depth = parseInt(opts.positional[0] || '1', 10);
      if (!Number.isFinite(depth) || depth < 1) fail('usage: node regtest.mjs reorg [depth>=1]');
      if (!reorg(depth)) fail('reorg failed');
      log(`Reorged ${depth} block(s)`);
      break;
    }
    case 'create-invoice':
      createInvoice({ secondary: process.argv.includes('--secondary') });
      break;
    case 'pay-invoice':
      payInvoice(opts.positional[0]);
      break;
    case 'rotate-signer':
      await rotateSigner({ cutoff: resolveCutoff(opts.cutoff), newKey: opts.newKey });
      break;
    case 'signer-info':
      await signerInfo();
      break;
    case 'set-signers': {
      if (!opts.active) fail('usage: node regtest.mjs set-signers --active <priv> [--deprecated <priv>[:<cutoff>],...]');
      const deprecated = (opts.deprecated || '')
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
        .map((entry) => {
          const sep = entry.indexOf(':');
          if (sep === -1) return { priv: entry.toLowerCase() };
          return { priv: entry.slice(0, sep).toLowerCase(), cutoff: resolveCutoff(entry.slice(sep + 1)) };
        });
      await setSigners({ active: opts.active, deprecated });
      break;
    }
    default:
      fail(`unknown command: ${opts.command}`);
  }
}

main().catch((err) => {
  // fail() already printed marked errors; print anything unexpected.
  if (!err?.handled) {
    console.error(`\x1b[0;31m${err && err.stack ? err.stack : String(err)}\x1b[0m`);
  }
  process.exitCode = 1;
});
