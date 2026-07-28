import { makePlayable, makeCollection, makeSource, RENDER, TIER } from '../source.js';
import { mountRuffle } from './flash.js';
import { mountEmulator } from './game.js';
import { detectCore, SNIFF_BYTES } from '../rom-core.js';
import { needsChoice, playableFiles } from '../pickfile.js';
import { PlaybackError, FAILURE } from '../failures.js';

/**
 * The torrent path is the one thing here that was already proven in production,
 * so it is wrapped rather than rewritten: StreamEngine keeps its swarm,
 * bridge-peer and web-seed behaviour exactly as it is and simply gains the same
 * shape as every other resolver.
 *
 * engine.js (and everything it pulls in -- webtorrent, bridge-peer, tracker-udp)
 * is browser-only: bridge-peer.js reads `location` at module scope with no
 * guard, so it throws the instant it is imported outside a page. That is fine
 * in production, but it means the real classify() can only safely be imported
 * lazily, on first actual use -- not with a static top-level import, which
 * would make every environment that ever loads this module (including this
 * file's own unit tests, which never run in a browser) blow up regardless of
 * whether classify is ever called.
 */
let _enginePromise;
function loadEngineClassify() {
  if (!_enginePromise) _enginePromise = import('../engine.js').then((m) => m.classify);
  return _enginePromise;
}

const INFOHASH = /^[0-9a-f]{40}$/i;

/** An http(s) URL whose PATH ends in .torrent -- not merely one that mentions it. */
export function isTorrentFile(input) {
  try {
    const u = new URL(input);
    return /^https?:$/.test(u.protocol) && u.pathname.toLowerCase().endsWith('.torrent');
  } catch {
    return false;
  }
}

const KIND_TO_RENDER = {
  video: RENDER.VIDEO,
  audio: RENDER.AUDIO,
  image: RENDER.IMAGE,
  flash: RENDER.CANVAS,
  rom: RENDER.CANVAS,
};

/** Kinds that a canvas player (Ruffle, EmulatorJS) drives rather than an element. */
const CANVAS_KINDS = new Set(['flash', 'rom']);

const MIME_BY_EXT = {
  mp4: 'video/mp4',
  m4v: 'video/mp4',
  mkv: 'video/x-matroska',
  webm: 'video/webm',
  mp3: 'audio/mpeg',
  flac: 'audio/flac',
  m4a: 'audio/mp4',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  png: 'image/png',
  gif: 'image/gif',
};

export function renderForKind(kind) {
  return KIND_TO_RENDER[kind] ?? RENDER.VIDEO;
}

/** Guess a mime type from the file's extension so the player gets as much as the other resolvers give it. */
function mimeForName(name = '') {
  const ext = name.toLowerCase().split('.').pop();
  return MIME_BY_EXT[ext] ?? '';
}

/** Accept a full magnet URI or a bare 40-character info hash. */
export function normalizeMagnet(input) {
  const v = (input || '').trim();
  if (/^magnet:\?/i.test(v)) return v;
  if (/^[a-f0-9]{40}$/i.test(v)) {
    const trackers = [
      'udp://tracker.opentrackr.org:1337/announce',
      'udp://open.demonii.com:1337/announce',
      'udp://open.stealth.si:80/announce',
      'udp://exodus.desync.com:6969/announce',
      'udp://tracker.torrent.eu.org:451/announce',
    ];
    return `magnet:?xt=urn:btih:${v.toLowerCase()}` +
      trackers.map((t) => `&tr=${encodeURIComponent(t)}`).join('');
  }
  return null;
}

/**
 * Split a `<uri>#n=<index>` file choice off a torrent URI.
 *
 * A torrent holding two hundred ROMs has no single right answer, so the user
 * picks -- and the pick travels in the URI rather than in state somewhere.
 * That keeps choosing a file identical to playing anything else: one play()
 * call with a URI, nothing stored that can drift out of step with the screen.
 *
 * `#` cannot appear in a magnet or a URL path unencoded, so this is safe to
 * split on.
 */
export function splitFileChoice(input) {
  const raw = String(input ?? '');
  const match = /#n=(\d+)$/.exec(raw);
  if (!match) return { uri: raw, index: null };
  return { uri: raw.slice(0, match.index), index: Number(match[1]) };
}

