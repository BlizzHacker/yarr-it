import { makePlayable, RENDER } from '../source.js';
import { kindOf, bestFile } from '../pickfile.js';
import { mountEmulator } from './game.js';
import { mountRuffle } from './flash.js';
import { PlaybackError, FAILURE } from '../failures.js';

/**
 * archive.org items -- the Internet Archive's software and emulation
 * collections, where free, legal, instantly-playable games actually live.
 *
 * AN ITEM CAN BE PLAYED TWO WAYS, AND WHICH IS BETTER DEPENDS ON THE DEVICE.
 *
 * Their own Emularity player (RENDER.EMBED) costs us nothing: they serve the
 * ROM, they know the right core for every item, and no byte crosses our relay.
 * On a desktop it is the better option outright.
 *
 * But it expects a keyboard and offers no on-screen controls, so on a phone a
 * console game there is something you can watch and not play. EmulatorJS has a
 * virtual gamepad, and running it here fixes that -- at a price:
 *
 *   - archive.org sends no Access-Control-Allow-Origin on downloads, and a WASM
 *     emulator fetches the ROM itself, so every byte has to come through our
 *     relay, whose monthly cap is shared with a mail server. Hence the size
 *     ceiling and the metadata lookup happening at play time rather than for
 *     all sixty search results.
 *   - it needs a core, and a core must never be guessed from a file extension:
 *     `dk.bin` is Colecovision, but `.bin` is equally Atari 2600 or Mega Drive,
 *     and the wrong core boots successfully and then runs nothing. The core
 *     here is translated from the `emulator` field archive.org itself declares,
 *     which is authoritative.
 *
 * So both are offered, `#ejs` selects ours, and search ranks ours first.
 *
 * For a ROM out of a torrent or an NZB there is no upstream player at all, so
 * EmulatorJS is the only option -- see resolvers/game.js and rom-core.js, where
 * the core has to be recovered from the ROM's own header.
 */

const EMBED = 'https://archive.org/embed/';
const DOWNLOAD = 'https://archive.org/download/';
const METADATA = 'https://archive.org/metadata/';

/** Emulation-focused collections worth searching. */
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
    if (parts.length >= 2 && ['details', 'download', 'metadata', 'embed'].includes(parts[0])) {
      return decodeURIComponent(parts[1]);
    }
    return null;
  } catch {
    return null;
  }
}

/** The file within an item URL, when the URL names one. */
export function fileFrom(input) {
  try {
    const parts = new URL(input).pathname.split('/').filter(Boolean);
    return parts.length >= 3 ? decodeURIComponent(parts.slice(2).join('/')) : null;
  } catch {
    return null;
  }
}

export function embedUrl(id, file = null) {
  const base = EMBED + encodeURIComponent(id);
  // Their player takes a start file, which is how one ROM out of a multi-ROM
  // item gets launched instead of whatever the item defaults to.
  return file ? `${base}?start=${encodeURIComponent(file)}` : base;
}

/**
 * archive.org's emulator id -> the EmulatorJS core that runs the same machine.
 *
 * Their id is authoritative about the system; this is only the translation.
 * Nothing here is guessed from a file extension, which is the mistake that
 * booted a Colecovision game as an NES.
 */
const IA_TO_EJS = {
  nes: 'nes', famicom: 'nes',
  snes: 'snes', superfamicom: 'snes',
  gameboy: 'gb', gb: 'gb', gbcolor: 'gb', gbc: 'gb',
  gba: 'gba',
  n64: 'n64',
  genesis: 'segaMD', megadriv: 'segaMD', segaMD: 'segaMD',
  '32x': 'sega32x',
  sms: 'segaMS', smsj: 'segaMS',
  gamegear: 'segaGG', gg: 'segaGG',
  psx: 'psx',
  coleco: 'coleco',
  a2600: 'atari2600',
  a7800: 'atari7800',
  lynx: 'lynx',
  tg16: 'pce',
  wswan: 'ws', wscolor: 'ws',
  ngpc: 'ngp', ngp: 'ngp',
  vb: 'vb',
};

export function ejsCoreFor(emulator) {
  return IA_TO_EJS[String(emulator || '')] ?? null;
}

/**
 * archive.org sends no Access-Control-Allow-Origin, and an emulator running
 * here fetches the ROM itself, so those bytes have to come through the relay.
 * Their own player does not need this -- which is exactly the trade being
 * made: bandwidth for touch controls.
 */
export function viaRelay(url) {
  return `/bridge/iptv?u=${encodeURIComponent(url)}`;
}

/**
 * Past this, paying to relay a ROM is not worth it. Cartridge-era games are
 * kilobytes to a few megabytes; anything larger is a disc image, which the
 * archive's own player streams and ours would have to download in full.
 */
export const MAX_RELAY_ROM = 48 << 20;

/** A SWF is typically about a megabyte, so this is generous rather than tight. */
export const MAX_RELAY_SWF = 24 << 20;

