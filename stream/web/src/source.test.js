import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  makeSource, makePlayable, makeCollection, isCollection,
  RENDER, TIER, createRegistry,
} from './source.js';

test('makePlayable rejects an unknown render kind', () => {
  assert.throws(
    () => makePlayable({ render: 'hologram', src: 'x', mime: 'video/mp4' }),
    /unknown render/,
  );
});

test('makePlayable defaults tier to direct and cleanup to a no-op', () => {
  const p = makePlayable({ render: RENDER.VIDEO, src: 'x', mime: 'video/mp4' });
  assert.equal(p.tier, TIER.DIRECT);
  assert.equal(typeof p.cleanup, 'function');
  p.cleanup();
});

test('isCollection distinguishes a list from a playable', () => {
  const c = makeCollection({ title: 'IPTV', sources: [] });
  const p = makePlayable({ render: RENDER.VIDEO, src: 'x', mime: 'video/mp4' });
  assert.equal(isCollection(c), true);
  assert.equal(isCollection(p), false);
});

test('registry picks the first resolver that claims the input', () => {
  const reg = createRegistry();
  reg.register({ name: 'no', canHandle: () => false, resolve: async () => null });
  reg.register({ name: 'yes', canHandle: (i) => i.startsWith('magnet:'), resolve: async () => null });
  assert.equal(reg.find('magnet:?xt=1').name, 'yes');
  assert.equal(reg.find('https://example.com'), null);
});

test('registry resolve throws a typed error when nothing handles the input', async () => {
  const reg = createRegistry();
  await assert.rejects(
    () => reg.resolve(makeSource({ kind: 'unknown', uri: 'wat://x' })),
    /no resolver/,
  );
});
