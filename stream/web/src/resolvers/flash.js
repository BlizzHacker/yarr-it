import { makePlayable, RENDER } from '../source.js';

/**
 * Flash (.swf) via Ruffle.
 *
 * Ruffle is a Flash Player reimplementation in Rust/WASM, dual MIT/Apache
 * licensed, vendored under /ruffle/ rather than pulled from a CDN so the page
 * has no third-party runtime dependency.
 *
 * It renders into a container element it creates itself, which is why this
 * resolver returns a `mount(el)` rather than a src: there is no attribute to
 * point at a URL.
 */

const RUFFLE_SRC = '/ruffle/ruffle.js';

let rufflePromise = null;

/**
 * Load Ruffle once and cache the promise. Exported so tests can inject a fake
 * without touching the DOM or the network.
 */
export function loadRuffle({ doc = document, src = RUFFLE_SRC } = {}) {
  if (globalThis.RufflePlayer) return Promise.resolve(globalThis.RufflePlayer);
  if (rufflePromise) return rufflePromise;

  rufflePromise = new Promise((resolve, reject) => {
    const tag = doc.createElement('script');
    tag.src = src;
    tag.onload = () => {
      if (globalThis.RufflePlayer) resolve(globalThis.RufflePlayer);
      else reject(new Error('ruffle loaded but exposed no RufflePlayer'));
    };
    tag.onerror = () => {
      rufflePromise = null; // let a later attempt retry
      reject(new Error('could not load ruffle'));
    };
    doc.head.append(tag);
  });
  return rufflePromise;
}

export function isSwf(input) {
  try {
    return new URL(input).pathname.toLowerCase().endsWith('.swf');
  } catch {
    return false;
  }
}

export const flashResolver = {
  name: 'flash',
  canHandle(input) {
    return typeof input === 'string' && /^https?:\/\//i.test(input) && isSwf(input);
  },
  async resolve(source) {
    let player = null;

    return makePlayable({
      render: RENDER.CANVAS,
      src: source.uri,
      mime: 'application/x-shockwave-flash',
      mount(el) {
        loadRuffle()
          .then((RufflePlayer) => {
            const ruffle = RufflePlayer.newest();
            player = ruffle.createPlayer();
            player.style.width = '100%';
            player.style.height = '100%';
            el.append(player);
            return player.load(source.uri);
          })
          .catch((err) => {
            el.textContent = `Flash player failed: ${err.message}`;
          });
      },
      cleanup() {
        // Ruffle keeps an audio context and a render loop alive until the
        // player element is destroyed; removing the node is not enough.
        try {
          player?.remove();
        } catch {
          /* already gone */
        }
        player = null;
      },
    });
  },
};
