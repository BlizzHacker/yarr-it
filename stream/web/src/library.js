/**
 * Browses a Collection. A playlist is frequently thousands of channels, so it
 * is grouped by category rather than rendered as one flat list.
 */

import { isCollection } from './source.js';

// This module is where untrusted third-party playlist data (an .m3u fetched
// from wherever the user pointed it) first reaches the UI layer. A
// Playable, undefined, or malformed object must fail loudly here rather than
// blow up mid-loop with a confusing "Cannot read properties of undefined".
export function groupByCategory(collection) {
  if (!isCollection(collection)) {
    const kind = collection?.type ?? typeof collection;
    throw new TypeError(`groupByCategory expects a Collection, got ${kind}`);
  }
  const groups = new Map();
  for (const source of collection.sources) {
    const key = source.meta?.group || 'Ungrouped';
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(source);
  }
  return groups;
}

// One delegated listener per mount, not one per button: a 10k-channel IPTV
// playlist would otherwise attach 10k closures in a single synchronous pass.
// Keyed by mount so repeated renderLibrary() calls on the same mount reuse
// the existing listener instead of stacking a new one on every render.
const mountState = new WeakMap();

export function renderLibrary(collection, { mount, onPick }) {
  mount.textContent = '';
  const flatSources = [];

  let state = mountState.get(mount);
  if (!state) {
    state = {};
    state.listener = (event) => {
      const btn = event.target.closest?.('.lib-item');
      if (!btn) return;
      const source = state.flatSources[Number(btn.dataset.index)];
      if (source) state.onPick(source);
    };
    mount.addEventListener('click', state.listener);
    mountState.set(mount, state);
  }
  state.flatSources = flatSources;
  state.onPick = onPick;

  for (const [group, sources] of groupByCategory(collection)) {
    const heading = document.createElement('h3');
    heading.className = 'lib-group';
    heading.textContent = `${group} · ${sources.length}`;
    mount.append(heading);

    const row = document.createElement('div');
    row.className = 'lib-row';
    for (const source of sources) {
      const btn = document.createElement('button');
      btn.className = 'lib-item';
      btn.type = 'button';
      btn.textContent = source.meta?.title || source.uri;
      btn.dataset.index = String(flatSources.length);
      flatSources.push(source);
      row.append(btn);
    }
    mount.append(row);
  }
  mount.hidden = false;
}
