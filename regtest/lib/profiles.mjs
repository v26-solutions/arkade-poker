import { fail } from './log.mjs';

export const PROFILE_DEPS = {
  base: [],
  ark: ['base'],
  delegate: ['ark'],
  lightning: ['ark'],
  emulator: ['ark'],
  covclaimd: ['ark', 'emulator'],
  solver: ['ark', 'emulator'],
  'intent-solver': ['ark', 'emulator', 'lightning', 'nostr'],
  sync: ['base'],
  nostr: ['base'],
  'evm-e2e': ['ark', 'emulator'],
};

export const DEFAULT_PROFILES = Object.keys(PROFILE_DEPS).filter((profile) => profile !== 'evm-e2e');

export function resolveProfiles(requested) {
  const out = new Set();
  const visit = (profile) => {
    if (out.has(profile)) return;
    if (!(profile in PROFILE_DEPS)) {
      fail(`unknown profile "${profile}" (valid: ${Object.keys(PROFILE_DEPS).join(', ')})`);
    }
    out.add(profile);
    PROFILE_DEPS[profile].forEach(visit);
  };
  requested.forEach(visit);
  return [...out];
}
