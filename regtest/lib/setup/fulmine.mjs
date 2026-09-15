// Delegator wallet bring-up (Fulmine image, run without LND).
import { env } from '../env.mjs';
import { log, fail } from '../log.mjs';
import { waitForOrFail, fetchJson, fetchText, httpOk } from '../wait.mjs';

const JSON_HEADERS = { 'Content-Type': 'application/json' };
// Docker-internal arkd URL stored in the Fulmine wallet at creation time.
// Coupled to the `arkd` service name in docker/compose.ark.yml — keep in sync
// if that service is ever renamed.
const ARK_SERVER = 'http://arkd:7070';

async function status(base) {
  const { json } = await fetchJson(`${base}/api/v1/wallet/status`);
  return json || {};
}

async function setupWallet({ label, port }) {
  const base = `http://localhost:${port}`;

  const existing = await status(base).catch(() => ({}));
  if (existing.initialized) {
    log(`${label} wallet already initialized, skipping...`);
    return;
  }

  await waitForOrFail(`${label} service`, () =>
    httpOk(`${base}/api/v1/wallet/status`),
  );

  log(`Creating ${label} wallet...`);
  const { json: seedResp } = await fetchJson(`${base}/api/v1/wallet/genseed`);
  const privateKey = seedResp && seedResp.nsec;
  if (!privateKey) fail(`${label}: failed to generate seed`);

  await fetchText(`${base}/api/v1/wallet/create`, {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify({ private_key: privateKey, password: 'password', server_url: ARK_SERVER }),
  });
  await fetchText(`${base}/api/v1/wallet/unlock`, {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify({ password: 'password' }),
  });

  await waitForOrFail(`${label} wallet ready`, async () => {
    const s = await status(base);
    return s.initialized === true && s.synced === true && s.unlocked === true;
  });

  log(`${label} wallet setup completed`);
}

export async function setupDelegator() {
  await setupWallet({
    label: 'Delegator',
    port: env('DELEGATOR_API_PORT', '7011'),
  });
}
