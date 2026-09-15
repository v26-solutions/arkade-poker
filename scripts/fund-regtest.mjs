// External test funding only. No poker wallet key enters this process.
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { loadEnv, env } from '../regtest/lib/env.mjs';
import { dockerExec } from '../regtest/lib/proc.mjs';

const [address, amount] = process.argv.slice(2);
if (!/^tark1[0-9a-z]+$/.test(address || '') || !/^\d+$/.test(amount || '') ||
    Number(amount) < 1 || Number(amount) > 100000) {
  throw new Error('expected regtest Ark address and 1–100000 test satoshis');
}
loadEnv(resolve(fileURLToPath(new URL('../regtest/', import.meta.url))));
const port = Number(env('ARKD_PORT', '7070'));
const response = await fetch(`http://127.0.0.1:${port}/v1/info`, {
  signal: AbortSignal.timeout(10000),
});
if (!response.ok || (await response.json()).network !== 'regtest') {
  throw new Error('faucet must use the local regtest stack');
}
const send = () => dockerExec('arkd', ['ark', 'send', '--to', address, '--amount', amount,
  '--password', env('ARKD_PASSWORD', 'secret')], { capture: true });
let result = send();
// This explicit coin-selection failure occurs before submission. Do not retry
// other failures: a missing acknowledgement could hide an accepted payment.
if (result.code !== 0 && /^error: not enough funds to cover amount \d+$/.test(
  result.stderr || result.stdout,
)) {
  console.error('Faucet has insufficient spendable funds (coins may have expired); replenishing...');
  const receive = dockerExec('arkd', ['ark', 'receive'], { capture: true });
  if (receive.code !== 0) throw new Error(`faucet receive failed: ${receive.stderr || receive.stdout}`);
  const faucetAddress = JSON.parse(receive.stdout).offchain_address;
  if (!/^tark1[0-9a-z]+$/.test(faucetAddress || '')) throw new Error('invalid regtest faucet address');
  const minted = dockerExec('arkd', ['arkd', 'note', '--amount', '1000000'], { capture: true });
  if (minted.code !== 0) throw new Error(`faucet note creation failed: ${minted.stderr || minted.stdout}`);
  const note = minted.stdout.trim();
  if (!/^arknote[1-9A-HJ-NP-Za-km-z]+$/.test(note)) throw new Error('invalid faucet credit note');
  const refill = spawnSync('go', ['run', './scripts/replenish-regtest'], {
    cwd: fileURLToPath(new URL('../', import.meta.url)),
    input: JSON.stringify({ port, address: faucetAddress, note }),
    encoding: 'utf8', timeout: 180000,
  });
  if (refill.error || refill.status !== 0) {
    throw new Error(`faucet replenishment failed: ${refill.error?.message || refill.stderr || refill.stdout}`);
  }
  console.error(refill.stdout.trim());
  result = send();
}
if (result.code !== 0) {
  console.error((result.stderr || result.stdout).slice(0, 2048));
  process.exit(result.code);
}
console.log(result.stdout);
