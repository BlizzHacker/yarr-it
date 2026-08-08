import test from 'node:test';
import assert from 'node:assert/strict';

import { normaliseServer } from './server.js';

test('a bare hostname is what people actually type', () => {
  assert.equal(normaliseServer('yarrit.com'), 'https://yarrit.com');
  assert.equal(normaliseServer('  yarrit.com  '), 'https://yarrit.com');
});

test('paths, queries and trailing slashes are stripped', () => {
  // Otherwise every later URL ends up with a double slash or a stray path.
  assert.equal(normaliseServer('https://yarrit.com/'), 'https://yarrit.com');
  assert.equal(normaliseServer('https://yarrit.com/some/page?x=1#y'), 'https://yarrit.com');
});

test('a self-hoster on a plain-http LAN box is supported', () => {
  // Insisting on https would lock out exactly the people this feature is for.
  assert.equal(normaliseServer('http://192.168.1.50:8802'), 'http://192.168.1.50:8802');
  // This line used to assert https, under a test named "a plain-http LAN box is
  // supported" -- the name stated the requirement and the assertion pinned its
  // opposite, with the suite fully green. A LAN box has no certificate, so
  // https here means ERR_CONNECTION_REFUSED and a message blaming the network.
  assert.equal(normaliseServer('192.168.1.50:8802'), 'http://192.168.1.50:8802');
  assert.equal(normaliseServer('localhost:8096'), 'http://localhost:8096');
  assert.equal(normaliseServer('nas.local:8080'), 'http://nas.local:8080');
  assert.equal(normaliseServer('10.0.0.5'), 'http://10.0.0.5');
  // A public host still defaults to https, which is the safe guess there.
  assert.equal(normaliseServer('yarrit.com'), 'https://yarrit.com');
  assert.equal(normaliseServer('example.com:8443'), 'https://example.com:8443');
});

test('a non-http scheme is refused rather than half-accepted', () => {
  assert.equal(normaliseServer('javascript:alert(1)'), '');
  assert.equal(normaliseServer('ftp://example.com'), '');
  assert.equal(normaliseServer(''), '');
  assert.equal(normaliseServer(null), '');
  assert.equal(normaliseServer('   '), '');
});

test('nonsense does not throw', () => {
  // A settings box receives paste damage of every kind; it must not crash the app.
  for (const bad of ['http://', '://x', 'https://[', '%%%']) {
    assert.equal(typeof normaliseServer(bad), 'string');
  }
});

// --- bridgeSocketURL ---------------------------------------------------------
// These need browser globals. server.js reads them inside the functions rather
// than at import, so stubbing here is enough.

function withPage({ stored = null, protocol = 'https:', host = 'yarrit.com' }, fn) {
  const prevLS = globalThis.localStorage;
  const prevLoc = globalThis.location;
  globalThis.localStorage = {
    getItem: () => stored,
    setItem: () => {},
    removeItem: () => {},
  };
  globalThis.location = {
    protocol,
    host,
    origin: `${protocol}//${host}`,
    search: '',
  };
  try {
    return fn();
  } finally {
    globalThis.localStorage = prevLS;
    globalThis.location = prevLoc;
  }
}

test('the relay follows the page scheme, so a plain-http self-host works', async () => {
  const { bridgeSocketURL } = await import('./server.js');

  // The bug this replaces: wss:// was hard-coded. On a LAN box served over
  // http the browser refuses the secure socket, and only torrents break --
  // search and direct files keep working, so nothing looks wrong.
  withPage({ protocol: 'http:', host: '192.168.1.50:8802' }, () => {
    assert.equal(bridgeSocketURL(), 'ws://192.168.1.50:8802/bridge/socket');
  });

  withPage({ protocol: 'https:', host: 'yarrit.com' }, () => {
    assert.equal(bridgeSocketURL(), 'wss://yarrit.com/bridge/socket');
  });
});

test('the relay follows the configured server, not the page it was loaded from', async () => {
  const { bridgeSocketURL } = await import('./server.js');

  // Someone opens yarrit.com but points the client at their own box: the peer
  // relay has to go to their box too, or the torrent is relayed by us.
  withPage({ stored: 'http://192.168.1.50:8802', protocol: 'https:', host: 'yarrit.com' }, () => {
    assert.equal(bridgeSocketURL(), 'ws://192.168.1.50:8802/bridge/socket');
  });

  withPage({ stored: 'https://mybox.example', protocol: 'http:', host: 'localhost:8802' }, () => {
    assert.equal(bridgeSocketURL(), 'wss://mybox.example/bridge/socket');
  });
});

// A hostname with a port is letters-then-colon, which matches a scheme pattern
// exactly. That rejected "localhost:8096" -- the single most common thing
// anyone types into a server box -- along with every other named host with a
// port. Found by testing the settings UI against real services.
test('a hostname with a port is a host, not a scheme', () => {
  // Private hosts get http, because a LAN box has no certificate; public ones
  // get https. The point of this test is that the colon is read as a port
  // separator at all -- "localhost:" matches a scheme pattern perfectly, which
  // is what used to reject every one of these outright.
  assert.equal(normaliseServer('localhost:8096'), 'http://localhost:8096');
  assert.equal(normaliseServer('box.local:5000'), 'http://box.local:5000');
  assert.equal(normaliseServer('192.168.0.26:7878'), 'http://192.168.0.26:7878');
  // A name we cannot tell is private defaults to https, which is the safe guess.
  assert.equal(normaliseServer('nas:8080'), 'https://nas:8080');
  assert.equal(normaliseServer('jellyfin:8096'), 'https://jellyfin:8096');
});

// The narrow host:port rule must not become a way back in for the schemes the
// function exists to reject.
test('fixing host:port did not readmit a rejected scheme', () => {
  assert.equal(normaliseServer('ftp://example.com'), '');
  assert.equal(normaliseServer('javascript:alert(1)'), '');
  assert.equal(normaliseServer('file:///etc/passwd'), '');
  // Looks port-ish but is a scheme with a path; must not be treated as a host.
  assert.equal(normaliseServer('ftp://example.com:21'), '');
  // A scheme whose "port" is not digits to the end cannot slip through.
  assert.equal(normaliseServer('data:text/html,x'), '');
});
