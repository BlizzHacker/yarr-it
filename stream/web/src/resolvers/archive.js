import { makeSource, makeCollection, RENDER, makePlayable } from '../source.js';
import { PlaybackError, FAILURE } from '../failures.js';
import { playableFiles, kindOf } from '../pickfile.js';
import { mountEmulator } from './game.js';
import { mountRuffle } from './flash.js';
import { coreFor } from './game.js';

/**
 * archive.org items -- the Internet Archive's software and emulation
 * collections, which is where free, legal, instantly-playable games actually
 * live.
 *
 * Two facts shape this resolver, both established by testing rather than
 * assumed:
 *
 * 1. YOU CANNOT GUESS THE FILENAME. `archive.org/download/dk_coleco/dk_coleco.zip`
 *    is a 404; the ROM is `dk.bin`. Every item has to be read through
 *    /metadata/<id>, whose file list is mostly screenshots, XML and a .torrent
 *    with the ROM somewhere among them.
 *
 * 2. ARCHIVE.ORG SENDS NO CORS HEADERS. A <video src> does not care, but a WASM
 *    emulator fetches the ROM bytes itself and that fetch is blocked. So ROM
 *    URLs are routed through the relay, which adds the header. ROMs are
 *    16KB-4MB, so unlike video the bandwidth cost is negligible.
 */

/**
 * archive.org's own `emulator` field -> the EmulatorJS core that runs it.
 *
 * This mapping exists because guessing from the file extension is wrong and
 * silently so: `dk.bin` is a Colecovision ROM, but `.bin` is also Atari 2600,
 * Mega Drive and half a dozen others. Defaulting an unknown extension to the
 * NES core loads EmulatorJS successfully and then runs the wrong machine --
 * which looks like it worked until you notice the game never starts.
 *
 * Archive.org states the emulator outright, so use it and refuse when it names
 * something unsupported rather than picking a core at random.
 */
const IA_EMULATOR_TO_CORE = {
  nes: 'nes', famicom: 'nes',
  snes: 'snes',
  gb: 'gb', gbc: 'gb', gba: 'gba',
  n64: 'n64',
  genesis: 'segaMD', megadrive: 'segaMD', sega: 'segaMD', segacd: 'segaCD',
  sms: 'segaMS', mastersystem: 'segaMS', gamegear: 'segaGG',
  coleco: 'coleco', colecovision: 'coleco',
  atari2600: 'atari2600', a2600: 'atari2600',
  atari7800: 'atari7800', a7800: 'atari7800',
  lynx: 'lynx', jaguar: 'jaguar',
  vb: 'vb', virtualboy: 'vb',
  ws: 'ws', wonderswan: 'ws',
  ngp: 'ngp', neogeopocket: 'ngp',
  pce: 'pce', tg16: 'pce', turbografx: 'pce',
  intellivision: 'intv', intv: 'intv',
  arcade: 'arcade', mame: 'arcade',
};

/** The EmulatorJS core for an item, from archive.org's own metadata. */
export function coreFromMetadata(meta) {
  const declared = String(meta?.metadata?.emulator || '').toLowerCase().replace(/[^a-z0-9]/g, '');
  return IA_EMULATOR_TO_CORE[declared] || null;
}

const METADATA = 'https://archive.org/metadata/';
const DOWNLOAD = 'https://archive.org/download/';
const RELAY = '/bridge/iptv?u=';

/** Emulation-focused collections worth searching by default. */
export const COLLECTIONS = [
  'consolelivingroom',
  'softwarelibrary',
  'internetarcade',
  'softwarelibrary_msdos_games',
];

export function identifierFrom(input) {
  try {
    const url = new URL(input);
    if (!/(^|\.)archive\.org$/i.test(url.hostname)) return null;
    const parts = url.pathname.split('/').filter(Boolean);
    // /details/<id> and /download/<id>[/file] both name the item second.
    if (parts.length >= 2 && ['details', 'download', 'metadata'].includes(parts[0])) {
      return decodeURIComponent(parts[1]);
    }
    return null;
  } catch {
    return null;
  }
}