/**
 * A Flash movie or a ROM out of a torrent.
 *
 * Neither can be streamed. Ruffle needs the whole SWF and an emulator needs the
 * whole cartridge before it can boot -- there is no "start at the first byte you
 * need" for a ROM image. So this waits for the file to finish, showing progress
 * rather than a frozen blank canvas, and only then hands the bytes to a player.
 *
 * That is fine for what it targets: cartridge-era ROMs and Flash files are
 * kilobytes to a few megabytes, so the wait is seconds on a healthy swarm.
 */
function canvasPlayable(file, torrent, kind, engine) {
  let handle = null;
  let blobUrl = null;
  let progressTimer = null;

  const stopProgress = () => {
    if (progressTimer) clearInterval(progressTimer);
    progressTimer = null;
  };

  return makePlayable({
    render: RENDER.CANVAS,
    src: '',
    mime: mimeForName(file.name),
    tier: TIER.DIRECT,
    mount(el) {
      const label = document.createElement('div');
      label.style.padding = '18px';
      label.style.textAlign = 'center';
      label.textContent = `Downloading ${file.name}…`;
      el.replaceChildren(label);

      // file.progress is 0..1 while the piece picker works through the file.
      progressTimer = setInterval(() => {
        const pct = Math.round((file.progress ?? 0) * 100);
        if (Number.isFinite(pct)) {
          label.textContent = `Downloading ${file.name}… ${pct}%`;
        }
      }, 500);

      file.blob()
        .then(async (blob) => {
          stopProgress();
          blobUrl = URL.createObjectURL(blob);
          if (kind === 'flash') {
            handle = mountRuffle(el, blobUrl);
            return;
          }

          // A torrent, unlike an archive.org item, declares nothing about what
          // machine its ROM is for -- and the extension often cannot say
          // either, since `.bin` is Colecovision, Atari 2600 and Mega Drive
          // alike. Booting the wrong core is silent: the emulator starts, the
          // ROM loads, and the screen stays black forever.
          //
          // The file is already downloaded here, so the ROM's own header is
          // available to answer the question properly.
          const head = new Uint8Array(await blob.slice(0, SNIFF_BYTES).arrayBuffer());
          const { core, via } = detectCore({
            name: file.name,
            bytes: head,
            // The release and folder names are how a torrent says what a
            // headerless `.bin` actually is.
            context: `${torrent?.name ?? ''} ${file.path ?? ''}`,
            length: blob.size,
          });

          if (!core) {
            label.textContent = `${file.name} is a ROM, but its system could not be `
              + 'identified, so it would boot the wrong emulator. Try a file whose '
              + 'name gives the system, or play this title from archive.org.';
            el.replaceChildren(label);
            return;
          }

          handle = mountEmulator(el, blobUrl, { core, name: file.name, via });
        })
        .catch((err) => {
          stopProgress();
          label.textContent = `Could not load ${file.name}: ${err.message}`;
        });
    },
    cleanup: () => {
      stopProgress();
      handle?.destroy();
      handle = null;
      if (blobUrl) URL.revokeObjectURL(blobUrl);
      blobUrl = null;
      // Scoped teardown: only destroy the torrent if the engine is still on
      // the one this Playable was built for.
      try {
        if (!engine?.torrent || engine.torrent === torrent) engine?.destroyTorrent?.();
      } catch {
        /* engine already torn down */
      }
    },
  });
}

