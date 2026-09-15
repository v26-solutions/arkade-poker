// arkd bring-up: create/unlock/sync the server wallet, fund it, set intent fees.
// One code path — there is no longer a "nigiri built-in arkd" variant.
import { env } from '../env.mjs';
import { log, warn, fail } from '../log.mjs';
import { waitFor, waitForOrFail, fetchJson, fetchText, httpOk } from '../wait.mjs';
import { faucet, mine } from '../chain.mjs';
import { dockerExec } from '../proc.mjs';

// Host-mapped arkd endpoints. Evaluated lazily (not at import time) so they pick
// up ARKD_PORT / ARKD_ADMIN_PORT after loadEnv() has populated process.env —
// these must match the host port mappings in docker/compose.ark.yml.
const arkdUrl = () => `http://localhost:${env('ARKD_PORT', '7070')}`;
const arkdAdminUrl = () => `http://localhost:${env('ARKD_ADMIN_PORT', '7071')}`;
const JSON_HEADERS = { 'Content-Type': 'application/json' };

async function isInitialized() {
  // arkd exposes its signer pubkey on /v1/info only once the wallet is created
  // and unlocked, so its presence is a reliable "already set up" signal.
  const { json } = await fetchJson(`${arkdUrl()}/v1/info`);
  return Boolean(json && (json.signerPubkey || json.pubkey));
}

async function walletStatus() {
  const { json } = await fetchJson(`${arkdAdminUrl()}/v1/admin/wallet/status`);
  return json || {};
}

async function setupWallet() {
  const password = env('ARKD_PASSWORD', 'secret');

  // arkd only serves its admin endpoint once it has connected to arkd-wallet,
  // which in turn waits on nbxplorer + bitcoind. On cold/loaded hosts that whole
  // chain (and any Docker restart backoff while it settles) can take a while.
  await waitForOrFail('arkd admin endpoint', () =>
    httpOk(`${arkdAdminUrl()}/v1/admin/wallet/status`),
    { attempts: 120, intervalMs: 3000 },
  );

  let status = await walletStatus();
  if (!status.initialized) {
    log('Creating arkd server wallet...');
    const { json: seedResp } = await fetchJson(`${arkdAdminUrl()}/v1/admin/wallet/seed`);
    const seed = seedResp && seedResp.seed;
    if (!seed) fail('Failed to generate wallet seed');
    const { text } = await fetchText(`${arkdAdminUrl()}/v1/admin/wallet/create`, {
      method: 'POST',
      headers: JSON_HEADERS,
      body: JSON.stringify({ seed, password }),
    });
    log(`Server wallet created: ${text}`);
  } else {
    log('arkd server wallet already initialized');
  }

  status = await walletStatus();
  if (!status.unlocked) {
    log('Unlocking arkd server wallet...');
    await fetchText(`${arkdAdminUrl()}/v1/admin/wallet/unlock`, {
      method: 'POST',
      headers: JSON_HEADERS,
      body: JSON.stringify({ password }),
    });
  }

  await waitForOrFail(
    'arkd wallet sync',
    async () => (await walletStatus()).synced === true,
    { attempts: 60, intervalMs: 3000 },
  );
  log('arkd wallet synced');

  // Fund the SERVER wallet with 21 confirmed txs so fee estimation has history.
  const { json: addrResp } = await fetchJson(`${arkdAdminUrl()}/v1/admin/wallet/address`);
  const serverAddr = addrResp && addrResp.address;
  if (!serverAddr) {
    warn('Could not get arkd server wallet address; skipping funding');
    return;
  }
  log(`Funding arkd server wallet at ${serverAddr} (21 txs for fee estimation)...`);
  for (let i = 0; i < 21; i++) faucet(serverAddr, 1);
  mine(1); // confirm the batch (faucet no longer mines per-tx)
  const { text: balance } = await fetchText(`${arkdAdminUrl()}/v1/admin/wallet/balance`);
  log(`Server wallet balance: ${balance}`);
}

const FEE_FREE = {
  offchainInputFee: '0.0',
  onchainInputFee: '0.0',
  offchainOutputFee: '0.0',
  onchainOutputFee: '0.0',
};

function postIntentFees(fees) {
  return fetchText(`${arkdAdminUrl()}/v1/admin/intentFees`, {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify({ fees }),
  });
}

export async function applyArkdFees() {
  log('Configuring arkd intent fees...');
  try {
    await postIntentFees({
      offchainInputFee: env('ARK_OFFCHAIN_INPUT_FEE'),
      onchainInputFee: env('ARK_ONCHAIN_INPUT_FEE'),
      offchainOutputFee: env('ARK_OFFCHAIN_OUTPUT_FEE'),
      onchainOutputFee: env('ARK_ONCHAIN_OUTPUT_FEE'),
    });
    const { text } = await fetchText(`${arkdAdminUrl()}/v1/admin/intentFees`);
    log(`arkd fees configured: ${text}`);
    refreshArkClientConfig();
  } catch {
    warn('Failed to set arkd fees (admin endpoint unavailable?)');
  }
}

