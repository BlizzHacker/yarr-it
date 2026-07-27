import { makePlayable, RENDER } from '../source.js';
import { PlaybackError, FAILURE } from '../failures.js';

const PREFIX = {
  'video/': RENDER.VIDEO,
  'audio/': RENDER.AUDIO,
  'image/': RENDER.IMAGE,
};

export function renderForMime(mime = '') {
  const found = Object.entries(PREFIX).find(([p]) => mime.startsWith(p));
  return found ? found[1] : null;
}

export const urlResolver = {
  name: 'url',
  canHandle(input) {
    return /^https?:\/\//i.test(input);
  },
  async resolve(source, { fetchImpl = fetch } = {}) {
    let res;
    try {
      res = await fetchImpl(source.uri, { method: 'HEAD' });
    } catch {
      throw new PlaybackError(FAILURE.DEAD_STREAM, source.uri);
    }
    if (!res.ok) throw new PlaybackError(FAILURE.DEAD_STREAM, `HTTP ${res.status}`);

    const mime = (res.headers.get('content-type') ?? '').split(';')[0].trim();
    const render = renderForMime(mime);
    if (!render) throw new PlaybackError(FAILURE.UNSUPPORTED_CODEC, mime || 'unknown type');

    return makePlayable({ render, src: source.uri, mime });
  },
};
