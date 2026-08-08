import test from 'node:test';
import assert from 'node:assert/strict';

import {
  normaliseAddonURL, isConfiguredAddonURL, addonHost,
  describeAddon, addonWarnings, addonIsUsable, moveAddon,
  createAddonAPI, renderAddons, healthLabel,
} from './addons.js';
import { HEALTH_STATES } from './schema.js';

// A view exactly as /api/addons sends it, so these tests pin the contract
// rather than a convenient shape.
const cinemeta = {
  id: 'com.linvo.cinemeta',
  name: 'Cinemeta',
  version: '3.0.14',
  safeUrl: 'https://v3-cinemeta.strem.io/manifest.json',
  configured: false,
  enabled: true,
  order: 0,
  domains: ['video'],
  roles: ['discovery'],
  capabilities: ['health', 'search', 'details'],
  resources: ['addon_catalog', 'catalog', 'meta'],
  catalogs: 2,
  searchable: true,
  health: { state: 'healthy', version: '3.0.14' },
};

const torrentio = {
  id: 'com.stremio.torrentio.addon',
  name: 'Torrentio',
  version: '0.0.15',
  safeUrl: 'https://torrentio.strem.fun/…/manifest.json',
  configured: true,
  enabled: true,
  order: 1,
  domains: ['video'],
  roles: ['stream', 'indexer'],
  capabilities: ['health', 'stream'],
  resources: ['stream'],
  catalogs: 0,
  searchable: false,
  p2p: true,
  health: { state: 'healthy' },
};

// -------------------------------------------------------------------- the URL

test('an Install button’s stremio:// link is accepted, not rejected', () => {
  // Confirmed on the wire: Torrentio’s configure page and OpenSubtitles’
  // landing page both emit stremio:// hrefs, so this is the string a person
  // actually has on their clipboard after clicking Install.
  const { url, error } = normaliseAddonURL('stremio://v3-cinemeta.strem.io/manifest.json');
  assert.equal(error, '');
  assert.equal(url, 'https://v3-cinemeta.strem.io/manifest.json');
});

test('the three shapes people actually paste all land on the same address', () => {
  for (const raw of [
    'https://v3-cinemeta.strem.io/manifest.json',
    'https://v3-cinemeta.strem.io/manifest.json/',
    'https://v3-cinemeta.strem.io',
    'v3-cinemeta.strem.io',
    '  v3-cinemeta.strem.io/manifest.json  ',
    'stremio://v3-cinemeta.strem.io',
  ]) {
    const { url, error } = normaliseAddonURL(raw);
    assert.equal(error, '', `${raw} was refused: ${error}`);
    assert.equal(url, 'https://v3-cinemeta.strem.io/manifest.json', `from ${raw}`);
  }
});

test('a configured addon keeps its settings path, because that is the addon', () => {
  // Losing this segment installs a *different* addon: the same host with no
  // providers and no debrid account, which then answers with nothing and looks
  // broken.
  const { url } = normaliseAddonURL('https://torrentio.strem.fun/providers=yts/manifest.json');
  assert.equal(url, 'https://torrentio.strem.fun/providers=yts/manifest.json');
});

test('host:port is not read as an unknown protocol', () => {
  // "nas:11470" parses as the scheme "nas" and is the single most common thing
  // a self-hoster types.
  const { url, error } = normaliseAddonURL('nas:11470/manifest.json');
  assert.equal(error, '');
  assert.equal(url, 'https://nas:11470/manifest.json');
});

test('a query string or fragment is dropped rather than stored as identity', () => {
  const { url } = normaliseAddonURL('https://x.example/manifest.json?utm_source=reddit#top');
  assert.equal(url, 'https://x.example/manifest.json');
});

test('each refusal says something the person can act on', () => {
  const cases = [
    ['', 'Paste'],
    ['ftp://x.example/manifest.json', 'http'],
    ['javascript:alert(1)', 'http'],
    ['ipfs://bafy/manifest.json', 'IPFS'],
    ['https://user:secret@x.example/manifest.json', 'username'],
  ];
  for (const [raw, want] of cases) {
    const { url, error } = normaliseAddonURL(raw);
    assert.equal(url, '', `${raw} was accepted`);
    assert.ok(error.includes(want), `${raw} said "${error}", which does not mention ${want}`);
  }
});