export function createTorrentResolver({ engine, classify, timeoutMs = 90000 }) {
  return {
    name: 'torrent',
    canHandle(input) {
      if (typeof input !== 'string') return false;
      // A file chosen out of a pack comes back as `<uri>#n=<index>`, and that
      // has to still be recognised as the torrent it came from -- otherwise
      // picking an entry falls through to the url resolver and plays nothing.
      const v = splitFileChoice(input.trim()).uri;
      return /^magnet:/i.test(v) || INFOHASH.test(v) || isTorrentFile(v);
    },
    resolve(source, _ctx) {
      // A .torrent FILE carries the metadata inline, so the file list is known
      // immediately. A magnet does not -- WebTorrent has to fetch metadata from
      // a peer via ut_metadata first, and a BEP-19 web seed serves content, not
      // metadata. So a magnet with only a web seed and no peers never becomes
      // ready. Pass a .torrent URL through untouched; WebTorrent fetches and
      // parses it itself.
      // A chosen file comes back as `<uri>#n=<index>`. Carrying the choice in
      // the URI keeps it stateless: picking an entry out of a library is just
      // another play() call, and there is no selection stored anywhere to fall
      // out of step with what is on screen.
      const { uri: bare, index: wantIndex } = splitFileChoice(source.uri);
      const uri = isTorrentFile(bare) ? bare.trim() : normalizeMagnet(bare);

      return new Promise((resolve, reject) => {
        let settled = false;
        let timer = null;

        const clear = () => {
          if (timer) clearTimeout(timer);
          timer = null;
        };

        if (timeoutMs > 0) {
          timer = setTimeout(() => {
            if (settled) return;
            settled = true;
            reject(new PlaybackError(FAILURE.DEAD_STREAM, `no response after ${timeoutMs}ms`));
          }, timeoutMs);
        }

        engine.add(uri, {
          onFiles: (torrent) => {
            if (settled) return true;

            // An explicit choice was already made, so honour it rather than
            // asking again.
            if (wantIndex !== null) {
              const chosen = torrent.files?.[wantIndex];
              if (!chosen) {
                settled = true;
                clear();
                reject(new PlaybackError(
                  FAILURE.UNSUPPORTED_CODEC,
                  'that file is no longer in this torrent',
                ));
                return true;
              }
              return false; // fall through to onReady with the engine's pick
            }

            // Several real choices means the honest answer is a list. A ROM
            // pack is the case that matters: its biggest file is not "the
            // game", it is whichever game happened to be biggest.
            const indexed = (torrent.files ?? []).map((f, i) => ({
              name: f.name, length: f.length, index: i,
            }));
            if (!needsChoice(indexed)) return false;

            settled = true;
            clear();
            resolve(makeCollection({
              title: torrent.name || 'Choose a file',
              sources: playableFiles(indexed).map((f) => makeSource({
                kind: 'auto',
                uri: `${source.uri}#n=${f.index}`,
                meta: { title: f.name, length: f.length },
              })),
            }));
            return true;
          },
          onReady: async (file, torrent) => {
            // When a specific file was asked for, the engine's own pick is not
            // it.
            if (wantIndex !== null && torrent.files?.[wantIndex]) {
              file = torrent.files[wantIndex];
            }
            if (settled) {
              console.warn('[torrent] onReady after resolve settled', file?.name);
              return;
            }
            settled = true;
            clear();
            try {
              const classifyFn = classify ?? (await loadEngineClassify());
              const kind = classifyFn(file.name);
              const render = renderForKind(kind);

              if (CANVAS_KINDS.has(kind)) {
                resolve(canvasPlayable(file, torrent, kind, engine));
                return;
              }

              // file.streamURL is served by WebTorrent's service worker,
              // which answers range requests so playback can start and seek
              // while the download is still in flight. The worker is
              // unavailable in some browsers (private windows, some
              // webviews) -- engine._serverReady reports that. A fake
              // engine used in tests that never sets _serverReady at all is
              // not making a claim about worker availability one way or the
              // other, so that case is treated as "ready" rather than
              // triggering the fallback; only an engine that explicitly
              // resolves _serverReady to a falsy value asks for it.
              const serverReady = engine && '_serverReady' in engine
                ? await engine._serverReady
                : true;

              let src = file.streamURL;
              let blobUrl = null;
              if (!serverReady || !file.streamURL) {
                // No service worker to answer streamURL -- fall back to a
                // blob. This only becomes playable once the file has fully
                // downloaded, but that beats pointing the element at a URL
                // nothing will ever answer, which just hangs forever.
                const blob = await file.blob();
                blobUrl = URL.createObjectURL(blob);
                src = blobUrl;
              }

              resolve(makePlayable({
                render,
                src,
                mime: mimeForName(file.name),
                tier: TIER.DIRECT,
                cleanup: () => {
                  // Always release this Playable's own blob URL, independent
                  // of whether the torrent teardown below fires.
                  if (blobUrl) URL.revokeObjectURL(blobUrl);
                  // Scoped to the torrent this Playable was built for: a stale
                  // cleanup() from a superseded source must never tear down
                  // whatever the engine has since moved on to.
                  if (!engine || engine.torrent !== torrent) return;
                  engine.destroyTorrent();
                },
              }));
            } catch (err) {
              reject(err);
            }
          },
          onError: (err) => {
            if (settled) {
              console.warn('[torrent] onError after resolve settled', err?.message || err);
              return;
            }
            settled = true;
            clear();
            reject(err);
          },
        });
      });
    },
  };
}
