import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createTorrentResolver, renderForKind } from './torrent.js';
import { RENDER } from '../source.js';

test('claims magnet links and bare infohashes only', () => {
  const r = createTorrentResolver({ engine: null });
  assert.equal(r.canHandle('magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10'), true);
  assert.equal(r.canHandle('08ada5a7a6183aae1e09d831df6748d566095a10'), true);
  assert.equal(r.canHandle('https://example.com/a.mp4'), false);
  assert.equal(r.canHandle('not a hash'), false);
});

test('engine file kinds map onto render paths', () => {
  assert.equal(renderForKind('video'), RENDER.VIDEO);
  assert.equal(renderForKind('audio'), RENDER.AUDIO);
  assert.equal(renderForKind('image'), RENDER.IMAGE);
});

test('resolve rejects when the engine reports no playable file', async () => {
  const engine = {
    add(_uri, { onError }) { onError(new Error('no playable media in this torrent')); },
  };
  const r = createTorrentResolver({ engine });
  await assert.rejects(
    () => r.resolve({ uri: 'magnet:?xt=urn:btih:abc' }),
    /no playable media/,
  );
});
