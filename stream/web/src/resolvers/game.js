import { makePlayable, RENDER } from '../source.js';
import { coreFromExtension } from '../rom-core.js';
import { PlaybackError, FAILURE } from '../failures.js';

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
export function bootEmulator(el, { gameUrl, core, name, biosUrl = null, coreOptions = null, doc = document }) {
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

  // Core options, which for two machines ARE the firmware.
  //
  // libretro-uae compiles the AROS Kickstart replacement into itself and
  // `puae_kickstart = "aros"` selects it; pcsx_rearmed carries its own BIOS
  // emulation and `pcsx_rearmed_bios = "HLE"` selects that. Neither is a file,
  // and without the option the core looks for firmware that is not there and
  // reports it by refusing to boot -- so this is not a preference, it is the
  // difference between a game and a black screen.
  //
  // Cleared rather than left alone for the same reason EJS_biosUrl is: these
  // are GLOBALS, and options set for an Amiga would otherwise be handed to the
  // next game, where an unknown key is written into another core's config.
  globalThis.EJS_defaultOptions = coreOptions && Object.keys(coreOptions).length
    ? { ...coreOptions }
    : undefined;

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
 * Fetch a ROM from the first source that yields the whole thing.
 *
 * WHY THE PLAYER FETCHES THIS ITSELF instead of handing EmulatorJS a URL.
 *
 * EmulatorJS names the file it writes into the core's filesystem after the URL
 * it downloaded -- `gameUrl.split("/").pop().split("?")[0]` -- and hands the
 * core that path. Several libretro cores read the extension to decide WHICH
 * MACHINE they are emulating. Through our relay the URL is `/bridge/iptv?u=…`,
 * so every ROM on the site arrived called `iptv`, with no extension at all; and
 * archive.org's own name is no better for the machine that matters, because it
 * calls both Game Gear and Mega Drive payloads `.bin`.
 *
 * Measured: genesis_plus_gx given a Game Gear ROM as `iptv` or `.bin` emulates
 * a MASTER SYSTEM -- 256x192 instead of the Game Gear's 160x144 -- and draws a
 * black screen while reporting itself started and running at 60 frames a
 * second. Given the same bytes as `.gg` it plays.
 *
 * EmulatorJS uses `EJS_gameName` as the filename when the URL is a `blob:` one,
 * which is the documented way to say what the file is called. So the bytes are
 * fetched here, wrapped in a blob, and the name comes from the server -- which
 * is where the core table already lives, and so where the answer to "what must
 * this be called" belongs.
 *
 * Sources are tried in order and the ANSWER IS CHECKED, not assumed: an
 * archive.org storage node that answers 5xx returns a short HTML body, and a
 * WASM emulator handed 170 bytes of HTML boots and runs nothing. Measured, that
 * happens to roughly one request in a hundred and never twice to the same item,
 * so trying the next source turns a dead game into a slower one.
 */
export async function fetchRom(sources, { expectBytes = 0, fetchImpl = fetch } = {}) {
  const tried = [];
  for (const src of sources) {
    if (!src) continue;
    try {
      const response = await fetchImpl(src);
      if (!response.ok) {
        tried.push(`${src} → HTTP ${response.status}`);
        continue;
      }
      const buf = await response.arrayBuffer();
      // A size the server told us is a size worth checking. A truncated or
      // error-page body is the failure that boots successfully and plays
      // nothing, which is the one this whole path exists to stop.
      if (expectBytes && buf.byteLength !== expectBytes) {
        tried.push(`${src} → ${buf.byteLength} bytes, expected ${expectBytes}`);
        continue;
      }
      if (buf.byteLength === 0) {
        tried.push(`${src} → empty`);
        continue;
      }
      return buf;
    } catch (error) {
      tried.push(`${src} → ${error?.message ?? error}`);
    }
  }
  throw new PlaybackError(
    FAILURE.DEAD_STREAM,
    `the ROM could not be fetched (${tried.join('; ') || 'no sources offered'})`,
  );
}

/**
 * Boot EmulatorJS into `el` against any URL -- an http(s) ROM or a blob: URL
 * from a completed torrent file. Returns a handle whose destroy() stops it.
 *
 * `sources`, when given, is an ordered list of places to fetch the ROM from;
 * the bytes are collected here and handed to EmulatorJS as a blob so that
 * `name` -- not the URL -- decides what the core sees. See fetchRom. Without
 * it, `url` is passed straight through exactly as before, which is what the
 * torrent path does with a blob: URL it made itself.
 */
export function mountEmulator(el, url, {
  core, name, biosUrl = null, coreOptions = null, doc = document,
  sources = null, expectBytes = 0, label = null, fetchImpl = undefined,
}) {
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
  button.textContent = `▶  Play ${label ?? name}`;
  el.replaceChildren(button);

  const state = { tag: null, booted: false, onResize: null, blob: null };

  button.addEventListener('click', async () => {
    if (state.booted) return;
    state.booted = true;

    const host = doc.createElement('div');
    host.style.width = '100%';
    host.style.height = '100%';
    el.replaceChildren(host);

    // The ROM is collected here, before the emulator exists, so that the name
    // the core sees is ours to set rather than whatever the URL happened to end
    // in. A failure has to be said out loud: booting an emulator with no ROM
    // produces a black screen that reports itself as running, which is the
    // failure mode this whole path was written to remove.
    let gameUrl = url;
    if (sources) {
      host.textContent = 'Fetching the game…';
      try {
        const bytes = await fetchRom(sources, { expectBytes, ...(fetchImpl ? { fetchImpl } : {}) });
        state.blob = URL.createObjectURL(new Blob([bytes], { type: 'application/octet-stream' }));
        gameUrl = state.blob;
      } catch (error) {
        host.textContent = error?.message
          ? `This game could not be started — ${error.message}`
          : 'This game could not be started.';
        return;
      }
      host.replaceChildren();
    }

    state.tag = bootEmulator(host, { gameUrl, core, name, biosUrl, coreOptions, doc });

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
      // The blob holds the whole ROM; without this it outlives the game that
      // needed it and the tab keeps paying for every title anybody opened.
      if (state.blob) {
        try {
          URL.revokeObjectURL(state.blob);
        } catch {
          /* no URL registry in this environment */
        }
        state.blob = null;
      }
      state.onResize = null;
      state.tag = null;
    },
  };
}

/**
 * A core and a name declared ON the URI, as `#ejs=<core>&name=<title>`.
 *
 * A catalogue that KNOWS the system must be able to say so, because the
 * alternative here is inference from a file extension and that is the one
 * thing rom-core.js exists to avoid. The Vimm vault serves its ROMs from
 * `/api/rom/<vault id>` -- no extension to read at all, and a last path
 * segment that would name the game "3" -- while knowing the exact core,
 * because vimm.net's own player declares it and the vault checks it against
 * the EmulatorJS release it serves.
 *
 * Returns nulls for a URI that declares nothing, which is every other ROM URL.
 */
export function declared(uri) {
  try {
    const hash = new URL(uri).hash;
    if (!hash) return { core: null, name: null };
    const params = new URLSearchParams(hash.slice(1));
    return { core: params.get('ejs') || null, name: params.get('name') || null };
  } catch {
    return { core: null, name: null };
  }
}

export const gameResolver = {
  name: 'game',
  canHandle(input) {
    if (typeof input !== 'string' || !/^https?:\/\//i.test(input)) return false;
    return Boolean(declared(input).core) || isRom(input);
  },
  async resolve(source) {
    const url = new URL(source.uri);
    const path = url.pathname;
    const said = declared(source.uri);
    // A declared core always wins. It came from a catalogue that knows the
    // machine; the extension is at best a good guess about it.
    const core = said.core ?? coreFor(path);
    const name = said.name ?? decodeURIComponent(path.split('/').pop() || 'game');
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
