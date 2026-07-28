import { makePlayable, RENDER, TIER } from '../source.js';
import { PlaybackError, FAILURE } from '../failures.js';
import { probeTier } from '../ladder.js';

const PREFIX = {
  'video/': RENDER.VIDEO,
  'audio/': RENDER.AUDIO,
  'image/': RENDER.IMAGE,
};

export function renderForMime(mime = '') {
  const lower = mime.toLowerCase();
  const found = Object.entries(PREFIX).find(([p]) => lower.startsWith(p));
  return found ? found[1] : null;
}

// Some CDNs and media servers reject HEAD with 405 (Method Not Allowed) or
// 501 (Not Implemented) even when the resource is perfectly playable. In
// that case retry once with a ranged GET instead of reporting the stream
// dead on a probe method the server just doesn't support.
const HEAD_UNSUPPORTED = new Set([405, 501]);

const EXT_RENDER = {
  mp4: RENDER.VIDEO, m4v: RENDER.VIDEO, mkv: RENDER.VIDEO, webm: RENDER.VIDEO,
  mov: RENDER.VIDEO, avi: RENDER.VIDEO, ts: RENDER.VIDEO, m3u8: RENDER.VIDEO,
  mp3: RENDER.AUDIO, aac: RENDER.AUDIO, flac: RENDER.AUDIO, m4a: RENDER.AUDIO,
  wav: RENDER.AUDIO, ogg: RENDER.AUDIO,
  jpg: RENDER.IMAGE, jpeg: RENDER.IMAGE, png: RENDER.IMAGE, gif: RENDER.IMAGE, webp: RENDER.IMAGE,
};

/**
 * Best-effort render kind when there is no HTTP response to read a
 * content-type from. Most extension-less URLs that reach this path are live
 * IPTV streams, so an unrecognised (or missing) extension guesses video
 * rather than giving up.
 */
function renderForExt(uri) {
  try {
    const { pathname } = new URL(uri);
    const ext = pathname.toLowerCase().split('.').pop();
    return EXT_RENDER[ext] ?? RENDER.VIDEO;
  } catch {
    return RENDER.VIDEO;
  }
}

export const urlResolver = {
  name: 'url',
  canHandle(input) {
    return /^https?:\/\//i.test(input);
  },
  async resolve(source, { fetchImpl = fetch, pageProtocol, gateway, relayAvailable } = {}) {
    let res;
    let networkFailure = false;
    try {
      res = await fetchImpl(source.uri, { method: 'HEAD' });
      if (HEAD_UNSUPPORTED.has(res.status)) {
        res = await fetchImpl(source.uri, {
          method: 'GET',
          headers: { Range: 'bytes=0-0' },
        });
      }
    } catch {
      networkFailure = true;
    }

    if (networkFailure) {
      // A <video>/<audio>/<img> element is not subject to CORS - only a real
      // fetch/XHR is. So a probe that fails at the network level (which is
      // often exactly a CORS preflight rejection) does NOT mean the element
      // can't play the URL; it means this probe couldn't confirm it. The one
      // failure a media element genuinely cannot work around is mixed
      // content (an https page loading an http:// resource), because that is
      // enforced on the element itself, not just on fetch. Everything else
      // attaches optimistically and lets the element's own network stack
      // have the real, authoritative attempt.
      const resolvedProtocol = pageProtocol ??
        (typeof location !== 'undefined' ? location.protocol : 'https:');
      const insecure = /^http:\/\//i.test(source.uri);

      if (resolvedProtocol === 'https:' && insecure) {
        const probe = await probeTier(source.uri, {
          fetchImpl, pageProtocol: resolvedProtocol, gateway, relayAvailable,
        });
        if (!probe.tier) throw new PlaybackError(probe.blockedBy ?? FAILURE.MIXED_CONTENT, source.uri);
        return makePlayable({ render: renderForExt(source.uri), src: probe.url, mime: '', tier: probe.tier });
      }

      return makePlayable({ render: renderForExt(source.uri), src: source.uri, mime: '', tier: TIER.DIRECT });
    }

    if (!res.ok) throw new PlaybackError(FAILURE.DEAD_STREAM, `HTTP ${res.status}`);

    const mime = (res.headers.get('content-type') ?? '').split(';')[0].trim().toLowerCase();
    const render = renderForMime(mime);
    if (!render) throw new PlaybackError(FAILURE.UNSUPPORTED_CODEC, mime || 'unknown type');

    return makePlayable({ render, src: source.uri, mime });
  },
};
