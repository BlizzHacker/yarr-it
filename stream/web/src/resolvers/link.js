/**
 * The paste-a-link resolver.
 *
 * Two separate jobs, deliberately kept apart:
 *
 *   resolveLink()       asks the server what a pasted URL contains. Returns
 *                       data, not a Playable, because the panel needs the
 *                       whole format list, not just one stream.
 *
 *   createLinkResolver() is a registry resolver claiming the signed
 *                       /api/link/media URLs that resolveLink hands back, so
 *                       a chosen format plays through the existing player with
 *                       no parallel playback path.
 *
 * The second one exists to avoid a probe. The generic `url` resolver would
 * HEAD the media URL to sniff its type -- harmless for a plain file, but for a
 * format the server has to repack it would start an ffmpeg run just to read a
 * content-type and then throw the result away. We already know what the format
 * is, so it is passed through on the source's meta.
 */

import { makePlayable, RENDER, TIER } from '../source.js';
import { PlaybackError, FAILURE } from '../failures.js';

export const MEDIA_PREFIX = '/api/link/media';

/** A structured failure, so the UI can say what went wrong rather than "error". */
export class LinkError extends Error {
  constructor({ error, code, extractor, upstream }) {
    super(error || 'that link could not be read');
    this.name = 'LinkError';
    this.code = code || 'extractor_broken';
    this.extractor = extractor || '';
    this.upstream = upstream || '';
    this.error = error;
  }
}

/**
 * Ask the server to resolve a pasted URL.
 *
 * Errors are normalised into LinkError with the server's own code intact. A
 * network failure gets its own code rather than being folded into
 * extractor_broken, because "your connection dropped" and "Instagram changed
 * their page" want completely different responses from the reader.
 */
export async function resolveLink(uri, { fetchImpl = fetch, signal } = {}) {
  let res;
  try {
    res = await fetchImpl(`/api/link/resolve?u=${encodeURIComponent(uri)}`, { signal });
  } catch (err) {
    if (err?.name === 'AbortError') throw err;
    throw new LinkError({ code: 'network', error: `could not reach the resolver: ${err.message}` });
  }

  let body;
  try {
    body = await res.json();
  } catch {
    body = {};
  }
  if (!res.ok) throw new LinkError(body);
  if (!body || (!body.formats?.length && !body.items?.length)) {
    throw new LinkError({
      code: 'no_formats',
      error: 'that page resolved, but there was nothing on it to download',
      extractor: body?.extractor,
    });
  }
  return body;
}

const RENDER_FOR_KIND = {
  audio: RENDER.AUDIO,
  image: RENDER.IMAGE,
  video: RENDER.VIDEO,
};

/**
 * Build a Playable for one already-resolved format.
 *
 * No probe, no guessing: the server has told us whether this carries a picture
 * and what container it is in.
 */
export function playableForFormat(format, kind = 'video') {
  if (!format?.media) {
    throw new PlaybackError(FAILURE.DEAD_STREAM, 'that format has no playable URL');
  }
  let render = RENDER_FOR_KIND[kind] ?? RENDER.VIDEO;
  if (!format.hasVideo && format.hasAudio) render = RENDER.AUDIO;
  else if (format.hasVideo) render = RENDER.VIDEO;

  return makePlayable({
    render,
    src: format.media,
    // Everything the server repacks comes out as MP4 whatever went in, so the
    // source container is not what the element will be handed.
    mime: format.muxed || format.streaming ? 'video/mp4' : mimeForExt(format.ext),
    // Always DIRECT: this is our own origin. It is already the proxied form --
    // sending it up the gateway/relay ladder would proxy a proxy.
    tier: TIER.DIRECT,
  });
}

function mimeForExt(ext = '') {
  switch (ext.toLowerCase()) {
    case 'mp4':
    case 'm4v':
      return 'video/mp4';
    case 'webm':
      return 'video/webm';
    case 'mkv':
      return 'video/x-matroska';
    case 'm4a':
      return 'audio/mp4';
    case 'mp3':
      return 'audio/mpeg';
    case 'opus':
    case 'ogg':
      return 'audio/ogg';
    case 'jpg':
    case 'jpeg':
      return 'image/jpeg';
    case 'png':
      return 'image/png';
    default:
      return '';
  }
}

export function createLinkResolver() {
  return {
    name: 'link',
    // Claims only the signed media URLs this feature mints. Registering
    // something broader would quietly take playback away from the torrent,
    // playlist and archive resolvers.
    canHandle(input) {
      return typeof input === 'string' && input.startsWith(MEDIA_PREFIX);
    },
    async resolve(source) {
      const format = source?.meta?.format;
      if (format) return playableForFormat(format, source.meta.kind);
      // A media URL with no format attached (a shared link, a reload): it is
      // still ours and still same-origin, so play it as video and let the
      // element report any real problem.
      return makePlayable({
        render: RENDER.VIDEO,
        src: source.uri,
        mime: 'video/mp4',
        tier: TIER.DIRECT,
      });
    },
  };
}

/**
 * Should the paste box hand this URL to the link resolver rather than to the
 * existing resolvers?
 *
 * Yes for anything the generic resolvers would have taken -- `embed` (YouTube,
 * Vimeo) and `url` (everything else http) -- because those two are exactly the
 * cases where a format list is more use than an iframe or a blind <video src>.
 *
 * No for the specific ones. A `.m3u8` channel list is a library to browse, a
 * `.swf` needs Ruffle and a ROM needs an emulator; running any of those
 * through a video extractor would break behaviour that already works.
 */
const GENERIC_RESOLVERS = new Set(['embed', 'url']);

export function shouldResolveAsLink(uri, registry) {
  if (typeof uri !== 'string' || !/^https?:\/\//i.test(uri)) return false;
  if (uri.startsWith(MEDIA_PREFIX)) return false;
  // A URL that is plainly a media file already plays instantly through the url
  // resolver. Sending it to a video extractor would cost a subprocess and a
  // second or two to learn what the extension already said.
  if (looksLikeDirectMedia(uri)) return false;
  const claimed = registry?.find?.(uri);
  return !claimed || GENERIC_RESOLVERS.has(claimed.name);
}

const DIRECT_MEDIA_EXT =
  /\.(mp4|m4v|webm|mkv|mov|avi|mp3|m4a|flac|wav|ogg|opus|jpg|jpeg|png|gif|webp)(\?|#|$)/i;

export function looksLikeDirectMedia(uri) {
  try {
    const { pathname } = new URL(uri);
    return DIRECT_MEDIA_EXT.test(pathname);
  } catch {
    return false;
  }
}
