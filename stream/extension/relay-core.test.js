import test from 'node:test';
import assert from 'node:assert/strict';

import {
  BUILTIN_ORIGINS, MAX_HEADERS,
  createLimiter, describeTarget, hostPattern, isTrustedOrigin,
  normaliseOrigin, permissionNeededMessage, sanitiseHeaders,
  sanitiseResponseHeaders, validateRequest, withTimeout,
} from './relay-core.js';

// ------------------------------------------------------- who may ask -------

test('only yarrit.com and what the user added may drive the relay', () => {
  assert.ok(isTrustedOrigin('https://yarrit.com'));
  assert.ok(isTrustedOrigin('https://www.yarrit.com'));
  assert.ok(isTrustedOrigin('http://192.168.0.50:8080', ['http://192.168.0.50:8080']));

  for (const bad of [
    'https://evil.test',
    // The classic way an origin check goes wrong. endsWith('yarrit.com') accepts
    // this, and accepting it hands a stranger a port scanner aimed at the LAN.
    'https://yarrit.com.evil.test',
    'https://notyarrit.com',
    // Scheme is part of an origin: a plain-HTTP impostor is a different site.
    'http://yarrit.com',
    // A subdomain is a different origin and is not implied by the parent.
    'https://beta.yarrit.com',
    '', null, undefined, '*', 'null',
  ]) {
    assert.ok(!isTrustedOrigin(bad), `${bad} must not be trusted`);
  }
});

test('a wildcard can never become an origin or a host permission', () => {
  // `new URL('https://*')` parses and yields the hostname "*". Left alone that
  // becomes the match pattern `https://*/*`, which Chrome accepts -- one
  // character typed into the options box would grant the relay the entire web
  // and inject it into every HTTPS page.
  assert.equal(normaliseOrigin('*'), '');
  assert.equal(hostPattern('https://*/'), '');
  assert.equal(hostPattern('http://*.example.com/'), '');
  assert.equal(hostPattern('https://192.168.0.*/'), '');
  assert.equal(normaliseOrigin('*://*/*'), '');
  assert.ok(!isTrustedOrigin('https://evil.test', ['*']));
  assert.ok(!isTrustedOrigin('https://evil.test', ['*://*/*']));
  // A relayed request aimed at a wildcard is refused before it reaches Chrome,
  // and says so as a wildcard rather than blaming IPv6.
  const w = validateRequest({ id: 'a', url: 'https://*/x' });
  assert.equal(w.ok, false);
  assert.doesNotMatch(w.error, /IPv6/);
  assert.match(w.error, /\*/);
  assert.match(validateRequest({ id: 'a', url: 'http://[::1]:8096/' }).error, /IPv6/);
});

test('an origin is reduced to scheme, host and port and nothing else', () => {
  assert.equal(normaliseOrigin('  yarrit.com '), 'https://yarrit.com');
  assert.equal(normaliseOrigin('http://192.168.0.50:8080/some/path?x=1'), 'http://192.168.0.50:8080');
  // A rejected scheme must not become a plausible host: naive prefixing turns
  // "ftp://x" into "https://ftp://x", which parses with the hostname "ftp".
  assert.equal(normaliseOrigin('ftp://example.com'), '');
  assert.equal(normaliseOrigin('javascript:alert(1)'), '');
  assert.equal(normaliseOrigin('file:///C:/Users'), '');
  assert.equal(normaliseOrigin(''), '');
});

// ---------------------------------------------------- what may be asked ----

test('a host permission drops the port, because Chrome rejects one that has it', () => {
  // This is not cosmetic. `http://192.168.0.251:8096/*` makes the whole
  // chrome.permissions call fail, and every service this exists for lives on a
  // port: Jellyfin 8096, RomM 8080, Radarr 7878.
  assert.equal(hostPattern('http://192.168.0.251:8096/System/Info'), 'http://192.168.0.251/*');
  assert.equal(hostPattern('http://192.168.0.26:7878/api/v3/system/status'), 'http://192.168.0.26/*');
  assert.equal(hostPattern('https://jellyfin.example.com/x'), 'https://jellyfin.example.com/*');
});

test('addresses Chrome cannot express as a match pattern are refused up front', () => {
  assert.equal(hostPattern('http://[::1]:8096/'), '');
  assert.equal(hostPattern('file:///C:/Users/wadei/.ssh/id_rsa'), '');
  assert.equal(hostPattern('chrome-extension://abc/x'), '');
  assert.equal(hostPattern('not a url'), '');
});

test('the relay refuses every scheme but http and https', () => {
  for (const url of [
    'file:///C:/Users/wadei/.ssh/id_rsa',
    'chrome-extension://abcdef/manifest.json',
    'data:text/html,<script>x</script>',
    'javascript:fetch("/")',
    'ftp://192.168.0.26/',
  ]) {
    const r = validateRequest({ id: 'a', url });
    assert.equal(r.ok, false, `${url} must be refused`);
  }
});

test('a real Radarr call passes and comes out normalised', () => {
  const r = validateRequest({
    id: 'yf_1',
    url: 'http://192.168.0.26:7878/api/v3/system/status',
    init: { method: 'get', headers: { 'X-Api-Key': 'abc123' } },
  });
  assert.equal(r.ok, true);
  assert.equal(r.method, 'GET');
  assert.equal(r.pattern, 'http://192.168.0.26/*');
  assert.equal(r.headers['x-api-key'], 'abc123');
  assert.equal(r.body, undefined);
});

