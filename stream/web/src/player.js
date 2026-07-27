/**
 * Renders a Playable. Knows nothing about torrents, HTTP or playlists -- it
 * switches on `render` and attaches a source to the matching element. That is
 * the whole point of the descriptor: Ruffle and EmulatorJS will arrive as
 * `canvas` and need no change here.
 *
 * Ownership note: this module only manages elements (attaching/detaching
 * sources), it never touches a Playable's `cleanup()`. The caller is
 * responsible for calling `cleanup()` on the outgoing Playable before
 * rendering a new one -- neither `renderPlayable` nor `detachAll` receives
 * the outgoing Playable, so neither one can do it for you.
 */

import { RENDER } from './source.js';

const RENDER_KINDS = new Set(Object.values(RENDER));

export function renderPlayable(playable, elements) {
  if (!RENDER_KINDS.has(playable.render)) {
    throw new Error(`unknown render kind: ${playable.render}`);
  }

  const el = elements[playable.render];
  if (!el) throw new Error(`no element for render kind: ${playable.render}`);

  detachAll(elements);
  el.src = playable.src;
  el.hidden = false;
  return el;
}

export function detachAll(elements) {
  for (const el of Object.values(elements)) {
    if (!el) continue;
    el.hidden = true;
    // removeAttribute('src') alone does not stop a real <video>/<audio>
    // element: it can keep buffering/playing an in-flight fetch against the
    // old source until the element is explicitly told to stop. pause() halts
    // playback and load() forces the element to abandon the current network
    // request and reset, which is what actually releases the media. Do not
    // "simplify" this back down to just removeAttribute.
    if (typeof el.pause === 'function') el.pause();
    el.removeAttribute('src');
    if (typeof el.load === 'function') el.load();
  }
}