test('a rejected scheme never becomes a plausible host', () => {
  // "https://" + "ftp://x.example" parses happily, with the hostname "ftp".
  const { url } = normaliseAddonURL('ftp://x.example/manifest.json');
  assert.equal(url, '');
});

test('a configured URL is recognised so the UI can warn about the key in it', () => {
  assert.equal(isConfiguredAddonURL('https://torrentio.strem.fun/providers=yts/manifest.json'), true);
  assert.equal(isConfiguredAddonURL('https://v3-cinemeta.strem.io/manifest.json'), false);
  assert.equal(isConfiguredAddonURL('nonsense'), false);
});

test('only the host is ever pulled out for display', () => {
  assert.equal(addonHost('https://torrentio.strem.fun/providers=yts/manifest.json'), 'torrentio.strem.fun');
  assert.equal(addonHost('rubbish'), '');
});

// --------------------------------------------------------------- describing it

test('an addon is described by what it can do, not by its own marketing', () => {
  assert.equal(describeAddon(cinemeta), 'search, details for Video');
  assert.equal(describeAddon(torrentio), 'streams for Video');
});

test('a browse-only catalogue says browse rather than claiming search', () => {
  // The distinction is not cosmetic: a feed-only catalog answers a search with
  // its unfiltered feed, which looks like a working search returning nonsense.
  const feed = { ...cinemeta, searchable: false, capabilities: ['health', 'details'], roles: ['discovery'] };
  assert.ok(describeAddon(feed).startsWith('browse'));
  assert.ok(!describeAddon(feed).includes('search'));
});

test('an addon nothing here can use says so instead of looking healthy and empty', () => {
  const podcasts = {
    id: 'p', name: 'Podcatcher', domains: [], roles: ['discovery'],
    capabilities: ['health'], resources: ['catalog'], unsupportedTypes: ['podcast'],
  };
  assert.equal(describeAddon(podcasts), 'Nothing this version can use.');
});

test('subtitle addons are described, not left blank', () => {
  const subs = {
    id: 's', name: 'OpenSubtitles v3', domains: ['video'],
    roles: [], capabilities: ['health'], resources: ['subtitles'],
  };
  assert.ok(describeAddon(subs).includes('subtitles'));
});

// ------------------------------------------------------------------- warnings

test('a configured URL is flagged as something not to share', () => {
  const w = addonWarnings(torrentio).find((x) => x.kind === 'configured');
  assert.ok(w, 'no warning for a URL that can contain a paid account key');
  assert.ok(/share/i.test(w.text));
});

test('p2p is warned about where the decision is made, not buried in a manifest', () => {
  const w = addonWarnings(torrentio).find((x) => x.kind === 'p2p');
  assert.ok(w);
  assert.ok(/IP address/i.test(w.text), 'the warning must say what is actually exposed');
});

test('unsupported content types are named rather than silently dropped', () => {
  // An addon whose items never appear looks exactly like a broken addon and
  // gets reported as one.
  const a = { ...cinemeta, unsupportedTypes: ['podcast', 'audiobook_stream'] };
  const w = addonWarnings(a).find((x) => x.kind === 'unsupported');
  assert.ok(w);
  assert.ok(w.text.includes('podcast'));
  assert.ok(/still works/i.test(w.text), 'a partial complaint must not read as total failure');
});

test('a plain healthy addon has nothing to warn about', () => {
  assert.deepEqual(addonWarnings(cinemeta), []);
});

test('degraded is still usable, because a partial complaint is not a failure', () => {
  assert.equal(addonIsUsable({ health: { state: 'healthy' } }), true);
  assert.equal(addonIsUsable({ health: { state: 'degraded' } }), true);
  for (const s of ['unreachable', 'auth_failed', 'incompatible', 'not_configured']) {
    assert.equal(addonIsUsable({ health: { state: s } }), false, `${s} should not be usable`);
  }
});

test('every health state the server can send has a sentence here', () => {
  // Two lists that drift is how a state arrives on screen as "undefined".
  for (const s of HEALTH_STATES) {
    assert.ok(healthLabel(s), `no label for ${s}`);
    assert.notEqual(healthLabel(s), s, `${s} is being shown raw`);
  }
});

// ------------------------------------------------------------------- ordering

