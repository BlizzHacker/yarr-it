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
