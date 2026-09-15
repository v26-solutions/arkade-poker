import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { DEFAULT_PROFILES, resolveProfiles } from '../lib/profiles.mjs';
import { containerName } from '../lib/proc.mjs';
import { receiveRfqRequest, sendRfqRequest } from '../lib/evm.mjs';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');

test('evm-e2e stays opt-in while resolving its Ark dependencies', () => {
  assert.equal(DEFAULT_PROFILES.includes('evm-e2e'), false);
  assert.deepEqual(resolveProfiles(['evm-e2e']), ['evm-e2e', 'ark', 'base', 'emulator']);
});

test('container names stay backward compatible and can be stack-scoped', () => {
  assert.equal(containerName('bitcoin', {}), 'bitcoin');
  assert.equal(
    containerName('bitcoin', { REGTEST_CONTAINER_PREFIX: 'arkade-regtest-evm-e2e-' }),
    'arkade-regtest-evm-e2e-bitcoin',
  );
});

test('rendered EVM solvers keep quotes valid through checkout safety headroom', () => {
  const output = execFileSync(
    'docker',
    ['compose', '-f', join(root, 'docker', 'compose.evm.yml'), '--profile', 'evm-e2e', 'config', '--format', 'json'],
    { encoding: 'utf8' },
  );
  const services = JSON.parse(output).services;

  for (const name of ['intent-solver-evm-send', 'intent-solver-evm-receive']) {
    const environment = services[name].environment;
    const quoteValiditySeconds = Number(environment.EVM_QUOTE_VALIDITY_SECONDS);
    const requiredSeconds = Number(environment.EVM_FEE_HEADROOM_SECONDS) + 60;
    assert.ok(
      quoteValiditySeconds >= requiredSeconds,
      `${name} quote validity ${quoteValiditySeconds} must cover ${requiredSeconds} seconds`,
    );
  }
});

test('rendered EVM send quotes exceed the client recourse margin', () => {
  const output = execFileSync(
    'docker',
    ['compose', '-f', join(root, 'docker', 'compose.evm.yml'), '--profile', 'evm-e2e', 'config', '--format', 'json'],
    { encoding: 'utf8' },
  );
  const services = JSON.parse(output).services;
  const clientMarginSeconds = 7_200;
  const clockDriftHeadroomSeconds = 300;

  for (const name of ['intent-solver-evm-send', 'intent-solver-evm-receive']) {
    const quotedMarginSeconds = Number(services[name].environment.EVM_ORDER_MARGIN_SECONDS);
    assert.ok(
      quotedMarginSeconds >= clientMarginSeconds + clockDriftHeadroomSeconds,
      `${name} quote margin ${quotedMarginSeconds} must exceed the client margin by ${clockDriftHeadroomSeconds}s`,
    );
  }
});

test('rendered Ark VTXOs outlive three ingress refund horizons', () => {
  const output = execFileSync(
    'docker',
    [
      'compose',
      '--env-file', join(root, '.env.defaults'),
      '--env-file', join(root, '.env.evm-e2e'),
      '-f', join(root, 'docker', 'compose.base.yml'),
      '-f', join(root, 'docker', 'compose.ark.yml'),
      '-f', join(root, 'docker', 'compose.evm.yml'),
      '--profile', 'base',
      '--profile', 'ark',
      '--profile', 'emulator',
      '--profile', 'evm-e2e',
      'config',
      '--format', 'json',
    ],
    { encoding: 'utf8' },
  );
  const treeExpirySeconds = Number(JSON.parse(output).services.arkd.environment.ARKD_VTXO_TREE_EXPIRY);
  const ingressRefundHorizonSeconds = 7_200;

  assert.ok(
    treeExpirySeconds >= ingressRefundHorizonSeconds * 3,
    `Ark VTXO lifetime ${treeExpirySeconds} must cover three ${ingressRefundHorizonSeconds}s refund horizons`,
  );
});

test('RFQ readiness requests use each EVM direction exact wire shape', () => {
  const base = {
    token: '0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2',
    arkAddress: 'tark1probe',
    evmAddress: '0x3c44cddddb6a900fa2b585dd299e03d12fa4293bc',
    paymentHash: '11'.repeat(32),
    rfqId: '22'.repeat(32),
  };
  assert.deepEqual(sendRfqRequest(base), {
    v: 1,
    type: 'rfq_request',
    rfq_id: base.rfqId,
    pair: `arkade:BTC->ethereum:${base.token}`,
    amount_side: 'from',
    amount: 100_000,
    profile: {
      payment_hash: base.paymentHash,
      evm_claim_address: base.evmAddress,
      refund_address: base.arkAddress,
      client_refund_pubkey: '79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798',
    },
  });
  assert.deepEqual(receiveRfqRequest({ ...base, timeoutBlock: 900, evmAmount: '1000000000000000' }), {
    v: 1,
    type: 'rfq_request',
    rfq_id: base.rfqId,
    pair: `ethereum:${base.token}->arkade:BTC`,
    amount_side: 'from',
    profile: {
      payment_hash: base.paymentHash,
      evm_amount: '1000000000000000',
      evm_timeout_block: 900,
      evm_refund_address: base.evmAddress,
      payout_address: base.arkAddress,
      payout_pubkey: '79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798',
    },
  });
});
