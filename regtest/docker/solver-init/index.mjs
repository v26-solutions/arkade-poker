#!/usr/bin/env node
// arkade-regtest equivalent of the solver repo's `make init-solverd`: funds
// solverd with BTC and a minted asset, then registers its market.
import { EventSource } from 'eventsource';
import {
  Wallet,
  SingleKey,
  ArkNote,
  InMemoryWalletRepository,
  InMemoryContractRepository,
} from '@arkade-os/sdk';

// ts-sdk streams arkd round events over SSE; Node has no global EventSource.
globalThis.EventSource ??= EventSource;

const cfg = {
  arkServerUrl: env('ARK_SERVER_URL', 'http://arkd:7070'),
  esploraUrl: env('ESPLORA_URL', 'http://mempool_web/api'),
  solverHttpUrl: env('SOLVER_HTTP_URL', 'http://solver:7171'),
  pricefeedBase: env('PRICEFEED_BASE', 'http://pricefeed'),
  note: env('INIT_NOTE', ''),
  btcFunding: BigInt(env('SOLVER_INIT_BTC', '1000000')),
  assetSupply: BigInt(env('SOLVER_INIT_ASSET_SUPPLY', '100000')),
  assetFunding: BigInt(env('SOLVER_INIT_ASSET_FUNDING', '50000')),
};

function env(key, fallback) {
  const v = process.env[key];
  return v !== undefined && v !== '' ? v : fallback;
}

const log = (...a) => console.log('[solver-init]', ...a);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function main() {
  if (!cfg.note) throw new Error('INIT_NOTE is required');

  await waitSolverReady();
  const solverAddr = await solverAddress();
  log('solver offchain address:', solverAddr);

  const wallet = await Wallet.create({
    identity: SingleKey.fromRandomBytes(),
    arkServerUrl: cfg.arkServerUrl,
    esploraUrl: cfg.esploraUrl,
    // Node has no IndexedDB; keep wallet state in memory (throwaway wallet).
    storage: {
      walletRepository: new InMemoryWalletRepository(),
      contractRepository: new InMemoryContractRepository(),
    },
  });
  const funderAddr = await wallet.getAddress();

  log('redeeming credit note...');
  await wallet.settle({
    inputs: [ArkNote.fromString(cfg.note)],
    outputs: [{ address: funderAddr, amount: noteAmount() }],
  });
  await waitBalance(wallet, (b) => BigInt(b.available) >= cfg.btcFunding, 'funder BTC');

  // BTC first: a send picks sat carriers from the whole VTXO set, so minting
  // before this would let it spend the fresh asset VTXO out from under us.
  log(`funding solver with ${cfg.btcFunding} sats...`);
  await wallet.send({ address: solverAddr, amount: Number(cfg.btcFunding) });
  await pollSolverBalance(
    (b) => BigInt(b.offchainSettled ?? 0) >= (cfg.btcFunding * 9n) / 10n,
    'solver BTC',
  );

  // solverd's market validation resolves "decimals" metadata via the indexer, so
  // it must be set at issuance.
  log(`minting asset (supply ${cfg.assetSupply})...`);
  const { assetId } = await wallet.assetManager.issue({
    amount: cfg.assetSupply,
    metadata: { decimals: 0, name: 'Regtest Asset', ticker: 'RGT' },
  });
  log('minted asset:', assetId);
  await waitBalance(wallet, (b) => assetBalance(b, assetId) >= cfg.assetSupply, 'funder asset');

  log(`funding solver with ${cfg.assetFunding} units of asset...`);
  await wallet.send({
    address: solverAddr,
    amount: Number(cfg.assetFunding), // BTC carrier for the asset VTXO
    assets: [{ assetId, amount: cfg.assetFunding }],
  });
  await pollSolverBalance(
    (b) => BigInt(b.assetBalances?.[assetId] ?? 0) >= cfg.assetFunding,
    'solver asset',
  );

  await addMarket('BTC', assetId, `${cfg.pricefeedBase}/btc-asset`, '/btc/asset');

  log('done: solver funded, asset minted, market registered');
  await wallet.dispose().catch(() => {});
}

// Covers the BTC sent to the solver plus the sat carriers backing the asset,
// with headroom for fees.
function noteAmount() {
  return cfg.btcFunding + cfg.assetSupply + 1_000_000n;
}

function assetBalance(balance, assetId) {
  const found = (balance.assets ?? []).find((a) => a.assetId === assetId);
  return found ? BigInt(found.amount) : 0n;
}

async function waitBalance(wallet, pred, label, timeoutMs = 90_000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const b = await wallet.getBalance().catch(() => null);
    if (b && pred(b)) return;
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${label}`);
    await sleep(2000);
  }
}

async function waitSolverReady(timeoutMs = 120_000) {
  log('waiting for solverd...');
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const s = await getJson(`${cfg.solverHttpUrl}/v1/status`).catch(() => null);
    if (s && s.running) return;
    if (Date.now() > deadline) throw new Error('timed out waiting for solverd readiness');
    await sleep(2000);
  }
}

async function solverAddress() {
  const r = await getJson(`${cfg.solverHttpUrl}/v1/address`);
  const addr = r.offchainAddress ?? r.offchain_address;
  if (!addr) throw new Error('solverd returned empty offchain address');
  return addr;
}

async function pollSolverBalance(pred, label, timeoutMs = 90_000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const b = await getJson(`${cfg.solverHttpUrl}/v1/balance`).catch(() => null);
    if (b && pred(normalizeBalance(b))) return;
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${label}`);
    await sleep(2000);
  }
}

function normalizeBalance(b) {
  return {
    offchainSettled: b.offchainSettled ?? b.offchain_settled ?? 0,
    assetBalances: b.assetBalances ?? b.asset_balances ?? {},
  };
}

// The feed returns quote-per-base; both directions are enabled by giving each
// its own want-side bounds (a zero max would disable that side). price_path is
// the JSON pointer to the price in the feed response — required since solver
// v0.0.3 for anything that isn't a Binance/CoinGecko URL.
async function addMarket(baseAsset, quoteAsset, priceFeed, pricePath) {
  await postJson(`${cfg.solverHttpUrl}/v1/market`, {
    market: {
      base_asset: baseAsset,
      quote_asset: quoteAsset,
      min_quote_amount: 1,
      max_quote_amount: 100_000_000,
      min_base_amount: 1,
      max_base_amount: 100_000_000,
      price_feed: priceFeed,
      price_path: pricePath,
    },
  });
  log(`added market ${baseAsset}/${quoteAsset}`);
}

async function getJson(url) {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`GET ${url} -> HTTP ${res.status}`);
  return res.json();
}

async function postJson(url, body) {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(`POST ${url} -> HTTP ${res.status} ${text}`);
  }
  return res.json().catch(() => ({}));
}

main().catch((err) => {
  console.error('[solver-init] failed:', err?.message || err);
  process.exit(1);
});