test('moving an addon returns the new id order and moves nothing else', () => {
  const rows = [{ id: 'a' }, { id: 'b' }, { id: 'c' }];
  assert.deepEqual(moveAddon(rows, 'c', -1), ['a', 'c', 'b']);
  assert.deepEqual(moveAddon(rows, 'a', 1), ['b', 'a', 'c']);
});

test('moving past either end is a no-op, not a wrap-around', () => {
  const rows = [{ id: 'a' }, { id: 'b' }];
  assert.deepEqual(moveAddon(rows, 'a', -1), ['a', 'b']);
  assert.deepEqual(moveAddon(rows, 'b', 1), ['a', 'b']);
});

test('moving something that is not there leaves the order alone', () => {
  const rows = [{ id: 'a' }, { id: 'b' }];
  assert.deepEqual(moveAddon(rows, 'ghost', 1), ['a', 'b']);
  assert.deepEqual(moveAddon([], 'a', 1), []);
});

// --------------------------------------------------------------- the API client

function fakeFetch(routes) {
  const calls = [];
  const impl = async (url, options = {}) => {
    calls.push({ url, options });
    const key = Object.keys(routes).find((k) => url.startsWith(k));
    if (!key) throw new Error(`no route for ${url}`);
    const r = routes[key];
    if (typeof r === 'function') return r(url, options);
    return {
      ok: (r.status || 200) < 400,
      status: r.status || 200,
      json: async () => r.body,
    };
  };
  impl.calls = calls;
  return impl;
}

test('the list comes back with the health the server reported', async () => {
  const fetchImpl = fakeFetch({
    '/api/addons': { body: { addons: [cinemeta, torrentio], domains: ['video'], limits: { maxInstalled: 64 } } },
  });
  const api = createAddonAPI({ fetchImpl });
  const r = await api.list();
  assert.equal(r.ok, true);
  assert.equal(r.addons.length, 2);
  assert.deepEqual(r.domains, ['video']);
  assert.equal(r.limits.maxInstalled, 64);
});

test('a session is sent same-origin and never to another instance', async () => {
  const same = fakeFetch({ '/api/addons': { body: { addons: [] } } });
  await createAddonAPI({ fetchImpl: same }).list();
  assert.equal(same.calls[0].options.credentials, 'same-origin');

  // Pointed at somebody else's server, the cookie must not go with it.
  const other = fakeFetch({ 'https://friend.example/api/addons': { body: { addons: [] } } });
  await createAddonAPI({ fetchImpl: other, base: 'https://friend.example' }).list();
  assert.equal(other.calls[0].options.credentials, 'omit');
});

test('a bad address is refused before a request is made', async () => {
  const fetchImpl = fakeFetch({ '/api/addons': { body: {} } });
  const r = await createAddonAPI({ fetchImpl }).add('ftp://x.example/manifest.json');
  assert.equal(r.ok, false);
  assert.ok(r.error.includes('http'));
  assert.equal(fetchImpl.calls.length, 0, 'the server was contacted for an address that could never work');
});

test('adding normalises first, so an Install link works without a round trip to fail', async () => {
  const fetchImpl = fakeFetch({
    '/api/addons': { body: { addon: cinemeta } },
  });
  const r = await createAddonAPI({ fetchImpl }).add('stremio://v3-cinemeta.strem.io');
  assert.equal(r.ok, true);
  assert.equal(r.addon.id, 'com.linvo.cinemeta');
  const sent = JSON.parse(fetchImpl.calls[0].options.body);
  assert.equal(sent.url, 'https://v3-cinemeta.strem.io/manifest.json');
});

test('the server’s own refusal is shown, not replaced with a generic failure', async () => {
  const fetchImpl = fakeFetch({
    '/api/addons': { status: 400, body: { error: 'Port 6379 is Redis, not a web server. No addon is served there.' } },
  });
  const r = await createAddonAPI({ fetchImpl }).add('http://192.168.1.5:6379/manifest.json');
  assert.equal(r.ok, false);
  assert.ok(r.error.includes('Redis'), `lost the reason: ${r.error}`);
});

test('a signed-out caller is told to sign in rather than shown an error code', async () => {
  const fetchImpl = fakeFetch({ '/api/addons': { status: 401, body: { error: 'sign-in required' } } });
  const r = await createAddonAPI({ fetchImpl }).list();
  assert.equal(r.ok, false);
  assert.equal(r.needsSignIn, true);
  assert.deepEqual(r.addons, []);
});

