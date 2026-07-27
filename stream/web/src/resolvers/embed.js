import { makePlayable, RENDER } from '../source.js';

/**
 * YouTube and Vimeo are embeds, never extractions. Pulling stream URLs out of
 * either service violates its terms and gets the relay IP blocked, so this
 * resolver only ever emits an official iframe URL.
 *
 * Matching is done against the parsed URL's hostname and pathname/query, not
 * against the raw input string, so a URL like
 * https://evil.com/youtube.com/watch?v=... is never mistaken for YouTube.
 */

const YOUTUBE_HOSTS = new Set([
  'youtube.com',
  'www.youtube.com',
  'm.youtube.com',
  'music.youtube.com',
]);
const YOUTUBE_SHORT_HOST = 'youtu.be';
const YOUTUBE_ID_RE = /^[\w-]{11}$/;

const VIMEO_HOSTS = new Set([
  'vimeo.com',
  'www.vimeo.com',
  'player.vimeo.com',
]);
const VIMEO_PATH_PATTERNS = [
  /^\/(\d+)$/,
  /^\/video\/(\d+)$/,
  /^\/channels\/[^/]+\/(\d+)$/,
  /^\/groups\/[^/]+\/videos\/(\d+)$/,
];

function youtubeId(url) {
  if (url.hostname === YOUTUBE_SHORT_HOST) {
    const id = url.pathname.slice(1);
    return YOUTUBE_ID_RE.test(id) ? id : null;
  }
  if (!YOUTUBE_HOSTS.has(url.hostname)) return null;
  if (url.pathname === '/watch') {
    const v = url.searchParams.get('v');
    return v && YOUTUBE_ID_RE.test(v) ? v : null;
  }
  const embedMatch = url.pathname.match(/^\/embed\/([\w-]{11})$/);
  return embedMatch ? embedMatch[1] : null;
}

function vimeoId(url) {
  if (!VIMEO_HOSTS.has(url.hostname)) return null;
  for (const re of VIMEO_PATH_PATTERNS) {
    const m = url.pathname.match(re);
    if (m) return m[1];
  }
  return null;
}

export function embedUrlFor(input) {
  let url;
  try {
    url = new URL(input);
  } catch {
    return null;
  }

  const yid = youtubeId(url);
  if (yid) return `https://www.youtube.com/embed/${yid}`;

  const vid = vimeoId(url);
  if (vid) return `https://player.vimeo.com/video/${vid}`;

  return null;
}

export const embedResolver = {
  name: 'embed',
  canHandle(input) {
    return embedUrlFor(input) !== null;
  },
  async resolve(source) {
    return makePlayable({
      render: RENDER.EMBED,
      src: embedUrlFor(source.uri),
      mime: 'text/html',
    });
  },
};
