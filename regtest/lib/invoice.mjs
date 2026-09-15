// Lightning invoice helpers — ports of helpers/create-invoice.sh and
// helpers/pay-invoice.sh into the cross-platform CLI.
import { log, fail } from './log.mjs';
import { lncli } from './lnd.mjs';

// lncli that aborts on failure and returns the parsed JSON (or raw stdout).
function lncliJson(container, args) {
  const r = lncli(container, args);
  if (!r.ok) fail(`lncli ${args.join(' ')} on ${container} failed: ${r.err}`);
  return r.json ?? r.raw;
}

// Create a 100k-sat invoice on lnd-peer (primary) or lnd (--secondary).
// Prints the bare payment request to stdout so it can be piped/captured.
export function createInvoice({ secondary = false } = {}) {
  const container = secondary ? 'lnd' : 'lnd-peer';
  log(`Creating invoice on ${secondary ? 'secondary (lnd)' : 'primary (lnd-peer)'} ...`);
  const { payment_request: invoice } = lncliJson(container, ['addinvoice', '--amt', '100000']);
  log('Invoice created');
  console.log(invoice);
  return invoice;
}

// Pay an invoice from whichever node is NOT its destination.
export function payInvoice(invoice) {
  if (!invoice) fail('usage: node regtest.mjs pay-invoice <invoice>');
  const dest = lncliJson('lnd-peer', ['decodepayreq', invoice]).destination;
  const primary = lncliJson('lnd-peer', ['getinfo']).identity_pubkey;
  const secondary = lncliJson('lnd', ['getinfo']).identity_pubkey;

  if (dest === primary) {
    log('Paying invoice from secondary (lnd) -> primary (lnd-peer)...');
    lncliJson('lnd', ['payinvoice', '--force', invoice]);
  } else if (dest === secondary) {
    log('Paying invoice from primary (lnd-peer) -> secondary (lnd)...');
    lncliJson('lnd-peer', ['payinvoice', '--force', invoice]);
  } else {
    fail('Invoice destination matches neither lnd-peer nor lnd');
  }
}
