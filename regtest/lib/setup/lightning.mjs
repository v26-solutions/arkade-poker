// Lightning bring-up: fund `lnd-peer`, open a channel to the base `lnd`, and
// push liquidity back so the channel carries payments in both directions.
import { env } from '../env.mjs'
import { log, fail } from '../log.mjs'
import { sleep, waitForOrFail } from '../wait.mjs'
import { faucet, mine } from '../chain.mjs'
import { dockerExec } from '../proc.mjs'
import { lncli, lncliBase } from '../lnd.mjs'

const PEER = 'lnd-peer'

function channelCount() {
  const { json } = lncli(PEER, ['listchannels'])
  return json && Array.isArray(json.channels) ? json.channels.length : 0
}

export async function setupLightning() {
  if (channelCount() > 0) {
    log('LND channel already open, skipping setup...')
    return
  }

  log('Setting up LND for Lightning swaps...')
  await waitForOrFail(`${PEER} wallet`, () => lncli(PEER, ['getinfo']).ok)

  const addr = lncli(PEER, ['newaddress', 'p2wkh']).json?.address
  if (!addr) fail(`Could not get ${PEER} address`)
  log(`Funding ${PEER} at ${addr}...`)
  faucet(addr, env('LND_FAUCET_AMOUNT', '2'), { confirm: true })

  // lnd credits the wallet only after it has processed the faucet's block.
  let bal = 0
  await waitForOrFail(`${PEER} funding (>= 1,000,000 confirmed sats)`, () => {
    bal = Number(lncli(PEER, ['walletbalance']).json?.account_balance?.default?.confirmed_balance ?? '0')
    return bal >= 1000000
  })
  log(`${PEER} balance: ${bal}`)

  await waitForOrFail('counterparty lnd', () => lncli('lnd', ['getinfo']).ok)
  const counterparty = lncli('lnd', ['getinfo']).json?.identity_pubkey
  if (!counterparty) fail('Could not get counterparty lnd pubkey')
  log(`Opening channel to counterparty (${counterparty})...`)
  dockerExec(PEER, [
    ...lncliBase(PEER),
    'openchannel',
    '--node_key',
    counterparty,
    '--connect',
    'lnd:9735',
    '--local_amt',
    env('LND_CHANNEL_SIZE', '1000000'),
    '--sat_per_vbyte',
    '1',
    '--min_confs',
    '0',
  ])

  log('Mining 10 blocks to confirm channel...')
  mine(10)
  await sleep(10000)

  // The opener holds the whole balance, so without this the base `lnd` can send
  // and never receive — the direction a `lightning:BTC -> arkade:BTC` quote needs.
  log('Balancing channel via a test invoice...')
  const invoice = lncli('lnd', ['addinvoice', '--amt', '500000']).json?.payment_request
  if (invoice) dockerExec(PEER, [...lncliBase(PEER), 'payinvoice', '--force', invoice])
  log('LND channel setup completed')
}