test('an unreachable Yarr.It server is not reported as a broken addon', async () => {
  const fetchImpl = async () => { throw new TypeError('Failed to fetch'); };
  const r = await createAddonAPI({ fetchImpl }).list();
  assert.equal(r.ok, false);
  assert.ok(/Yarr\.It server/.test(r.error), `wrong culprit named: ${r.error}`);
});

test('search carries the addons that could not answer', async () => {
  // Without this, a short list and a broken addon look identical, and "no
  // results" is the most misleading thing a search can say.
  const fetchImpl = fakeFetch({
    '/api/addons/search': {
      body: {
        items: [{ canonicalId: 'addon:a:movie:tt1', title: 'Found', domain: 'video' }],
        failed: [{ id: 'bad', name: 'Bad', detail: 'That addon did not answer in time.' }],
      },
    },
  });
  const r = await createAddonAPI({ fetchImpl }).search('dune', 'movies');
  assert.equal(r.ok, true);
  assert.equal(r.items.length, 1);
  assert.equal(r.failed[0].name, 'Bad');
  // "movies" must reach the server as the canonical "video", which is the exact
  // mismatch schema.json exists to prevent.
  assert.ok(fetchImpl.calls[0].url.includes('domain=video'), fetchImpl.calls[0].url);
});

test('an unknown domain is dropped rather than sent as a filter nothing satisfies', async () => {
  const fetchImpl = fakeFetch({ '/api/addons/search': { body: { items: [] } } });
  await createAddonAPI({ fetchImpl }).search('x', 'sculpture');
  assert.ok(!fetchImpl.calls[0].url.includes('domain='), fetchImpl.calls[0].url);
});

test('toggling and reordering post exactly what the server expects', async () => {
  const fetchImpl = fakeFetch({
    '/api/addons/enabled': { body: { id: 'a', enabled: false } },
    '/api/addons/order': { body: { order: ['b', 'a'] } },
    '/api/addons/remove': { body: { removed: 'a' } },
  });
  const api = createAddonAPI({ fetchImpl });
  await api.setEnabled('a', false);
  await api.reorder(['b', 'a']);
  await api.remove('a');

  assert.deepEqual(JSON.parse(fetchImpl.calls[0].options.body), { id: 'a', enabled: false });
  assert.deepEqual(JSON.parse(fetchImpl.calls[1].options.body), { ids: ['b', 'a'] });
  assert.deepEqual(JSON.parse(fetchImpl.calls[2].options.body), { id: 'a' });
});

// --------------------------------------------------------------------- the view
//
// No jsdom (project constraint). These fakes cover only the DOM surface
// renderAddons touches.

function fakeEl(tag) {
  const node = {
    tagName: tag, className: '', textContent: '', type: '', disabled: false,
    dataset: {}, children: [], parent: null, listeners: {},
    append(...kids) { for (const k of kids) { k.parent = node; node.children.push(k); } },
    replaceChildren(...kids) { node.children = []; node.append(...kids); },
    addEventListener(ev, fn) { node.listeners[ev] = fn; },
  };
  return node;
}

function withFakeDom(fn) {
  const original = globalThis.document;
  globalThis.document = { createElement: fakeEl };
  try {
    return fn();
  } finally {
    globalThis.document = original;
  }
}

/** Every text node anywhere under a row, flattened. */
function textOf(node) {
  const parts = node.textContent ? [node.textContent] : [];
  for (const c of node.children) parts.push(textOf(c));
  return parts.join(' ');
}

function findByAction(node, action) {
  if (node.dataset?.action === action) return node;
  for (const c of node.children) {
    const hit = findByAction(c, action);
    if (hit) return hit;
  }
  return null;
}

test('an empty list explains what to do instead of showing nothing', () => {
  withFakeDom(() => {
    const host = fakeEl('div');
    renderAddons(host, { addons: [] });
    assert.equal(host.children.length, 1);
    assert.ok(/Install button/i.test(textOf(host)));
  });
});