// The ark CLI persists the server params (including the intent fee programs)
// at `ark init` time and never refreshes them (arkd >= v0.9.16 dropped the
// per-settle GetInfo). Since the CLI is initialized while fees are zeroed for
// the funding phase, re-init it with its own key after the real fees are
// applied so `ark settle` computes the fees the server now expects.
function refreshArkClientConfig() {
  const password = env('ARKD_PASSWORD', 'secret');
  const explorer = env('ARK_CLIENT_EXPLORER', 'http://mempool_web/api');
  const dump = dockerExec('arkd', ['ark', 'dump-privkey', '--password', password], { capture: true });
  if (dump.code !== 0) return; // ark client not initialized — nothing to refresh
  let prvkey;
  try {
    prvkey = JSON.parse(dump.stdout).private_key;
  } catch {
    warn('Failed to parse ark dump-privkey output; ark client fee config may be stale');
    return;
  }
  const init = dockerExec(
    'arkd',
    ['ark', 'init', '--password', password, '--prvkey', prvkey, '--server-url', 'http://localhost:7070', '--explorer', explorer],
    { capture: true },
  );
  if (init.code !== 0) {
    warn(`ark client config refresh failed: ${init.stderr || init.stdout}`);
  } else {
    log('ark client config refreshed with final intent fees');
  }
}

// Initialize the `ark` client CLI inside the arkd container so `ark receive`,
// `ark balance`, `ark send`, etc. work out of the box, and seed it by boarding
// confirmed regtest bitcoin into a round.
async function setupArkClient() {
  const password = env('ARKD_PASSWORD', 'secret');
  const explorer = env('ARK_CLIENT_EXPLORER', 'http://mempool_web/api');

  if (dockerExec('arkd', ['ark', 'config'], { capture: true }).code !== 0) {
    log('Initializing ark client CLI...');
    const init = dockerExec(
      'arkd',
      // This runs INSIDE the arkd container, where arkd always listens on the
      // fixed internal port 7070 — NOT the host-mapped ARKD_PORT. (The explorer
      // URL is likewise an in-network container address.) Don't use arkdUrl()
      // here; that's the host-side mapping, used only by the fetch() calls above.
      ['ark', 'init', '--password', password, '--server-url', 'http://localhost:7070', '--explorer', explorer],
      { capture: true },
    );
    if (init.code !== 0) {
      warn(`ark client init failed: ${init.stderr || init.stdout}`);
      return;
    }
  } else {
    log('ark client already initialized');
  }

  // Idempotent: skip funding if the client already holds offchain balance.
  let total = 0;
  try {
    total = JSON.parse(dockerExec('arkd', ['ark', 'balance'], { capture: true }).stdout).offchain_balance.total;
  } catch {
    /* not funded yet */
  }
  if (total > 0) {
    log('ark client wallet already funded');
    return;
  }

  const receive = dockerExec('arkd', ['ark', 'receive'], { capture: true });
  let boardingAddress;
  try {
    boardingAddress = JSON.parse(receive.stdout).boarding_address;
  } catch {
    fail(`ark client receive failed: ${receive.stderr || receive.stdout}`);
  }
  if (!boardingAddress) fail('ark client did not return a boarding address');

  log(`Funding ark client boarding address with 1 BTC...`);
  if (!faucet(boardingAddress, 1, { confirm: true })) {
    fail('ark client boarding transaction failed');
  }
  await waitForOrFail('ark client boarding settlement', () => {
    const settle = dockerExec('arkd', ['ark', 'settle', '--password', password], { capture: true });
    return settle.code === 0;
  }, { attempts: 30, intervalMs: 500 });
  log('ark client settled boarding funds');
  mine(3);
  await waitForOrFail('ark client offchain funds', () => {
    try {
      const balance = JSON.parse(dockerExec('arkd', ['ark', 'balance'], { capture: true }).stdout);
      return Number(balance.offchain_balance?.total) > 0;
    } catch {
      return false;
    }
  }, { attempts: 30, intervalMs: 1000 });
}

export async function setupArkd() {
  if (await isInitialized()) {
    log('arkd wallet already initialized, skipping wallet setup...');
  } else {
    await setupWallet();
  }
  // Zero intent fees for the funding phase. The real fees are applied once at
  // the end of start(), after the test wallets are ready.
  await postIntentFees(FEE_FREE);
  await setupArkClient(); // init + fund (with fees zeroed)
}
