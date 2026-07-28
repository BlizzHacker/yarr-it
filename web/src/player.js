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
  el.hidden = false;
  // Canvas renders boot a WASM player into the element rather than pointing an
  // attribute at a URL.
  if (playable.mount) playable.mount(el);
  else el.src = playable.src;
  return el;
}

/**
 * Release every element.
 *
 * Three different kinds of thing need three different teardowns, and getting
 * any of them wrong leaves media running invisibly:
 *   - <video>/<audio>: removing src is NOT enough to stop buffering; it needs
 *     pause() then load().
 *   - <iframe>: removing src does not navigate it away, so a YouTube embed
 *     keeps playing audio under display:none. It has to be sent to about:blank.
 *   - a canvas container: the WASM player is a child element, so the container
 *     has to be emptied.
 */
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
    if (typeof el.pause === 'function') {
      el.pause();
    } else if (typeof el.replaceChildren === 'function' && !('src' in el)) {
      // A canvas container holds a WASM player (Ruffle, EmulatorJS) as a child
      // element rather than a src. Emptying it is what stops the emulator --
      // there is no attribute to clear, and leaving it would keep a game
      // running with sound under display:none.
      el.replaceChildren();
      continue;
    } else {
      // An element with no pause() -- an <iframe> embed (YouTube/Vimeo) is
      // the case that matters here -- cannot be stopped by
      // removeAttribute('src') alone. Removing the attribute does not
      // navigate the frame away: its document (and any audio/video it is
      // playing) keeps running until something else loads in its place.
      // about:blank is that something else.
      el.src = 'about:blank';
    }
    el.removeAttribute('src');
    if (typeof el.load === 'function') el.load();
  }
}
