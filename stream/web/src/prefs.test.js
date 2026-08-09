import test from 'node:test';
import assert from 'node:assert/strict';

import {
  PREFS_KEY, DOMAIN_DEFAULTS, SHARED_DEFAULTS, APPEARANCE_DEFAULTS, PLAYBACK_DEFAULTS,
  createPrefs, domainKeyFor, activeFilterCount, normalisePrefs,
  describeStored, forgetStored, formatBytes,
} from './prefs.js';

/** A storage that is not a browser's, with the shape a real one has. */
function fakeStorage(seed = {}) {
  const mem = new Map(Object.entries(seed));
  return {
    get length() { return mem.size; },
    key: (i) => [...mem.keys()][i] ?? null,
    getItem: (k) => (mem.has(k) ? mem.get(k) : null),
    setItem: (k, v) => { mem.set(k, String(v)); },
    removeItem: (k) => { mem.delete(k); },
    _raw: mem,
  };
}

/** A storage that refuses everything, like a private window. */
function hostileStorage() {
  return {
    get length() { throw new Error('denied'); },
    key() { throw new Error('denied'); },
    getItem() { throw new Error('denied'); },
    setItem() { throw new Error('denied'); },
    removeItem() { throw new Error('denied'); },
  };
}

// ------------------------------------------------------------ the domain key

test('one ticked category is that domain', () => {
  assert.equal(domainKeyFor(['music']), 'music');
  assert.equal(domainKeyFor(new Set(['games'])), 'games');
});

test('no category and several categories are both "all"', () => {
  // Several at once imply no single Newznab bucket, so the server treats them
  // as no domain at all -- and remembering numbers under a name the server does
  // not agree exists is how the two sides drift apart.
  assert.equal(domainKeyFor([]), 'all');
  assert.equal(domainKeyFor(['movies', 'tv']), 'all');
  assert.equal(domainKeyFor(null), 'all');
});

// ------------------------------------------------------------- what survives

test('a filter set in one domain does not follow you into another', () => {
  const p = createPrefs(fakeStorage());
  p.setDomain('movies', { seeders: 40, sort: 'size' });
  assert.equal(p.domain('movies').seeders, 40);
  // "At least 40 seeders" is a sentence about a swarm. An archive.org concert
  // has no seeders at all, so carrying it over would filter a catalogue by a
  // property none of it has.
  assert.equal(p.domain('music').seeders, DOMAIN_DEFAULTS.seeders);
  assert.equal(p.domain('music').sort, DOMAIN_DEFAULTS.sort);
});

test('18+, web-safe and language are shared across every domain', () => {
  const p = createPrefs(fakeStorage());
  p.setShared({ adult: true, webSafe: true, lang: 'fr' });
  // There is no per-domain copy of any of these to disagree with: somebody who
  // does not want adult results does not want them in music either.
  assert.equal(p.shared().adult, true);
  assert.equal(p.shared().webSafe, true);
  assert.equal(p.shared().lang, 'fr');
  for (const key of ['all', 'movies', 'music']) {
    assert.equal('adult' in p.domain(key), false);
    assert.equal('lang' in p.domain(key), false);
  }
  assert.equal(createPrefs(fakeStorage()).shared().adult, SHARED_DEFAULTS.adult);
});

test('everything written comes back after a reload', () => {
  const storage = fakeStorage();
  const first = createPrefs(storage);
  first.setShared({ groups: ['music'], adult: true, lang: 'any' });
  first.setDomain('music', { sort: 'recent', quality: ['flac'], source: 'instant' });
  first.setAppearance({ theme: 'midnight', covers: 'large' });
  first.setPlayback({ autoplay: false, subtitles: 'on' });

  // A second store over the same storage is exactly what a page reload is.
  const second = createPrefs(storage);
  assert.deepEqual(second.shared().groups, ['music']);
  assert.equal(second.shared().adult, true);
  assert.equal(second.shared().lang, 'any');
  assert.equal(second.domain('music').sort, 'recent');
  assert.deepEqual(second.domain('music').quality, ['flac']);
  assert.equal(second.domain('music').source, 'instant');
  assert.equal(second.appearance().theme, 'midnight');
  assert.equal(second.playback().autoplay, false);
  assert.equal(second.playback().subtitles, 'on');
});

