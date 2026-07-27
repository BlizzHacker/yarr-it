/**
 * Renders a Playable. Knows nothing about torrents, HTTP or playlists -- it
 * switches on `render` and attaches a source to the matching element. That is
 * the whole point of the descriptor: Ruffle and EmulatorJS will arrive as
 * `canvas` and need no change here.
 */

export function renderPlayable(playable, elements) {
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
    el.removeAttribute('src');
  }
}
