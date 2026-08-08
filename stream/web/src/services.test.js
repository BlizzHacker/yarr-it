import test from 'node:test';
import assert from 'node:assert/strict';

import {
  SERVICE_TYPES, HEALTH_LABELS, HEALTH_STATES, allServiceTypes,
  normaliseServiceURL, createServiceStore, buildProbeRequest, classifyProbe,
  probeService, routeAdvice, describeService,
} from './services.js';
import { TRANSPORT, BLOCKED } from './transport.js';

/** A localStorage that is not the browser's, so these tests need no DOM. */
function fakeStorage(seed = null) {
  const mem = new Map();
  if (seed != null) mem.set('yarrit_services', seed);
  return {
    getItem: (k) => (mem.has(k) ? mem.get(k) : null),
    setItem: (k, v) => { mem.set(k, String(v)); },
    removeItem: (k) => { mem.delete(k); },
    raw: () => mem.get('yarrit_services'),
  };
}

const radarr = { type: 'radarr', name: 'Radarr', url: 'http://192.168.0.26', key: 'abc' };

// --------------------------------------------------------------- the store --

test('a service survives a round trip through storage', () => {
  const s = fakeStorage();
  const store = createServiceStore(s);
  const added = store.add(radarr);
  assert.ok(added.ok);

  // A second store over the same storage is what a page reload actually is.
  const reopened = createServiceStore(s).list();
  assert.equal(reopened.length, 1);
  assert.equal(reopened[0].id, added.service.id);
  assert.equal(reopened[0].url, 'http://192.168.0.26');
  assert.equal(reopened[0].key, 'abc');
  assert.equal(reopened[0].enabled, true);
});

test('two instances of one type is the normal case, not an error', () => {
  // Radarr and Radarr-4K, Sonarr-TV and Sonarr-Anime. Anything that dedupes by
  // type quietly deletes half of a real setup.
  const store = createServiceStore(fakeStorage());
  const a = store.add({ ...radarr, name: 'Radarr' });
  const b = store.add({ ...radarr, name: 'Radarr 4K', url: 'http://192.168.0.26:7879' });
  assert.ok(a.ok && b.ok);
  assert.notEqual(a.service.id, b.service.id);

  const rows = store.list();
  assert.equal(rows.length, 2);
  assert.deepEqual(rows.map((r) => r.name), ['Radarr', 'Radarr 4K']);

  // Removing one leaves the other. This is the assertion that a shared id would
  // have failed.
  store.remove(a.service.id);
  const left = store.list();
  assert.equal(left.length, 1);
  assert.equal(left[0].name, 'Radarr 4K');
});

test('ids stay unique even when the clock and the dice both repeat', () => {
  // Two adds in the same millisecond is what pasting in a setup looks like.
  const store = createServiceStore(fakeStorage());
  const frozen = () => 0.5;
  const a = store.add(radarr, frozen);
  const b = store.add(radarr, frozen);
  assert.notEqual(a.service.id, b.service.id);
  assert.equal(store.list().length, 2);
});

test('the id does not move when anything else about the service does', () => {
  const store = createServiceStore(fakeStorage());
  const { service } = store.add(radarr);
  const id = service.id;

  store.update(id, { name: 'Films', url: 'http://10.0.0.9:7878', key: 'zzz', enabled: false });
  const after = store.get(id);
  assert.equal(after.id, id, 'editing a service must not re-key it');
  assert.equal(after.name, 'Films');
  assert.equal(after.url, 'http://10.0.0.9:7878');
  assert.equal(after.key, 'zzz');
  assert.equal(after.enabled, false);

  // Even changing what kind of thing it is keeps the id: anything holding a
  // reference to this instance still means this instance.
  store.update(id, { type: 'sonarr' });
  assert.equal(store.get(id).id, id);
  assert.equal(store.get(id).type, 'sonarr');
});

test('a bad address is refused, not stored', () => {
  const s = fakeStorage();
  const store = createServiceStore(s);
  for (const bad of ['', '   ', 'not a url', 'javascript:alert(1)', 'ftp://box', 'http://']) {
    const r = store.add({ ...radarr, url: bad });
    assert.equal(r.ok, false, `${JSON.stringify(bad)} should be refused`);
    assert.match(r.error, /address/i);
  }
  // Nothing was written. A typo that appears to save is a row that looks
  // configured, answers nothing, and gives no clue which half is wrong.
  assert.equal(store.list().length, 0);
});

test('editing to a bad address leaves the good one in place', () => {
  const store = createServiceStore(fakeStorage());
  const { service } = store.add(radarr);
  const r = store.update(service.id, { url: 'nonsense::' });
  assert.equal(r.ok, false);
  assert.equal(store.get(service.id).url, 'http://192.168.0.26');
});