test('malformed requests are rejected rather than half-understood', () => {
  assert.equal(validateRequest(null).ok, false);
  assert.equal(validateRequest({ url: 'http://192.168.0.26/' }).ok, false, 'no id');
  assert.equal(validateRequest({ id: 'a'.repeat(200), url: 'http://192.168.0.26/' }).ok, false);
  assert.equal(validateRequest({ id: 'a', url: 'http://x/', init: { method: 'TRACE' } }).ok, false);
  assert.equal(validateRequest({ id: 'a', url: 'http://x/', init: { method: 'CONNECT' } }).ok, false);
  // A body that is not text would be structured-cloned into the worker and then
  // handed to fetch as whatever it happens to be.
  assert.equal(validateRequest({ id: 'a', url: 'http://x/', init: { method: 'POST', body: { a: 1 } } }).ok, false);
  assert.equal(validateRequest({ id: 'a', url: 'http://x/', init: { body: 'x' } }).ok, false, 'GET with a body');
});

// -------------------------------------------------- what never crosses -----

test('cookies never cross the relay in either direction', () => {
  const out = sanitiseHeaders({
    Cookie: 'session=abc',
    cookie2: 'x',
    'Set-Cookie': 'a=b',
    Host: 'evil.test',
    Origin: 'https://evil.test',
    Referer: 'https://evil.test/',
  });
  assert.deepEqual(out, {}, 'nothing credential-shaped may be forwarded');

  const back = sanitiseResponseHeaders([
    ['Content-Type', 'application/json'],
    ['Set-Cookie', 'session=abc'],
  ]);
  assert.deepEqual(back, { 'content-type': 'application/json' });
});

test('the API key the user typed for their own service is kept', () => {
  // The point of the relay is reaching services that want a key. Stripping
  // these would leave a relay that can only fetch login pages.
  const out = sanitiseHeaders({
    'X-Api-Key': 'radarr-key',
    Authorization: 'Bearer jellyfin-token',
    'X-Emby-Token': 'emby',
    Accept: 'application/json',
  });
  assert.deepEqual(out, {
    'x-api-key': 'radarr-key',
    authorization: 'Bearer jellyfin-token',
    'x-emby-token': 'emby',
    accept: 'application/json',
  });
});

test('a header cannot smuggle a second request in a newline', () => {
  const out = sanitiseHeaders({
    'X-Api-Key': 'ok\r\nX-Injected: yes',
    'X-Good': 'fine',
    'bad name': 'v',
    'X-Null': 'a\0b',
  });
  assert.deepEqual(out, { 'x-good': 'fine' });
});

test('a page cannot flood the relay with headers', () => {
  const many = {};
  for (let i = 0; i < 200; i += 1) many[`x-h${i}`] = 'v';
  assert.equal(Object.keys(sanitiseHeaders(many)).length, MAX_HEADERS);
  assert.deepEqual(sanitiseHeaders('nope'), {});
  assert.deepEqual(sanitiseHeaders(['a']), {});
});

test('nothing shown or stored ever carries the query string', () => {
  // Radarr accepts ?apikey=, so a full URL in a prompt, a badge or a stored
  // pending-request is a key on screen and a key in storage.
  const url = 'http://192.168.0.26:7878/api/v3/movie?apikey=SUPERSECRET';
  assert.equal(describeTarget(url), 'http://192.168.0.26:7878');
  const msg = permissionNeededMessage(url);
  assert.ok(!msg.includes('SUPERSECRET'), msg);
  assert.ok(!msg.includes('apikey'), msg);
  assert.match(msg, /192\.168\.0\.26:7878/);
  // It has to name the next click; the prompt needs a gesture the page cannot make.
  assert.match(msg, /toolbar|icon/i);
});

// ------------------------------------------------------- back pressure -----

test('a wedged service cannot take the working ones down with it', async () => {
  const limiter = createLimiter({ max: 2, maxQueue: 3 });
  let peak = 0;
  let live = 0;
  const release = [];
  const jobs = [];

  for (let i = 0; i < 5; i += 1) {
    jobs.push(limiter.run(() => {
      live += 1;
      peak = Math.max(peak, live);
      return new Promise((res) => release.push(() => { live -= 1; res(i); }));
    }));
  }

  // Two running, three queued -- and the queue is now full.
  await assert.rejects(() => limiter.run(async () => 'overflow'), /too many requests/);
  assert.equal(peak, 2, 'more than max ran at once');

  while (release.length) release.shift()();
  // Draining the first two lets the queued ones start; keep releasing.
  for (let i = 0; i < 10 && release.length === 0; i += 1) await Promise.resolve();
  while (release.length) release.shift()();
  for (let i = 0; i < 10; i += 1) { await Promise.resolve(); while (release.length) release.shift()(); }

  assert.deepEqual(await Promise.all(jobs), [0, 1, 2, 3, 4]);
  assert.ok(peak <= 2, `peak concurrency was ${peak}`);
});

test('a slot is given back when a job throws, not just when it succeeds', async () => {
  const limiter = createLimiter({ max: 1, maxQueue: 4 });
  await assert.rejects(() => limiter.run(async () => { throw new Error('boom'); }), /boom/);
  assert.equal(limiter.active, 0);
  assert.equal(await limiter.run(async () => 'after'), 'after');
});

test('a service that never answers is given up on', async () => {
  await assert.rejects(
    () => withTimeout(new Promise(() => {}), 20, 'did not answer in time'),
    /did not answer in time/,
  );
  assert.equal(await withTimeout(Promise.resolve('ok'), 1000, 'x'), 'ok');
});

// ----------------------------------------------------------- odds & ends ---

test('the built-in origins are exactly the site, over https', () => {
  assert.deepEqual(BUILTIN_ORIGINS, ['https://yarrit.com', 'https://www.yarrit.com']);
});
