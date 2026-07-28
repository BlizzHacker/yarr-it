import { makePlayable, RENDER } from '../source.js';
import { kindOf } from '../pickfile.js';

/**
 * archive.org items -- the Internet Archive's software and emulation
 * collections, where free, legal, instantly-playable games actually live.
 *
 * THIS EMBEDS ARCHIVE.ORG'S OWN PLAYER RATHER THAN RUNNING OUR OWN EMULATOR,
 * and that is worth explaining because the alternative was already built and
 * working before it was thrown away:
 *
 *   - archive.org sends no Access-Control-Allow-Origin on file downloads, so a
 *     WASM emulator here (which fetches the ROM bytes itself) is CORS-blocked.
 *     Getting a ROM into EmulatorJS meant proxying every byte through our own
 *     relay -- bandwidth we pay for, on a monthly cap shared with a mail
 *     server, for files somebody else is already happy to serve.
 *   - It also meant choosing an emulator core, which is a real trap: `dk.bin`
 *     is Colecovision, but `.bin` is equally Atari 2600 or Mega Drive.
 *     Defaulting an unknown ROM to the NES core boots EmulatorJS successfully
 *     and then runs the wrong machine -- it looks like it worked until you
 *     notice the game never starts.
 *
 * archive.org already solved both. Their Emularity player knows the correct
 * core for every item in their collections, serves the ROM itself, and costs
 * us nothing. So an archive.org item is an EMBED.
 *
 * EmulatorJS remains the right tool where there is no upstream player -- a ROM
 * out of a torrent or an NZB, where we hold the file and nobody else will run
 * it for us. See resolvers/game.js.
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

export const archiveResolver = {
  name: 'archive',
  canHandle(input) {
    return typeof input === 'string' && identifierFrom(input) !== null;
  },

  async resolve(source) {
    const id = identifierFrom(source.uri);
    const file = fileFrom(source.uri);

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
