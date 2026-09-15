// Solver bring-up. solverd initialises its wallet from SOLVER_WALLET_SEED on
// boot, so this waits for its HTTP listener, then bootstraps it (fund + mint
// asset + register pairs) the way the solver repo's `make init-solverd` does.
import { env } from '../env.mjs';
import { log, warn } from '../log.mjs';
import { waitForOrFail, fetchText } from '../wait.mjs';
import { dockerExec } from '../proc.mjs';
import { composeRun, ALL_PROFILES } from '../compose.mjs';

export async function setupSolver() {
  const port = env('SOLVER_HTTP_PORT', '7091');
  await waitForOrFail(
    'solver HTTP API',
    async () => {
      const { ok, status } = await fetchText(`http://localhost:${port}/`, { timeoutMs: 5000 });
      return ok || status > 0;
    },
    { attempts: 45, intervalMs: 2000 },
  );
  log(`Solver up at http://localhost:${port}`);

  await initSolver();
}

async function initSolver() {
  const btc = BigInt(env('SOLVER_INIT_BTC', '1000000'));
  const supply = BigInt(env('SOLVER_INIT_ASSET_SUPPLY', '100000'));
  // Must match noteAmount() in docker/solver-init/index.mjs.
  const noteAmount = btc + supply + 1_000_000n;

  log('Funding solver init wallet with a credit note...');
  const note = dockerExec('arkd', ['arkd', 'note', '--amount', String(noteAmount)], { capture: true })
    .stdout.trim();
  if (!note) {
    warn('Solver init skipped: failed to create credit note');
    return;
  }

  log('Bootstrapping solver (fund + mint asset + register pairs)...');
  // depends_on chains reach into the ark/base profiles, so the whole project
  // must be enabled for `compose run` to consider it valid.
  const res = composeRun('solver-init', {
    profiles: ALL_PROFILES,
    env: { INIT_NOTE: note },
    build: true,
  });
  if (res.code !== 0) warn('Solver init failed; solver is up but not funded/registered');
  else log('Solver initialized');
}
