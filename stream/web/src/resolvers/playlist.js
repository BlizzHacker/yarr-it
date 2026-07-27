import { parseM3U, looksLikeHls } from '../m3u.js';
import { makeCollection, makeSource, makePlayable, RENDER } from '../source.js';
import { PlaybackError, FAILURE } from '../failures.js';

const HLS_MIME = 'application/vnd.apple.mpegurl';

export const playlistResolver = {
  name: 'playlist',
  // Matches on the URL's pathname only, the way embed.js matches on hostname
  // rather than the raw string. A naive regex over the whole URL lets `.*`
  // reach across the `?` and match a query-string value, so an ordinary
  // media URL carrying a tracking/redirect param that happens to end in
  // .m3u/.m3u8 (e.g. movie.mp4?ref=http://x.com/live.m3u8) gets stolen from
  // urlResolver even though this resolver is registered first. canHandle
  // must never throw - a malformed input just isn't a URL, so it's not ours.
  canHandle(input) {
    let url;
    try {
      url = new URL(input);
    } catch {
      return false;
    }
    if (!/^https?:$/i.test(url.protocol)) return false;
    return /\.m3u8?$/i.test(url.pathname);
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

    // Deliberate: this is a single fetch that never auto-expands a nested
    // playlist. A playlist-of-playlists cycle would require the user to
    // click through each level by hand, which is self-limiting. If a future
    // change wants to auto-expand nested playlists, it must add its own
    // depth guard rather than assuming this single-fetch shape still bounds
    // recursion.
    const { entries } = parseM3U(body);

    // Channel URIs are frequently relative to the playlist itself (IPTV
    // lists commonly point at paths like "seg/chan1.ts"). Resolve each one
    // against the playlist's own URL before handing it to makeSource - a
    // relative URI left as-is matches no resolver in the registry and blows
    // up as a bare, untyped Error instead of a PlaybackError the failure UI
    // can show. An entry whose URI can't be resolved is dropped rather than
    // failing the whole playlist over one bad line.
    const sources = [];
    for (const e of entries) {
      let absolute;
      try {
        absolute = new URL(e.uri, source.uri).href;
      } catch {
        continue;
      }
      sources.push(makeSource({
        kind: 'url',
        uri: absolute,
        meta: { title: e.title, logo: e.logo, group: e.group },
      }));
    }

    // An empty body, a header-only body, or a non-playlist file that slipped
    // past canHandle all parse to zero entries. Resolving that to a valid,
    // empty Collection hands the caller a blank screen with no explanation,
    // so treat it as a typed failure instead.
    if (sources.length === 0) {
      throw new PlaybackError(FAILURE.DEAD_STREAM, 'playlist contained no playable entries');
    }

    return makeCollection({
      title: source.meta?.title ?? 'Playlist',
      sources,
    });
  },
};
