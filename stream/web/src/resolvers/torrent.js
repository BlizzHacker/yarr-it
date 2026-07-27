import { makePlayable, RENDER, TIER } from '../source.js';

/**
 * The torrent path is the one thing here that was already proven in production,
 * so it is wrapped rather than rewritten: StreamEngine keeps its swarm,
 * bridge-peer and web-seed behaviour exactly as it is and simply gains the same
 * shape as every other resolver.
 */

const INFOHASH = /^[0-9a-f]{40}$/i;

const KIND_TO_RENDER = {
  video: RENDER.VIDEO,
  audio: RENDER.AUDIO,
  image: RENDER.IMAGE,
};

export function renderForKind(kind) {
  return KIND_TO_RENDER[kind] ?? RENDER.VIDEO;
}

export function createTorrentResolver({ engine, classify = () => 'video' }) {
  return {
    name: 'torrent',
    canHandle(input) {
      return input.startsWith('magnet:') || INFOHASH.test(input.trim());
    },
    resolve(source) {
      const uri = source.uri.startsWith('magnet:')
        ? source.uri
        : `magnet:?xt=urn:btih:${source.uri.trim()}`;

      return new Promise((resolve, reject) => {
        engine.add(uri, {
          onReady: (file) => {
            const render = renderForKind(classify(file.name));
            resolve(makePlayable({
              render,
              src: file.streamURL,
              mime: '',
              tier: TIER.DIRECT,
              cleanup: () => engine.destroyTorrent(),
            }));
          },
          onError: reject,
        });
      });
    },
  };
}