/** Route a URL through the relay so the emulator's own fetch is not CORS-blocked. */
export function viaRelay(url) {
  return RELAY + encodeURIComponent(url);
}

export const archiveResolver = {
  name: 'archive',
  canHandle(input) {
    return typeof input === 'string' && identifierFrom(input) !== null;
  },

  async resolve(source, { fetchImpl = fetch } = {}) {
    const id = identifierFrom(source.uri);
    let meta;
    try {
      const response = await fetchImpl(METADATA + encodeURIComponent(id));
      if (!response.ok) throw new Error(String(response.status));
      meta = await response.json();
    } catch {
      throw new PlaybackError(FAILURE.DEAD_STREAM, `archive.org item ${id}`);
    }

    const files = (meta.files || []).map((f) => ({
      name: f.name,
      length: Number(f.size || 0),
    }));
    const candidates = playableFiles(files);
    if (!candidates.length) {
      throw new PlaybackError(FAILURE.UNSUPPORTED_CODEC, `nothing playable in ${id}`);
    }

    const title = meta.metadata?.title || id;
    const core = coreFromMetadata(meta);

    // More than one playable file is a real choice, not something to guess at.
    if (candidates.length > 1) {
      return makeCollection({
        title,
        sources: candidates.map((f) => makeSource({
          kind: 'archive-file',
          uri: `${DOWNLOAD}${encodeURIComponent(id)}/${encodeURIComponent(f.name)}`,
          meta: { title: f.name, group: kindOf(f.name), core },
        })),
      });
    }

    return archiveFilePlayable(candidates[0].name, id, core);
  },
};

/** A single file inside an archive.org item. */
export function archiveFilePlayable(name, id, core = null) {
  const direct = `${DOWNLOAD}${encodeURIComponent(id)}/${encodeURIComponent(name)}`;
  const kind = kindOf(name);

  if (kind === 'video' || kind === 'audio' || kind === 'image') {
    // Media elements ignore CORS, so these can go direct and cost nothing.
    return makePlayable({
      render: kind === 'image' ? RENDER.IMAGE : (kind === 'audio' ? RENDER.AUDIO : RENDER.VIDEO),
      src: direct,
      mime: '',
    });
  }

  // Emulators and Ruffle fetch the bytes themselves, so they need the header
  // archive.org does not send.
  const proxied = viaRelay(direct);

  // archive.org's declared emulator wins over anything inferred from the file
  // name. Only fall back to the extension when the item did not say, and give
  // up rather than defaulting -- a wrong core boots and then does nothing.
  const chosen = core || coreFor(name);
  if (kind !== 'flash' && !chosen) {
    throw new PlaybackError(FAILURE.UNSUPPORTED_CODEC,
      `no emulator core for ${name}`);
  }
  let handle = null;
  return makePlayable({
    render: RENDER.CANVAS,
    src: proxied,
    mime: 'application/octet-stream',
    mount(el) {
      handle = kind === 'flash'
        ? mountRuffle(el, proxied)
        : mountEmulator(el, proxied, { core: chosen, name });
    },
    cleanup() {
      handle?.destroy();
      handle = null;
    },
  });
}

/** Resolver for a single file already chosen out of an item. */
export const archiveFileResolver = {
  name: 'archive-file',
  canHandle(input) {
    if (typeof input !== 'string') return false;
    const id = identifierFrom(input);
    if (!id) return false;
    try {
      return new URL(input).pathname.split('/').filter(Boolean).length >= 3;
    } catch {
      return false;
    }
  },
  async resolve(source, { fetchImpl = fetch } = {}) {
    const id = identifierFrom(source.uri);
    const parts = new URL(source.uri).pathname.split('/').filter(Boolean);
    const name = decodeURIComponent(parts.slice(2).join('/'));

    // The core may already be known from the item listing that produced this
    // source; only re-fetch metadata when it is not.
    let core = source.meta?.core ?? null;
    if (!core) {
      try {
        const response = await fetchImpl(METADATA + encodeURIComponent(id));
        if (response.ok) core = coreFromMetadata(await response.json());
      } catch {
        /* fall back to the extension below */
      }
    }
    return archiveFilePlayable(name, id, core);
  },
};
