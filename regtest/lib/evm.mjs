const XONLY_GENERATOR = '79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798';

export async function evmRpc(url, method, params = []) {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }),
  });
  const body = await response.json();
  if (!response.ok || body.error) throw new Error(`${method}: ${JSON.stringify(body.error || body)}`);
  return body.result;
}

export function sendRfqRequest({ token, arkAddress, evmAddress, paymentHash, rfqId }) {
  return {
    v: 1,
    type: 'rfq_request',
    rfq_id: rfqId,
    pair: `arkade:BTC->ethereum:${token}`,
    amount_side: 'from',
    amount: 100_000,
    profile: {
      payment_hash: paymentHash,
      evm_claim_address: evmAddress,
      refund_address: arkAddress,
      client_refund_pubkey: XONLY_GENERATOR,
    },
  };
}

export function receiveRfqRequest({ token, arkAddress, evmAddress, paymentHash, rfqId, timeoutBlock, evmAmount }) {
  return {
    v: 1,
    type: 'rfq_request',
    rfq_id: rfqId,
    pair: `ethereum:${token}->arkade:BTC`,
    amount_side: 'from',
    profile: {
      payment_hash: paymentHash,
      evm_amount: evmAmount,
      evm_timeout_block: timeoutBlock,
      evm_refund_address: evmAddress,
      payout_address: arkAddress,
      payout_pubkey: XONLY_GENERATOR,
    },
  };
}
