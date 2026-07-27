import { makePlayable, RENDER } from '../source.js';
import { PlaybackError, FAILURE } from '../failures.js';

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

export const urlResolver = {
  name: 'url',
  canHandle(input) {
    return /^https?:\/\//i.test(input);
  },
  async resolve(source, { fetchImpl = fetch } = {}) {
    let res;
    try {
      res = await fetchImpl(source.uri, { method: 'HEAD' });
      if (HEAD_UNSUPPORTED.has(res.status)) {
        res = await fetchImpl(source.uri, {
          method: 'GET',
          headers: { Range: 'bytes=0-0' },
        });
      }
    } catch {
      throw new PlaybackError(FAILURE.DEAD_STREAM, source.uri);
    }
    if (!res.ok) throw new PlaybackError(FAILURE.DEAD_STREAM, `HTTP ${res.status}`);

    const mime = (res.headers.get('content-type') ?? '').split(';')[0].trim().toLowerCase();
    const render = renderForMime(mime);
    if (!render) throw new PlaybackError(FAILURE.UNSUPPORTED_CODEC, mime || 'unknown type');

    return makePlayable({ render, src: source.uri, mime });
  },
};