test('each row states what the addon does and how it is', () => {
  withFakeDom(() => {
    const host = fakeEl('div');
    renderAddons(host, { addons: [cinemeta, torrentio] });
    assert.equal(host.children.length, 2);

    const row = host.children[0];
    const text = textOf(row);
    assert.ok(text.includes('Cinemeta'));
    assert.ok(text.includes('Healthy'), 'the health state is not shown in words');
    assert.ok(text.includes('search'), 'the row does not say what the addon can do');
    assert.ok(text.includes('v3-cinemeta.strem.io'), 'the host is not shown');
  });
});

test('a sick addon shows the state and the detail, not a colour', () => {
  withFakeDom(() => {
    const host = fakeEl('div');
    renderAddons(host, {
      addons: [{
        ...cinemeta,
        health: { state: 'auth_failed', detail: 'This addon refused the request.' },
      }],
    });
    const text = textOf(host);
    assert.ok(text.includes('Key rejected'), 'the state was collapsed into something vaguer');
    assert.ok(text.includes('refused the request'), 'the detail that says what to do was dropped');
    assert.equal(host.children[0].dataset.health, 'auth_failed');
  });
});

test('the key-in-the-URL and swarm warnings reach the row', () => {
  withFakeDom(() => {
    const host = fakeEl('div');
    renderAddons(host, { addons: [torrentio] });
    const text = textOf(host);
    assert.ok(/Do not share/i.test(text), 'no warning that the address contains settings');
    assert.ok(/IP address/i.test(text), 'no warning that p2p exposes an IP');
  });
});

test('the full addon URL is never rendered into the page', () => {
  withFakeDom(() => {
    const host = fakeEl('div');
    renderAddons(host, {
      addons: [{
        ...torrentio,
        url: 'https://torrentio.strem.fun/realdebrid=SUPERSECRET/manifest.json',
        safeUrl: 'https://torrentio.strem.fun/…/manifest.json',
      }],
    });
    assert.ok(!textOf(host).includes('SUPERSECRET'), 'a debrid key was printed on screen');
  });
});

test('the ends of the list cannot be moved past', () => {
  withFakeDom(() => {
    const host = fakeEl('div');
    renderAddons(host, { addons: [cinemeta, torrentio] });
    assert.equal(findByAction(host.children[0], 'up').disabled, true);
    assert.equal(findByAction(host.children[0], 'down').disabled, false);
    assert.equal(findByAction(host.children[1], 'down').disabled, true);
  });
});

test('the buttons call back with the id and the intended new state', () => {
  withFakeDom(() => {
    const host = fakeEl('div');
    const seen = [];
    renderAddons(host, {
      addons: [cinemeta, torrentio],
      handlers: {
        onToggle: (id, on) => seen.push(['toggle', id, on]),
        onMove: (id, d) => seen.push(['move', id, d]),
        onRemove: (id) => seen.push(['remove', id]),
      },
    });
    findByAction(host.children[0], 'toggle').listeners.click();
    findByAction(host.children[1], 'up').listeners.click();
    findByAction(host.children[1], 'remove').listeners.click();

    assert.deepEqual(seen, [
      ['toggle', 'com.linvo.cinemeta', false],
      ['move', 'com.stremio.torrentio.addon', -1],
      ['remove', 'com.stremio.torrentio.addon'],
    ]);
  });
});

test('a turned-off addon is marked so, and still offers to be turned on', () => {
  withFakeDom(() => {
    const host = fakeEl('div');
    renderAddons(host, { addons: [{ ...cinemeta, enabled: false }] });
    assert.equal(host.children[0].dataset.disabled, 'true');
    assert.equal(findByAction(host.children[0], 'toggle').textContent, 'Turn on');
  });
});

test('a missing handler is not a crash', () => {
  // The settings screen renders before its handlers are wired during startup.
  withFakeDom(() => {
    const host = fakeEl('div');
    renderAddons(host, { addons: [cinemeta] });
    findByAction(host.children[0], 'toggle').listeners.click();
    findByAction(host.children[0], 'remove').listeners.click();
  });
});

test('hitting the ceiling says so where someone is about to add another', () => {
  withFakeDom(() => {
    const host = fakeEl('div');
    const many = Array.from({ length: 3 }, (_, i) => ({ ...cinemeta, id: `a${i}` }));
    renderAddons(host, { addons: many, limits: { maxInstalled: 3 } });
    assert.ok(/maximum of 3/.test(textOf(host)));
  });
});
