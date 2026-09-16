// Run after `make web`: exercise the actual WASM app with isolated browser API
// fixtures. No network, wallet, browser profile or system clipboard is used.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import { webcrypto } from 'node:crypto';

const secret = 'ab'.repeat(32);
const phrase = 'abandon '.repeat(11) + 'about';
const consoleLines = [];
const copies = [];
const timers = new Set();
let denyCopy = false;
let terminal = '';
let runtimeError;
const context = vm.createContext({
  WebAssembly, TextEncoder, TextDecoder, performance, crypto: webcrypto,
  Headers, AbortController, Uint8Array, ArrayBuffer,
  setTimeout(fn, ms) {
    const timer = setTimeout(() => { timers.delete(timer); fn(); }, ms);
    timers.add(timer);
    return timer;
  },
  clearTimeout(timer) { timers.delete(timer); clearTimeout(timer); },
  console: { log(line) { assert.equal(typeof line, 'string'); consoleLines.push(line); } },
  fetch: async () => { throw new Error(`fixture connection failure ${secret} ${phrase}`); },
  navigator: { clipboard: {
    writeText: async text => {
      if (denyCopy) throw new Error('fixture permission denied');
      copies.push(text);
    },
  } },
});

async function until(check, description) {
  const deadline = Date.now() + 5000;
  do {
    if (runtimeError) throw runtimeError;
    if (context.bubbletea_read) terminal += context.bubbletea_read();
    if (check()) return;
    await new Promise(resolve => setTimeout(resolve, 10));
  } while (Date.now() < deadline);
  throw new Error(`Timed out: ${description}`);
}

try {
  vm.runInContext(await readFile(new URL('../build/web/wasm_exec.js', import.meta.url), 'utf8'), context);
  const go = new context.Go();
  const { instance } = await WebAssembly.instantiate(await readFile(new URL('../build/web/poker.wasm', import.meta.url)), go.importObject);
  go.run(instance).catch(error => { runtimeError = error; });
  await until(() => context.bubbletea_write, 'terminal bridge');
  context.bubbletea_resize(100, 35);
  await until(() => consoleLines.some(line => line.includes('Network discovery failed')), 'redacted network error');
  await until(() => terminal.includes('[L] COPY LOGS'), 'shortcut hint');

  context.bubbletea_write('l');
  await until(() => copies.length === 1, 'lowercase copy');
  await until(() => terminal.includes('Logs copied'), 'copy feedback');
  assert.match(copies[0], /Application starting/);
  assert.match(copies[0], /Network discovery failed/);
  assert.match(copies[0], /\[REDACTED\]/);
  assert.ok(consoleLines.every(line => copies[0].includes(line)), 'console records differ from clipboard');

  context.bubbletea_write('a');
  await until(() => terminal.includes('IMPORT WALLET'), 'wallet form');
  context.bubbletea_write('lL');
  // A redraw proves both input events have been processed, without copying.
  terminal = '';
  await until(() => terminal.includes('••'), 'both letters entered as password text');
  assert.equal(copies.length, 1, 'text entry copied logs');
  context.bubbletea_write('\x1b');
  terminal = '';
  await until(() => terminal.includes('[A] ADD WALLET'), 'wallet form closed');

  context.bubbletea_write('L');
  await until(() => copies.length === 2, 'uppercase copy');
  denyCopy = true;
  context.bubbletea_write('l');
  terminal = '';
  await until(() => terminal.includes('Could not copy logs'), 'clipboard denial feedback');
  assert.ok(consoleLines.some(line => line.includes('Log clipboard copy failed')));
  denyCopy = false;
  context.bubbletea_write('l');
  await until(() => copies.length === 3, 'retry after denial');

  for (const output of [...consoleLines, ...copies]) {
    assert.ok(!output.includes(secret), 'private key leaked');
    assert.ok(!output.includes('abandon'), 'mnemonic leaked');
  }
  console.log('PASS: WASM clipboard, console, secret redaction, text entry and copy retry');
} finally {
  for (const timer of timers) clearTimeout(timer);
}