test('an unknown kind of service is refused', () => {
  const store = createServiceStore(fakeStorage());
  const r = store.add({ type: 'ombi', url: 'http://192.168.0.5' });
  assert.equal(r.ok, false);
});

test('removing something already gone is not an error', () => {
  const store = createServiceStore(fakeStorage());
  assert.equal(store.remove('nope-1').removed, 0);
});

test('corrupt storage empties the list instead of killing the settings screen', () => {
  // The screen that shows this is the screen someone would use to fix it, so it
  // has to paint.
  for (const junk of ['{not json', '{"a":1}', 'null', '"a string"']) {
    assert.deepEqual(createServiceStore(fakeStorage(junk)).list(), []);
  }
  // One unreadable row among good ones costs that row, not the page.
  const mixed = JSON.stringify([
    { id: 'r1', type: 'radarr', url: 'http://192.168.0.26' },
    { id: 'x1', type: 'inventedarr', url: 'http://192.168.0.27' },
    null,
    { type: 'sonarr', url: 'http://192.168.0.138' },
  ]);
  const rows = createServiceStore(fakeStorage(mixed)).list();
  assert.equal(rows.length, 1);
  assert.equal(rows[0].id, 'r1');
});

// ------------------------------------------------------------------- URLs --

test('addresses are cleaned without a typo becoming a plausible host', () => {
  assert.equal(normaliseServiceURL('http://192.168.0.26/'), 'http://192.168.0.26');
  assert.equal(normaliseServiceURL('  http://192.168.0.251:8096//  '), 'http://192.168.0.251:8096');
  assert.equal(normaliseServiceURL('https://plex.example.com?x=1#y'), 'https://plex.example.com');
  // A reverse proxy at a subpath is ordinary, and dropping the path would aim
  // every call at the proxy's front page instead.
  assert.equal(normaliseServiceURL('https://home.example.com/radarr/'), 'https://home.example.com/radarr');
  // A bare LAN host is plain HTTP in reality; assuming https there produces a
  // TLS error that reads as "my Radarr is broken".
  assert.equal(normaliseServiceURL('192.168.0.26:7878'), 'http://192.168.0.26:7878');
  assert.equal(normaliseServiceURL('nas.local'), 'http://nas.local');
  // "localhost:8096" is the single most common thing typed into this box, and
  // a naive scheme test reads "localhost" as the scheme and throws the whole
  // address away with a message about it not looking like an address.
  assert.equal(normaliseServiceURL('localhost:8096'), 'http://localhost:8096');
  assert.equal(normaliseServiceURL('nas.local:8080'), 'http://nas.local:8080');
  assert.equal(normaliseServiceURL('media.example.com:443'), 'https://media.example.com');
  assert.equal(normaliseServiceURL('media.example.com'), 'https://media.example.com');
  // A rejected scheme must never be prefixed into something that parses.
  assert.equal(normaliseServiceURL('ftp://box'), '');
  assert.equal(normaliseServiceURL('javascript:alert(1)'), '');
  assert.equal(normaliseServiceURL('file:///etc/passwd'), '');
  assert.equal(normaliseServiceURL(null), '');
});

// ------------------------------------------------------------- the probe ---

test('every type this UI offers can actually be probed', () => {
  for (const id of allServiceTypes()) {
    const spec = SERVICE_TYPES[id];
    assert.ok(spec.label, `${id} has no label`);
    assert.ok(spec.credential, `${id} does not say what credential it wants`);
    assert.ok(spec.credentialHint, `${id} does not say where to find it`);
    assert.ok(spec.probe?.path?.startsWith('/'), `${id} has no probe path`);
    assert.equal(typeof spec.probe.marker, 'function', `${id} has no way to prove it is ${id}`);
    // A provider claiming "movies" would route nothing and report nothing
    // wrong -- the exact bug schema.json exists to prevent.
    assert.ok(describeService({ type: id }), `${id} describes as nothing`);
  }
  // The list the product promises, all present.
  for (const want of [
    'radarr', 'sonarr', 'lidarr', 'romarr', 'mylar3', 'readarr', 'prowlarr',
    'jellyfin', 'plex', 'emby', 'komga', 'kavita', 'audiobookshelf', 'romm',
  ]) {
    assert.ok(SERVICE_TYPES[want], `${want} is missing from the picker`);
  }
});

test('the key rides in a header wherever a header exists', () => {
  const req = buildProbeRequest({ type: 'radarr', url: 'http://192.168.0.26/', key: 'SEKRIT' });
  assert.equal(req.url, 'http://192.168.0.26/api/v3/system/status');
  assert.equal(req.headers['X-Api-Key'], 'SEKRIT');
  assert.equal(req.carriesKeyInURL, false);
  assert.ok(!req.url.includes('SEKRIT'), 'the key must not be in the URL');

  const jf = buildProbeRequest({ type: 'jellyfin', url: 'http://192.168.0.251:8096', key: 'JF' });
  assert.match(jf.headers.Authorization, /MediaBrowser Token="JF"/);
  assert.ok(!jf.url.includes('JF'));
});

