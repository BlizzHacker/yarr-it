import { makePlayable, RENDER } from '../source.js';
import { coreFromExtension } from '../rom-core.js';

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

/**
 * Which core a ROM needs is its own problem, and a harder one than it looks --
 * see rom-core.js. This resolver only handles a bare http(s) URL, where the
 * name is all there is to go on; the torrent path reads the ROM's header
 * instead, which is far more reliable.
 */
export function coreFor(name = '') {
  return coreFromExtension(name);
}

export function isRom(input) {
  try {
    return coreFromExtension(new URL(input).pathname) !== null;
  } catch {
    return false;
  }
}

/**
 * Inject the EmulatorJS loader. Exported so tests can drive it without a DOM.
 */
export function bootEmulator(el, { gameUrl, core, name, biosUrl = null, doc = document }) {
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

  // Firmware the viewer supplied, as a blob: URL from their own storage --
  // ColecoVision, PlayStation and Amiga will not boot without it. Cleared
  // rather than left alone when there is none: these are GLOBALS, so a BIOS set
  // for the previous game would otherwise be handed to the next one, and a
  // Kickstart ROM fed to an NES core is a black screen with no error.
  globalThis.EJS_biosUrl = biosUrl || '';

  // SharedArrayBuffer only exists on a cross-origin-isolated page, and asking
  // EmulatorJS for a threaded core without it loads a core that throws on
  // construction. Reading the platform's own answer means this is right whether
  // or not the isolation headers are deployed.
  globalThis.EJS_threads = globalThis.crossOriginIsolated === true;

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
export function mountEmulator(el, url, { core, name, biosUrl = null, doc = document }) {
  // A WASM emulator needs a REAL user gesture before the browser will let it
  // run: it opens an AudioContext, and autoplay policy holds the whole run loop
  // until the page has been interacted with. Relying on EJS_startOnLoaded alone
  // leaves the core loaded, the ROM written into its filesystem, `started` true
  // -- and the frame counter pinned at 0 with a black canvas and no error.
  //
  // So the boot is deliberately hung off a click. It is also better UX: a game
  // that starts making noise on page load is not what anyone wants.
  const button = doc.createElement('button');
  button.className = 'canvas-start';
  button.type = 'button';
  button.textContent = `▶  Play ${name}`;
  el.replaceChildren(button);

  const state = { tag: null, booted: false, onResize: null };

  button.addEventListener('click', () => {
    if (state.booted) return;
    state.booted = true;

    const host = doc.createElement('div');
    host.style.width = '100%';
    host.style.height = '100%';
    el.replaceChildren(host);
    state.tag = bootEmulator(host, { gameUrl: url, core, name, biosUrl, doc });

    // EmulatorJS lays its canvas out once and does not always catch a viewport
    // change: measured, a game booted at 1280x720 and then resized to 375x667
    // kept drawing at a fraction of the new canvas, centred in black. On a phone
    // that change is called turning the handset sideways, so it is worth a
    // nudge. Guarded on every hop -- a missing method must cost the nudge, not
    // the game.
    if (typeof globalThis.addEventListener === 'function') {
      state.onResize = () => {
        try {
          globalThis.EJS_emulator?.handleResize?.();
        } catch {
          /* the emulator is mid-teardown */
        }
      };
      globalThis.addEventListener('resize', state.onResize);
    }
  }, { once: true });

  return {
    /** Exposed so tests can drive the boot without a synthetic-click caveat. */
    _button: button,
    destroy() {
      try {
        state.tag?.remove();
        globalThis.EJS_emulator?.pause?.();
        if (state.onResize) globalThis.removeEventListener?.('resize', state.onResize);
      } catch {
        /* nothing running */
      }
      state.onResize = null;
      state.tag = null;
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