/**
 * Play an archive.org item with EmulatorJS instead of the archive's player.
 *
 * Worth the bandwidth because their Emularity player expects a keyboard and
 * offers no on-screen controls, so on a phone a console game there is
 * something you can watch and not play. EmulatorJS has a virtual gamepad.
 *
 * The file list is fetched here rather than during search: finding the ROM
 * inside an item costs a metadata request each, and doing sixty of them per
 * search would slow every search down for a link most people never click.
 */
async function playHere(id, wantFile, { fetchImpl }) {
  const info = await itemInfo(id, { fetchImpl });
  const core = ejsCoreFor(info.emulator);
  if (!core) {
    throw new PlaybackError(
      FAILURE.UNSUPPORTED_CODEC,
      `${info.title} runs on a system this player has no core for — use Play at archive.org.`,
    );
  }

  const named = wantFile && info.files.find((f) => f.name === wantFile);
  const rom = named || bestFile(info.files);
  if (!rom) {
    throw new PlaybackError(FAILURE.UNSUPPORTED_CODEC, 'nothing playable in this item');
  }
  if (rom.length > MAX_RELAY_ROM) {
    throw new PlaybackError(
      FAILURE.UNSUPPORTED_CODEC,
      `${rom.name} is too large to play here — use Play at archive.org.`,
    );
  }

  const url = viaRelay(`${DOWNLOAD}${encodeURIComponent(id)}/${encodeURIComponent(rom.name)}`);
  let handle = null;
  return makePlayable({
    render: RENDER.CANVAS,
    src: url,
    mime: 'application/octet-stream',
    mount(el) {
      handle = mountEmulator(el, url, { core, name: rom.name });
    },
    cleanup() {
      handle?.destroy();
      handle = null;
    },
  });
}

/**
 * Play an archive.org Flash item with our own Ruffle.
 *
 * Same trade as playHere: their player works, ours has touch support and sits
 * inside this app's controls. The trade is much cheaper here -- a SWF is about
 * a megabyte where a ROM can be tens -- so the ceiling is lower and it is
 * rarely reached.
 */
async function playFlashHere(id, wantFile, { fetchImpl }) {
  const info = await itemInfo(id, { fetchImpl });
  const named = wantFile && info.files.find((f) => f.name === wantFile);
  const swf = named || info.files.find((f) => kindOf(f.name) === 'flash');
  if (!swf) {
    throw new PlaybackError(FAILURE.UNSUPPORTED_CODEC, 'no Flash file in this item');
  }
  if (swf.length > MAX_RELAY_SWF) {
    throw new PlaybackError(
      FAILURE.UNSUPPORTED_CODEC,
      `${swf.name} is too large to play here — use Play at archive.org.`,
    );
  }

  const url = viaRelay(`${DOWNLOAD}${encodeURIComponent(id)}/${encodeURIComponent(swf.name)}`);
  let handle = null;
  return makePlayable({
    render: RENDER.CANVAS,
    src: url,
    mime: 'application/x-shockwave-flash',
    mount(el) {
      handle = mountRuffle(el, url);
    },
    cleanup() {
      handle?.destroy();
      handle = null;
    },
  });
}

export const archiveResolver = {
  name: 'archive',
  canHandle(input) {
    return typeof input === 'string' && identifierFrom(input) !== null;
  },

  async resolve(source, { fetchImpl = fetch } = {}) {
    const id = identifierFrom(source.uri);
    const file = fileFrom(source.uri);

    // `#ejs` and `#swf` are how a search result asks for our own player
    // rather than the archive's.
    if (/#ejs$/.test(source.uri)) {
      return playHere(id, file, { fetchImpl });
    }
    if (/#swf$/.test(source.uri)) {
      return playFlashHere(id, file, { fetchImpl });
    }

    // A direct link to plain media is worth playing natively: a video element
    // gives real seeking and fullscreen that an iframe does not, and media
    // elements ignore CORS so it costs nothing either way.
    if (file) {
      const kind = kindOf(file);
      if (kind === 'video' || kind === 'audio' || kind === 'image') {
        return makePlayable({
          render: kind === 'image' ? RENDER.IMAGE
            : kind === 'audio' ? RENDER.AUDIO : RENDER.VIDEO,
          src: `${DOWNLOAD}${encodeURIComponent(id)}/${encodeURIComponent(file)}`,
          mime: '',
        });
      }
    }

    // Everything emulated -- ROMs, arcade, MS-DOS -- goes to their player.
    // No bytes cross our relay and no core has to be guessed.
    return makePlayable({
      render: RENDER.EMBED,
      src: embedUrl(id, file),
      mime: 'text/html',
    });
  },
};

/**
 * Item metadata, for search results that want a title or a file list.
 *
 * Playback no longer needs this -- the embed handles it -- but a search UI
 * does, and so does anything that wants to offer a specific ROM.
 */
export async function itemInfo(id, { fetchImpl = fetch } = {}) {
  const response = await fetchImpl(METADATA + encodeURIComponent(id));
  if (!response.ok) throw new Error(`archive.org item ${id}: ${response.status}`);
  const meta = await response.json();
  return {
    id,
    title: meta.metadata?.title || id,
    emulator: meta.metadata?.emulator || null,
    files: (meta.files || []).map((f) => ({ name: f.name, length: Number(f.size || 0) })),
  };
}
