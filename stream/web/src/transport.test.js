import test from 'node:test';
import assert from 'node:assert/strict';

import {
  TRANSPORT, BLOCKED, isPrivateHost, wouldBeMixedContent, chooseTransport,
} from './transport.js';

test('private addresses are recognised across the ranges people actually use', () => {
  for (const h of [
    'http://192.168.0.26', 'http://10.0.0.5:7878', 'http://172.16.4.2',
    'http://172.31.255.1', 'http://127.0.0.1:8096', 'http://localhost:8989',
    'http://nas.local:8080', 'http://box.lan',
  ]) {
    assert.ok(isPrivateHost(h), `${h} should be private`);
  }
  // 172.32 is outside the private block; treating it as private would send a
  // public address down the extension path for no reason.
  for (const h of ['https://yarrit.com', 'http://172.32.0.1', 'http://8.8.8.8']) {
    assert.ok(!isPrivateHost(h), `${h} should not be private`);
  }
});

test('the mixed-content wall is predicted, not discovered', () => {
  // The browser refuses before sending, and the resulting TypeError is
  // identical to "the server is down". Telling someone their Radarr is offline
  // when the browser simply refused to ask is wrong twice over.
  assert.ok(wouldBeMixedContent('http://192.168.0.26', 'https:'));
  assert.ok(!wouldBeMixedContent('https://192.168.0.26', 'https:'));
  assert.ok(!wouldBeMixedContent('http://192.168.0.26', 'http:'));
});

test('a guest on the hosted site with no extension is told the real reason', async () => {
  const r = await chooseTransport('http://192.168.0.26', {
    pageProtocol: 'https:',
    hasExtension: false,
    fetchImpl: () => { throw new Error('should never be called'); },
  });
  assert.equal(r.transport, TRANSPORT.NONE);
  assert.equal(r.blocked, BLOCKED.MIXED_CONTENT);
  // The message must name a fix, not just a failure.
  assert.match(r.detail, /extension|own machine|HTTPS/i);
});

test('the extension is the door, not a fallback for a broken service', async () => {
  let probed = false;
  const r = await chooseTransport('http://192.168.0.26', {
    pageProtocol: 'https:',
    hasExtension: true,
    fetchImpl: () => { probed = true; throw new Error('nope'); },
  });
  assert.equal(r.transport, TRANSPORT.EXTENSION);
  // Probing first would waste a guaranteed-blocked request every time.
  assert.equal(probed, false, 'it tried a request the browser was always going to refuse');
});

test('a self-hoster on their own LAN needs nothing extra', async () => {
  const r = await chooseTransport('http://192.168.0.26', {
    pageProtocol: 'http:',
    hasExtension: false,
    fetchImpl: async () => ({ status: 200 }),
  });
  assert.equal(r.transport, TRANSPORT.DIRECT);
});

test('a service that answers 401 is reachable, not broken', async () => {
  // It is there and wants a key. That is a configuration step, and calling it
  // unreachable sends someone to check their network instead of their settings.
  const r = await chooseTransport('https://plex.example', {
    pageProtocol: 'https:',
    hasExtension: false,
    fetchImpl: async () => ({ status: 401 }),
  });
  assert.equal(r.transport, TRANSPORT.DIRECT);
});

test('an unreachable service with no extension explains CORS as well as downtime', async () => {
  const r = await chooseTransport('https://nothing.example', {
    pageProtocol: 'https:',
    hasExtension: false,
    fetchImpl: async () => { throw new TypeError('Failed to fetch'); },
  });
  assert.equal(r.transport, TRANSPORT.NONE);
  assert.equal(r.blocked, BLOCKED.CORS);
  assert.match(r.detail, /offline|refusing/i);
});