// --------------------------------------------------------------- robustness

test('junk in storage is defaults, not an exception', () => {
  const p = createPrefs(fakeStorage({ [PREFS_KEY]: '{not json' }));
  assert.equal(p.shared().adult, false);
  assert.equal(p.domain('all').sort, DOMAIN_DEFAULTS.sort);
});

test('a storage that throws still gives a working site', () => {
  const p = createPrefs(hostileStorage());
  assert.equal(p.appearance().theme, APPEARANCE_DEFAULTS.theme);
  // Writing must not throw either: every filter change calls this.
  p.setShared({ adult: true });
  assert.equal(p.shared().adult, true, 'the choice still holds for this session');
});

test('a value that is not one of the options falls back rather than sticking', () => {
  const p = createPrefs(fakeStorage({
    [PREFS_KEY]: JSON.stringify({
      appearance: { theme: 'neon', covers: 42 },
      playback: { upscale: 'crop' },
      domains: { movies: { sort: 'drop tables', source: 'magic', seeders: -3 } },
    }),
  }));
  assert.equal(p.appearance().theme, APPEARANCE_DEFAULTS.theme);
  assert.equal(p.appearance().covers, APPEARANCE_DEFAULTS.covers);
  assert.equal(p.playback().upscale, PLAYBACK_DEFAULTS.upscale);
  assert.equal(p.domain('movies').sort, DOMAIN_DEFAULTS.sort);
  assert.equal(p.domain('movies').source, '');
  assert.equal(p.domain('movies').seeders, DOMAIN_DEFAULTS.seeders);
});

test('one unknown field does not throw away the rest of the record', () => {
  // A record written by a newer build is not a corrupt record, and resetting
  // somebody's whole setup over one unrecognised key is the worst possible
  // reading of it.
  const p = createPrefs(fakeStorage({
    [PREFS_KEY]: JSON.stringify({
      shared: { adult: true, somethingNew: 'hello' },
      appearance: { theme: 'midnight' },
    }),
  }));
  assert.equal(p.shared().adult, true);
  assert.equal(p.appearance().theme, 'midnight');
});

test('a language code this build does not list is kept, not rewritten', () => {
  // LANGUAGES decides what the menu offers. The server understands more than
  // the menu shows, and quietly replacing a code we merely failed to recognise
  // would be overruling a choice rather than reading it.
  const p = createPrefs(fakeStorage({
    [PREFS_KEY]: JSON.stringify({ shared: { lang: 'yi' } }),
  }));
  assert.equal(p.shared().lang, 'yi');
});

test('a size box only ever holds digits', () => {
  const p = createPrefs(fakeStorage());
  p.setDomain('all', { minSize: '700', maxSize: 'DROP TABLE' });
  assert.equal(p.domain('all').minSize, '700');
  assert.equal(p.domain('all').maxSize, '');
});

test('a domain name that is not a name is ignored', () => {
  const p = createPrefs(fakeStorage({
    [PREFS_KEY]: JSON.stringify({ domains: { 'movies': { seeders: 5 }, '../etc': { seeders: 9 } } }),
  }));
  assert.equal(p.domain('movies').seeders, 5);
  assert.equal(p.snapshot().domains['../etc'], undefined);
});

test('a snapshot cannot be used to edit storage behind the store', () => {
  const p = createPrefs(fakeStorage());
  const snap = p.snapshot();
  snap.shared.adult = true;
  assert.equal(p.shared().adult, false);
});

// ------------------------------------------------------------------ clearing

test('clearing filters leaves the theme alone', () => {
  const p = createPrefs(fakeStorage());
  p.setShared({ adult: true, groups: ['games'] });
  p.setDomain('games', { seeders: 12 });
  p.setAppearance({ theme: 'light' });

  p.clearFilters();

  assert.equal(p.shared().adult, false);
  assert.deepEqual(p.shared().groups, []);
  assert.equal(p.domain('games').seeders, DOMAIN_DEFAULTS.seeders);
  // "Clear my filters" is a statement about a search. Answering it by also
  // undoing a theme somebody deliberately chose is a larger thing than the one
  // they asked for.
  assert.equal(p.appearance().theme, 'light');
});

