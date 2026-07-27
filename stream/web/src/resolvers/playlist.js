import { parseM3U, looksLikeHls } from '../m3u.js';
import { makeCollection, makeSource, makePlayable, RENDER } from '../source.js';
import { PlaybackError, FAILURE } from '../failures.js';

const HLS_MIME = 'application/vnd.apple.mpegurl';

export const playlistResolver = {
  name: 'playlist',
  canHandle(input) {
    return /^https?:\/\/.*\.m3u8?(\?|$)/i.test(input);
  },
  async resolve(source, { fetchImpl = fetch } = {}) {
    let res;
    try {
      res = await fetchImpl(source.uri);
    } catch {
      throw new PlaybackError(FAILURE.DEAD_STREAM, source.uri);
    }
    if (!res.ok) throw new PlaybackError(FAILURE.DEAD_STREAM, `HTTP ${res.status}`);

    const body = await res.text();

    // Both shapes share the .m3u8 extension, so the body decides.
    if (looksLikeHls(body)) {
      return makePlayable({ render: RENDER.VIDEO, src: source.uri, mime: HLS_MIME });
    }

    const { entries } = parseM3U(body);
    return makeCollection({
      title: source.meta?.title ?? 'Playlist',
      sources: entries.map((e) => makeSource({
        kind: 'url',
        uri: e.uri,
        meta: { title: e.title, logo: e.logo, group: e.group },
      })),
    });
  },
};
