import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';

// Exercise the real CLI process with fake external services. In particular,
// an ambiguous send failure must never mint a note or submit another payment.
function run(t, scenario) {
  const dir = mkdtempSync(join(tmpdir(), 'poker-faucet-test-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const log = join(dir, 'calls');
  writeFileSync(log, '');
  const preload = join(dir, 'fetch.mjs');
  writeFileSync(preload, 'globalThis.fetch = async () => ({ ok: true, json: async () => ({ network: "regtest" }) });');
  const fixture = `#!${process.execPath}
const fs = require('node:fs');
const log = process.env.FAUCET_TEST_LOG;
const scenario = process.env.FAUCET_TEST_SCENARIO;
const args = process.argv.slice(2);
const command = process.argv[1].endsWith('/go') ? 'refill' : args[3];
const calls = fs.readFileSync(log, 'utf8').trim().split('\\n');
fs.appendFileSync(log, command + '\\n');
if (command === 'send') {
  if (scenario === 'ambiguous') {
    console.error('error: finalization timed out'); process.exit(1);
  }
  if (scenario !== 'funded' && !calls.includes('send')) {
    console.error('error: not enough funds to cover amount 100000'); process.exit(1);
  }
  console.log(JSON.stringify({ txid: 'accepted' }));
} else if (command === 'receive') {
  console.log(JSON.stringify({ offchain_address: 'tark1faucet' }));
} else if (command === 'note') {
  console.log('arknote123');
} else if (command === 'refill') {
  const request = JSON.parse(fs.readFileSync(0, 'utf8'));
  if (request.note !== 'arknote123' || request.address !== 'tark1faucet') process.exit(2);
  if (scenario === 'refill_failed') { console.error('batch unavailable'); process.exit(1); }
  console.log('Faucet replenished');
} else process.exit(3);
`;
  for (const command of ['docker', 'go']) writeFileSync(join(dir, command), fixture, { mode: 0o755 });
  const result = spawnSync(process.execPath, [
    '--import', pathToFileURL(preload).href,
    fileURLToPath(new URL('./fund-regtest.mjs', import.meta.url)), 'tark1recipient', '100000',
  ], {
    env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, FAUCET_TEST_LOG: log, FAUCET_TEST_SCENARIO: scenario },
    encoding: 'utf8', timeout: 10000,
  });
  return { ...result, calls: readFileSync(log, 'utf8').trim().split('\n') };
}

test('funded faucet sends once', t => {
  const result = run(t, 'funded');
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(result.calls, ['send']);
});

test('empty faucet replenishes and retries once', t => {
  const result = run(t, 'empty');
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(result.calls, ['send', 'receive', 'note', 'refill', 'send']);
  assert.match(result.stdout, /accepted/);
});

test('ambiguous send failure never retries', t => {
  const result = run(t, 'ambiguous');
  assert.equal(result.status, 1);
  assert.deepEqual(result.calls, ['send']);
  assert.match(result.stderr, /finalization timed out/);
});

test('failed replenishment stops before another send', t => {
  const result = run(t, 'refill_failed');
  assert.equal(result.status, 1);
  assert.deepEqual(result.calls, ['send', 'receive', 'note', 'refill']);
  assert.match(result.stderr, /batch unavailable/);
});
