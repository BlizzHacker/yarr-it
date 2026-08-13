import test from 'node:test';
import assert from 'node:assert/strict';
import { filtersFromSearchURL, paramsForSearch } from './search-route.js';

test('a bare shared query cannot inherit a hidden saved category', () => {
  const f = filtersFromSearchURL(new URLSearchParams('q=The+Odyssey+2026'));
  assert.deepEqual(f.groups, []);
  assert.deepEqual(f.providers, []);
  assert.equal(f.source, '');
  assert.equal(f.seeders, 1);
});

test('an explicitly filtered search round-trips through its URL', () => {
  const original = {
    sort: 'relevance', seeders: 0, minSize: '', maxSize: '',
    quality: new Set(['1080p']), codec: new Set(['x264']),
    providers: new Set(['archive.org']), groups: new Set(['movies', 'comics']),
    systems: new Set(), webSafe: true, source: '', adult: false, lang: 'en',
  };
  const p = paramsForSearch('Odyssey', original);
  const restored = filtersFromSearchURL(p);
  assert.deepEqual(restored.groups, ['movies', 'comics']);
  assert.deepEqual(restored.providers, ['archive.org']);
  assert.deepEqual(restored.quality, ['1080p']);
  assert.equal(restored.seeders, 0);
  assert.equal(restored.webSafe, true);
  assert.equal(restored.lang, 'en');
});
