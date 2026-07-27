import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chooseTier } from './ladder.js';
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
