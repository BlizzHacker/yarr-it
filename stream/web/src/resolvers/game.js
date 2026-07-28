import { makePlayable, RENDER } from '../source.js';

/**
 * Game ROMs via EmulatorJS, the way archive.org's web emulators work.
 *
 * LICENSING AND BANDWIDTH: EmulatorJS is GPL-3.0 while this repository is MIT,
 * and its full release is 289MB compressed because it carries every emulator
 * core. Both problems have the same answer: load it from the project's own CDN
 * rather than vendoring or re-hosting it. Nothing GPL is distributed here, and
 * a player downloads only the one core their game needs (a few MB) instead of
 * costing this project's relay -- whose monthly cap is shared with a mail
 * server -- anything at all.
 *
 * Self-hosters who want no third-party dependency can point `yarrit.emulatorjs`
 * in localStorage at their own copy of the release's `data/` directory.
 *
 * EmulatorJS is configured entirely through globals that must be set BEFORE its
 * loader script runs, which is why this resolver returns a `mount(el)`: the
 * container has to exist and the globals have to be in place before the loader
 * is injected.
 *
 * A ROM must be COMPLETE before the emulator can boot -- there is no streaming
 * a cartridge. That is fine for retro ROMs (kilobytes to a few megabytes) and
 * is why this is scoped to those systems rather than to disc images.
 */

const DEFAULT_DATA_PATH = 'https://cdn.emulatorjs.org/stable/data/';

/** Where EmulatorJS loads its cores from. Overridable for self-hosting. */
export function dataPath() {
  try {
    const custom = globalThis.localStorage?.getItem('yarrit.emulatorjs');
    if (custom) return custom.endsWith('/') ? custom : `${custom}/`;
  } catch {
    /* localStorage unavailable (private mode, embedded webview) */
  }
  return DEFAULT_DATA_PATH;
}

// Extension -> EmulatorJS core. Kept to cartridge-era systems whose ROMs are
// small enough to finish downloading before anyone loses patience.
const CORES = {
  nes: 'nes', fds: 'nes', unf: 'nes', unif: 'nes',
  smc: 'snes', sfc: 'snes', swc: 'snes', fig: 'snes',
  gb: 'gb', gbc: 'gb',
  gba: 'gba',
  n64: 'n64', z64: 'n64', v64: 'n64',
  md: 'segaMD', gen: 'segaMD', smd: 'segaMD',
  sms: 'segaMS', gg: 'segaGG',
  a26: 'atari2600', a78: 'atari7800',
  lnx: 'lynx',
  pce: 'pce',
  ws: 'ws', wsc: 'ws',
  ngp: 'ngp', ngc: 'ngp',
  vb: 'vb',
};

export function coreFor(name = '') {
  const ext = String(name).toLowerCase().split('.').pop();
  return CORES[ext] ?? null;
}

export function isRom(input) {
  try {
    return coreFor(new URL(input).pathname) !== null;
  } catch {
    return false;
  }
}

/**
 * Inject the EmulatorJS loader. Exported so tests can drive it without a DOM.
 */
export function bootEmulator(el, { gameUrl, core, name, doc = document }) {
  // EJS_player is a CSS SELECTOR STRING, not an element. Handing it the node
  // itself makes the loader run, fetch emulator.min.js and define its globals,
  // and then silently never construct the emulator -- an empty container with
  // no error anywhere. The element therefore needs an id to point at.
  if (!el.id) el.id = `ejs-${Math.random().toString(36).slice(2, 10)}`;

  // These globals ARE the configuration API -- they must be set before the
  // loader script executes, not after.
  globalThis.EJS_player = `#${el.id}`;
  globalThis.EJS_core = core;
  globalThis.EJS_gameUrl = gameUrl;
  globalThis.EJS_gameName = name;
  globalThis.EJS_pathtodata = dataPath();
  globalThis.EJS_startOnLoaded = true;

  const tag = doc.createElement('script');
  tag.src = `${dataPath()}loader.js`;
  tag.onerror = () => {
    el.textContent = 'Emulator failed to load.';
  };
  doc.body.append(tag);
  return tag;
}

/**
 * Boot EmulatorJS into `el` against any URL -- an http(s) ROM or a blob: URL
 * from a completed torrent file. Returns a handle whose destroy() stops it.
 */
export function mountEmulator(el, url, { core, name }) {
  // EmulatorJS replaces its host element's contents with its own UI, so give it
  // a child of its own rather than the shared container.
  const host = document.createElement('div');
  host.style.width = '100%';
  host.style.height = '100%';
  el.replaceChildren(host);

  const tag = bootEmulator(host, { gameUrl: url, core, name });

  return {
    destroy() {
      try {
        tag?.remove();
        globalThis.EJS_emulator?.pause?.();
      } catch {
        /* nothing running */
      }
    },
  };
}

export const gameResolver = {
  name: 'game',
  canHandle(input) {
    return typeof input === 'string' && /^https?:\/\//i.test(input) && isRom(input);
  },
  async resolve(source) {
    const path = new URL(source.uri).pathname;
    const core = coreFor(path);
    const name = decodeURIComponent(path.split('/').pop() || 'game');
    let handle = null;

    return makePlayable({
      render: RENDER.CANVAS,
      src: source.uri,
      mime: 'application/octet-stream',
      mount(el) {
        handle = mountEmulator(el, source.uri, { core, name });
      },
      cleanup() {
        handle?.destroy();
        handle = null;
      },
    });
  },
};