test('the two services with no header scheme say so rather than hiding it', () => {
  // Mylar3 and Kavita genuinely take the key as a query parameter. That is
  // flagged so the UI can disclose it, because it changes where that key ends
  // up -- proxy logs, browser history -- and that is the user's call to make.
  for (const type of ['mylar3', 'kavita']) {
    assert.equal(SERVICE_TYPES[type].keyInURL, true, `${type} should disclose this`);
    const req = buildProbeRequest({ type, url: 'http://192.168.0.90:8090', key: 'K1' });
    assert.equal(req.carriesKeyInURL, true);
    assert.ok(req.url.includes('K1'));
  }
  // And nothing else does.
  for (const type of allServiceTypes()) {
    if (type === 'mylar3' || type === 'kavita') continue;
    assert.ok(!SERVICE_TYPES[type].keyInURL, `${type} should not need a key in a URL`);
  }
});

test('401 means reachable and check the key, never offline', () => {
  const h = classifyProbe({ status: 401, type: 'radarr', name: 'Radarr' });
  assert.equal(h.state, 'auth_failed');
  assert.match(h.detail, /answered/i);
  assert.match(h.detail, /api key/i);
  assert.doesNotMatch(h.detail, /offline|unreachable|down/i);
});

test('each failure lands on its own state, because each has its own fix', () => {
  const t = 'radarr';
  assert.equal(classifyProbe({ error: 'Failed to fetch', type: t }).state, 'unreachable');
  assert.equal(classifyProbe({ status: 403, type: t }).state, 'auth_failed');
  assert.equal(classifyProbe({ status: 404, type: t }).state, 'incompatible');
  assert.equal(classifyProbe({ status: 500, type: t }).state, 'degraded');
  assert.equal(classifyProbe({ status: 502, type: t }).state, 'degraded');
  // Answered 200 with a login page instead of JSON: something is there, but it
  // is not the API. Calling that "unreachable" sends someone to the wrong place.
  assert.equal(classifyProbe({ status: 200, bodyIsJSON: false, type: t }).state, 'incompatible');
  // JSON, but not Radarr's JSON -- the reverse-proxy-answering-for-it case.
  assert.equal(classifyProbe({ status: 200, body: { hello: 'world' }, type: t }).state, 'incompatible');
});

test('answering the door and refusing the API call is said out loud', async () => {
  // Measured on RomM: transport.js reaches the root (it serves a web UI and
  // allows it), then the API call is refused. The screen then said "reachable
  // directly" directly above "did not answer", which reads as a contradiction
  // and leaves the reader to work out which half to believe.
  const svc = { type: 'romm', name: 'RomM', url: 'http://192.168.0.94:8080', key: 'k' };
  const r = await probeService(svc, {
    chooseTransport: async () => ({ transport: TRANSPORT.DIRECT, detail: 'reachable directly' }),
    transportFetch: async () => { throw new TypeError('Failed to fetch'); },
  });
  assert.equal(r.route.transport, TRANSPORT.DIRECT);
  assert.equal(r.health.state, 'unreachable');
  assert.match(r.health.detail, /then refused this API call/);
  assert.match(r.health.detail, /extension|own machine/i);
  // The bare "did not answer" wording belongs to the case where nothing
  // answered at all, and would contradict the route line here.
  assert.doesNotMatch(r.health.detail, /did not answer/);
});

test('a healthy answer carries the version it proved', () => {
  const h = classifyProbe({ status: 200, body: { version: '5.14.0' }, type: 'radarr', name: 'Radarr' });
  assert.equal(h.state, 'healthy');
  assert.equal(h.version, '5.14.0');
  assert.match(h.detail, /5\.14\.0/);
});

test('a probe never repeats the key back into anything shown to anyone', () => {
  const svc = { type: 'radarr', name: 'Radarr', url: 'http://192.168.0.26', key: 'SUPERSECRET' };
  for (const h of [
    classifyProbe({ status: 401, type: svc.type, name: svc.name }),
    classifyProbe({ status: 200, body: { version: '5' }, type: svc.type, name: svc.name }),
    classifyProbe({ error: 'Failed to fetch', type: svc.type, name: svc.name }),
  ]) {
    assert.ok(!h.detail.includes('SUPERSECRET'), 'a key leaked into a message');
  }
});

