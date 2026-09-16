import { BoobaTerminal } from '@nimblemarkets/booba';

declare const POKER_WASM_PATH: string;
declare const POKER_BUILD_CONFIG: PokerConfig;

interface PokerConfig {
  arkd: string;
  emulator: string;
  delegator: string;
  indexer: string;
  relay: string;
  terms: { Stake: number; Bond: number; MinBet: number; MaxWager: number };
  storage?: string;
}

declare global {
  interface Window {
    Go: new () => { importObject: WebAssembly.Imports; run(instance: WebAssembly.Instance): Promise<void> };
    bubbletea_write?: (data: string) => void;
    pokerConfig?: PokerConfig;
  }
}

const loading = document.querySelector<HTMLDivElement>('#loading')!;

async function start() {
  window.pokerConfig = POKER_BUILD_CONFIG;
  const terminal = new BoobaTerminal('terminal', {
    fontSize: 14, fontFamily: '"SFMono-Regular", Menlo, Consolas, monospace', cursorBlink: true, scrollback: 0,
    allowOSC52: true,
    theme: { background: '#000000', foreground: '#7cff00', cursor: '#7cff00' },
  });
  await terminal.init();
  // Preserve macOS browser shortcuts, including Cmd+C/V and Ctrl+C. Do not
  // preventDefault: the browser keeps handling these. Paste arrives through the
  // terminal's paste event and Bubble Tea's bracketed-paste message.
  document.addEventListener('keydown', event => {
    if (event.metaKey || (event.ctrlKey && event.key.toLowerCase() === 'c')) event.stopImmediatePropagation();
  }, { capture: true });
  const go = new window.Go();
  const response = await fetch(POKER_WASM_PATH);
  if (!response.ok) throw new Error(`WASM download failed (${response.status})`);
  const { instance } = await WebAssembly.instantiate(await response.arrayBuffer(), go.importObject);
  const running = go.run(instance);
  // go.run starts the application synchronously up to its first JS yield. The
  // bridge is installed before any asynchronous application startup commands.
  if (!window.bubbletea_write) throw new Error('WASM terminal bridge did not initialize');
  terminal.connectWasm();
  loading.hidden = true;
  terminal.term?.focus();
  await running;
  terminal.disconnect();
}

start().catch(() => {
  // Runtime errors must not accidentally log transient input or wallet objects.
  console.error('Poker could not start or its runtime stopped.');
  loading.textContent = 'Poker could not start. Reload the page to try again.';
  loading.hidden = false;
});
