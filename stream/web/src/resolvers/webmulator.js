import { makePlayable, RENDER } from '../source.js';

// Webmulator publishes this as its visible "Play ... Now" destination. We
// embed that page unchanged. In particular, we never inspect its nested iframe
// or recover the private rom_url inside it.
const HOSTS = new Set(['webmulator.com', 'www.webmulator.com']);
const MOBILE = /^\/games\/[a-z0-9][a-z0-9-]*\/[a-z0-9][a-z0-9-]*\/mobile\/?$/;

export function webmulatorEmbedURL(input) {
  let url;
  try {
    url = new URL(input);
  } catch {
    return null;
  }
  if (url.protocol !== 'https:' || !HOSTS.has(url.hostname) || !MOBILE.test(url.pathname)) {
    return null;
  }
  url.search = '';
  url.hash = '';
  return url.href;
}

export const webmulatorResolver = {
  name: 'webmulator',
  canHandle(input) {
    return webmulatorEmbedURL(input) !== null;
  },
  async resolve(source) {
    return makePlayable({
      render: RENDER.EMBED,
      src: webmulatorEmbedURL(source.uri),
      mime: 'text/html',
    });
  },
};