test('every state the schema defines has something a person can read', () => {
  for (const state of HEALTH_STATES) {
    assert.ok(HEALTH_LABELS[state], `no label for ${state}`);
  }
  // And nothing invented beyond them, which is how a seventh state would slip
  // in without the schema ever agreeing to it.
  assert.deepEqual(Object.keys(HEALTH_LABELS).sort(), [...HEALTH_STATES].sort());
});

// ------------------------------------------------- probe, end to end-ish ---

test('a blocked service repeats transport.js word for word', async () => {
  // Two explanations of one problem in front of the same person is worse than
  // one, so this file must not paraphrase.
  const svc = { type: 'radarr', name: 'Radarr', url: 'http://192.168.0.26', key: 'k' };
  const r = await probeService(svc, {
    pageProtocol: 'https:',
    hasExtension: false,
    transportFetch: () => { throw new Error('should never be called'); },
  });
  assert.equal(r.route.transport, TRANSPORT.NONE);
  assert.equal(r.route.blocked, BLOCKED.MIXED_CONTENT);
  assert.equal(r.health.state, 'unreachable');
  assert.equal(r.health.detail, r.route.detail);
  assert.match(r.health.detail, /extension|own machine|HTTPS/i);
});

test('a service with an address but no key is not_configured, not broken', async () => {
  const svc = { type: 'radarr', name: 'Radarr', url: 'http://192.168.0.26', key: '' };
  const r = await probeService(svc, {
    pageProtocol: 'http:',
    hasExtension: false,
    chooseTransport: async () => ({ transport: TRANSPORT.DIRECT, detail: 'reachable directly' }),
    transportFetch: () => { throw new Error('should never be called without a key'); },
  });
  assert.equal(r.health.state, 'not_configured');
  assert.match(r.health.detail, /api key/i);
  assert.match(r.health.detail, /Settings/);
});

test('the extension route is used, and what it answers is still judged', async () => {
  const svc = { type: 'jellyfin', name: 'Jellyfin', url: 'http://192.168.0.251:8096', key: 'k' };
  let usedTransport = null;
  const r = await probeService(svc, {
    pageProtocol: 'https:',
    hasExtension: true,
    transportFetch: async (transport) => {
      usedTransport = transport;
      return { status: 200, json: async () => ({ Version: '10.9.11' }) };
    },
  });
  assert.equal(usedTransport, TRANSPORT.EXTENSION);
  assert.equal(r.route.transport, TRANSPORT.EXTENSION);
  assert.equal(r.health.state, 'healthy');
  assert.equal(r.health.version, '10.9.11');
});

test('no address at all is not_configured without asking the network', async () => {
  const r = await probeService({ type: 'plex', name: 'Plex', url: '', key: 'x' }, {
    chooseTransport: () => { throw new Error('should never be called'); },
  });
  assert.equal(r.health.state, 'not_configured');
});

// ------------------------------------------------------------ the advice ---

test('the two real routes are offered once, and only when they would help', () => {
  const list = [
    { name: 'Radarr', url: 'http://192.168.0.26', enabled: true },
    { name: 'Sonarr', url: 'http://192.168.0.138', enabled: true },
  ];
  const a = routeAdvice(list, { hasExtension: false, pageProtocol: 'https:' });
  assert.ok(a);
  assert.equal(a.count, 2);
  assert.equal(a.routes.length, 2);
  assert.ok(a.routes.some((r) => r.href === '/selfhost.sh'));
  assert.ok(a.routes.every((r) => r.detail.length > 0), 'each route must say what it buys');

  // Someone who already installed it has taken one of the two routes.
  assert.equal(routeAdvice(list, { hasExtension: true, pageProtocol: 'https:' }), null);
  // A self-hoster on their own HTTP copy is already fine and must not be told
  // to install anything.
  assert.equal(routeAdvice(list, { hasExtension: false, pageProtocol: 'http:' }), null);
  // A disabled service is not a reason to say anything.
  assert.equal(
    routeAdvice([{ name: 'x', url: 'http://192.168.0.26', enabled: false }],
      { hasExtension: false, pageProtocol: 'https:' }),
    null,
  );
  // Nor is a public HTTPS service.
  assert.equal(
    routeAdvice([{ name: 'x', url: 'https://plex.example.com', enabled: true }],
      { hasExtension: false, pageProtocol: 'https:' }),
    null,
  );
  assert.equal(routeAdvice([], { hasExtension: false, pageProtocol: 'https:' }), null);
});

test('a single blocked service is described in the singular', () => {
  const a = routeAdvice([{ name: 'Jellyfin', url: 'http://192.168.0.251:8096', enabled: true }],
    { hasExtension: false, pageProtocol: 'https:' });
  assert.match(a.reason, /^Jellyfin is on a plain-HTTP address/);
});
