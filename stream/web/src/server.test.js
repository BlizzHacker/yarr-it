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
  assert.equal(normaliseServer('192.168.1.50:8802'), 'https://192.168.1.50:8802');
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