test('clearing everything removes the key rather than storing defaults', () => {
  const storage = fakeStorage();
  const p = createPrefs(storage);
  p.setAppearance({ theme: 'light' });
  assert.ok(storage.getItem(PREFS_KEY));
  p.clearAll();
  assert.equal(storage.getItem(PREFS_KEY), null);
  assert.equal(p.appearance().theme, APPEARANCE_DEFAULTS.theme);
});

// ------------------------------------------------------------- how many are on

test('nothing set counts as no filters', () => {
  assert.equal(activeFilterCount({
    ...DOMAIN_DEFAULTS, ...SHARED_DEFAULTS,
    quality: new Set(), codec: new Set(), groups: new Set(),
  }), 0);
});

test('every filter that is doing something is counted once', () => {
  const n = activeFilterCount({
    groups: new Set(['movies', 'tv']), // one decision, however many chips
    adult: true,
    webSafe: true,
    lang: 'fr',
    seeders: 40,
    minSize: '700',
    maxSize: '',
    quality: new Set(['1080p']),
    codec: new Set(),
    source: 'instant',
    sort: 'recent',
  });
  assert.equal(n, 9);
});

test('the count is what makes a forgotten filter findable', () => {
  // The single worst state for this site is a filter somebody set last week,
  // cannot see, and reads as the catalogue having shrunk.
  assert.equal(activeFilterCount({ ...DOMAIN_DEFAULTS, seeders: 200 }), 1);
  assert.equal(activeFilterCount({ ...DOMAIN_DEFAULTS, seeders: 1 }), 0);
});

// -------------------------------------------------------------- stored data

test('stored data is grouped into things a person recognises', () => {
  const storage = fakeStorage({
    [PREFS_KEY]: '{"v":1}',
    yarrit_services: '[{"id":"radarr-1"}]',
    'yarrit.player': 'emulatorjs',
    'yarrit.com/bridge/iptv/supermariosunshine64stars': 'x'.repeat(2048),
    'privacy-ack': '1',
    'some.other.site': 'not ours',
  });
  const rows = describeStored(storage);
  const byId = Object.fromEntries(rows.map((r) => [r.id, r]));

  assert.ok(byId.prefs, 'preferences');
  assert.ok(byId.services, 'services');
  assert.ok(byId.saves, 'game saves');
  assert.equal(byId.saves.bytes > 2048, true, 'a save is measured, not just listed');
  assert.equal(rows.some((r) => r.keys.includes('some.other.site')), false,
    'a key this site never wrote is not ours to list or delete');
});

test('forgetting one kind leaves the others, and never eats a game save', () => {
  const storage = fakeStorage({
    [PREFS_KEY]: '{"v":1}',
    'yarrit.com/bridge/iptv/mario': 'save-bytes',
    'privacy-ack': '1',
  });
  const removed = forgetStored(['prefs', 'notices'], storage);
  assert.equal(removed, 2);
  assert.equal(storage.getItem(PREFS_KEY), null);
  assert.equal(storage.getItem('privacy-ack'), null);
  assert.equal(storage.getItem('yarrit.com/bridge/iptv/mario'), 'save-bytes');
});

test('a storage that cannot be read reports nothing rather than throwing', () => {
  assert.deepEqual(describeStored(hostileStorage()), []);
  assert.equal(forgetStored(['prefs'], hostileStorage()), 0);
});

test('sizes are readable without a calculator', () => {
  assert.equal(formatBytes(1), '1 byte');
  assert.equal(formatBytes(900), '900 bytes');
  assert.equal(formatBytes(2048), '2.0 KB');
  assert.equal(formatBytes(3 * 1024 * 1024), '3.0 MB');
});

test('normalisePrefs is total: anything at all produces a usable record', () => {
  for (const input of [null, undefined, 0, 'string', [], { domains: 'nope' }]) {
    const p = normalisePrefs(input);
    assert.equal(typeof p.shared, 'object');
    assert.equal(typeof p.domains, 'object');
    assert.equal(p.appearance.theme, APPEARANCE_DEFAULTS.theme);
  }
});
