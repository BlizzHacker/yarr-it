import { makePlayable, RENDER, TIER } from '../source.js';
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

const KIND_TO_RENDER = {
  video: RENDER.VIDEO,
  audio: RENDER.AUDIO,
  image: RENDER.IMAGE,
};

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

export function createTorrentResolver({ engine, classify, timeoutMs = 90000 }) {
  return {
    name: 'torrent',
    canHandle(input) {
      return typeof input === 'string' && (input.startsWith('magnet:') || INFOHASH.test(input.trim()));
    },
    resolve(source, _ctx) {
      const uri = normalizeMagnet(source.uri);

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
          onReady: async (file, torrent) => {
            if (settled) {
              console.warn('[torrent] onReady after resolve settled', file?.name);
              return;
            }
            settled = true;
            clear();
            try {
              const classifyFn = classify ?? (await loadEngineClassify());
              const render = renderForKind(classifyFn(file.name));

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
