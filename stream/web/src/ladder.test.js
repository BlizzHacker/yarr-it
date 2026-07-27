import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chooseTier, proxiedUrl, probeTier } from './ladder.js';
import { TIER } from './source.js';
import { FAILURE } from './failures.js';

const base = {
  pageProtocol: 'https:',
  streamUrl: 'https://cdn.example.com/live.m3u8',
  corsHeader: '*',
  gatewayUrl: null,
  relayAvailable: true,
};

test('a clean https stream with cors plays direct and costs nothing', () => {
  assert.deepEqual(chooseTier(base), { tier: TIER.DIRECT, blockedBy: null });
});

test('http stream on an https page is mixed content and cannot go direct', () => {
  const r = chooseTier({ ...base, streamUrl: 'http://cdn.example.com/live.m3u8' });
  assert.equal(r.blockedBy, FAILURE.MIXED_CONTENT);
  assert.equal(r.tier, TIER.RELAY);
});

test('the user gateway is preferred over the public relay when configured', () => {
  const r = chooseTier({
    ...base,
    streamUrl: 'http://cdn.example.com/live.m3u8',
    gatewayUrl: 'http://192.168.0.118:8900',
  });
  assert.equal(r.tier, TIER.GATEWAY);
});

test('a missing cors header blocks direct playback', () => {
  const r = chooseTier({ ...base, corsHeader: null });
  assert.equal(r.blockedBy, FAILURE.CORS_BLOCKED);
  assert.equal(r.tier, TIER.RELAY);
});

test('an http page can play an http stream directly - no mixed content rule applies', () => {
  const r = chooseTier({
    ...base,
    pageProtocol: 'http:',
    streamUrl: 'http://cdn.example.com/live.m3u8',
  });
  assert.equal(r.tier, TIER.DIRECT);
});

test('with no gateway and no relay the blocker is reported and no tier is chosen', () => {
  const r = chooseTier({ ...base, corsHeader: null, relayAvailable: false });
  assert.equal(r.tier, null);
  assert.equal(r.blockedBy, FAILURE.CORS_BLOCKED);
});

test('a blob stream url with no cors header plays direct - CORS never applied to it', () => {
  const r = chooseTier({ ...base, streamUrl: 'blob:https://example.com/abc-123', corsHeader: null });
  assert.deepEqual(r, { tier: TIER.DIRECT, blockedBy: null });
});

test('a data stream url with no cors header plays direct - CORS never applied to it', () => {
  const r = chooseTier({ ...base, streamUrl: 'data:video/mp4;base64,AAAA', corsHeader: null });
  assert.deepEqual(r, { tier: TIER.DIRECT, blockedBy: null });
});

test('a stream that is both mixed content and cors-less reports mixed content, not cors', () => {
  const r = chooseTier({
    ...base,
    streamUrl: 'http://cdn.example.com/live.m3u8',
    corsHeader: null,
  });
  assert.equal(r.blockedBy, FAILURE.MIXED_CONTENT);
});

test('an uppercase HTTP scheme on an https page is still detected as mixed content', () => {
  const r = chooseTier({ ...base, streamUrl: 'HTTP://cdn.example.com/live.m3u8' });
  assert.equal(r.blockedBy, FAILURE.MIXED_CONTENT);
  assert.equal(r.tier, TIER.RELAY);
});

// --------------------------------------------------------------- proxiedUrl --

test('proxiedUrl leaves a direct-tier url untouched', () => {
  assert.equal(
    proxiedUrl('https://cdn.example.com/live.m3u8', TIER.DIRECT, null),
    'https://cdn.example.com/live.m3u8',
  );
});

test('proxiedUrl routes a gateway-tier url through the user\'s own gateway', () => {
  assert.equal(
    proxiedUrl('http://cdn.example.com/live.m3u8', TIER.GATEWAY, 'http://192.168.0.118:8900'),
    'http://192.168.0.118:8900/iptv?u=' + encodeURIComponent('http://cdn.example.com/live.m3u8'),
  );
});

test('proxiedUrl routes a relay-tier url through the public /bridge/iptv endpoint', () => {
  assert.equal(
    proxiedUrl('http://cdn.example.com/live.m3u8', TIER.RELAY, null),
    '/bridge/iptv?u=' + encodeURIComponent('http://cdn.example.com/live.m3u8'),
  );
});

// ---------------------------------------------------------------- probeTier --

test('probeTier reads the CORS header and returns a direct url when everything is clean', async () => {
  const fetchImpl = async (_uri, init) => {
    assert.equal(init.method, 'HEAD');
    return { headers: { get: (h) => (h.toLowerCase() === 'access-control-allow-origin' ? '*' : null) } };
  };
  const r = await probeTier('https://cdn.example.com/live.m3u8', { fetchImpl, pageProtocol: 'https:' });
  assert.deepEqual(r, { tier: TIER.DIRECT, blockedBy: null, url: 'https://cdn.example.com/live.m3u8' });
});

test('probeTier routes an http:// stream on an https page through the relay', async () => {
  const fetchImpl = async () => ({ headers: { get: () => null } });
  const r = await probeTier('http://cdn.example.com/live.ts', { fetchImpl, pageProtocol: 'https:' });
  assert.equal(r.tier, TIER.RELAY);
  assert.equal(r.blockedBy, FAILURE.MIXED_CONTENT);
  assert.equal(r.url, '/bridge/iptv?u=' + encodeURIComponent('http://cdn.example.com/live.ts'));
});

test('probeTier prefers the user gateway over the relay when one is configured', async () => {
  const fetchImpl = async () => ({ headers: { get: () => null } });
  const r = await probeTier('http://cdn.example.com/live.ts', {
    fetchImpl, pageProtocol: 'https:', gateway: 'http://192.168.0.118:8900',
  });
  assert.equal(r.tier, TIER.GATEWAY);
  assert.equal(r.url, 'http://192.168.0.118:8900/iptv?u=' + encodeURIComponent('http://cdn.example.com/live.ts'));
});

test('probeTier treats a network-level HEAD failure the same as a missing CORS header, not as a crash', async () => {
  const fetchImpl = async () => { throw new Error('network down'); };
  const r = await probeTier('https://cdn.example.com/live.m3u8', { fetchImpl, pageProtocol: 'https:' });
  assert.equal(r.blockedBy, FAILURE.CORS_BLOCKED);
  assert.equal(r.tier, TIER.RELAY);
});

test('probeTier reports no tier and a null url when neither gateway nor relay can help', async () => {
  const fetchImpl = async () => ({ headers: { get: () => null } });
  const r = await probeTier('http://cdn.example.com/live.ts', {
    fetchImpl, pageProtocol: 'https:', relayAvailable: false,
  });
  assert.deepEqual(r, { tier: null, blockedBy: FAILURE.MIXED_CONTENT, url: null });
});

test('probeTier never throws even if the fetchImpl itself is broken', async () => {
  const brokenFetch = () => { throw new TypeError('boom'); }; // synchronous throw, not a rejected promise
  await assert.doesNotReject(probeTier('https://cdn.example.com/live.m3u8', { fetchImpl: brokenFetch }));
});
