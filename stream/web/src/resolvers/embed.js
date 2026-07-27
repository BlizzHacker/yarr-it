import { makePlayable, RENDER } from '../source.js';

/**
 * YouTube and Vimeo are embeds, never extractions. Pulling stream URLs out of
 * either service violates its terms and gets the relay IP blocked, so this
 * resolver only ever emits an official iframe URL.
 */

const YOUTUBE = [
  /(?:youtube\.com\/watch\?(?:.*&)?v=)([\w-]{11})/,
  /(?:youtu\.be\/)([\w-]{11})/,
  /(?:youtube\.com\/embed\/)([\w-]{11})/,
];
const VIMEO = /vimeo\.com\/(?:video\/)?(\d+)/;

export function embedUrlFor(input) {
  for (const re of YOUTUBE) {
    const m = input.match(re);
    if (m) return `https://www.youtube.com/embed/${m[1]}`;
  }
  const v = input.match(VIMEO);
  if (v) return `https://player.vimeo.com/video/${v[1]}`;
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
