/**
 * Browses a Collection. A playlist is frequently thousands of channels, so it
 * is grouped by category rather than rendered as one flat list.
 */

export function groupByCategory(collection) {
  const groups = new Map();
  for (const source of collection.sources) {
    const key = source.meta?.group || 'Ungrouped';
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(source);
  }
  return groups;
}

export function renderLibrary(collection, { mount, onPick }) {
  mount.textContent = '';
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
      btn.addEventListener('click', () => onPick(source));
      row.append(btn);
    }
    mount.append(row);
  }
  mount.hidden = false;
}
