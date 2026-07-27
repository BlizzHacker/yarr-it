import { test } from 'node:test';
import assert from 'node:assert/strict';
import { playlistResolver } from './playlist.js';
import { isCollection, RENDER } from '../source.js';

function fakeFetch(body) {
  return async () => ({ ok: true, text: async () => body });
}

test('a channel list resolves to a collection of sources', async () => {
  const body = [
    '#EXTM3U',
    '#EXTINF:-1 group-title="UK",BBC One',
    'http://s/bbc1',
    '#EXTINF:-1,ITV',
    'http://s/itv',
  ].join('\n');
  const out = await playlistResolver.resolve(
    { uri: 'http://x/list.m3u' }, { fetchImpl: fakeFetch(body) },
  );
  assert.equal(isCollection(out), true);
  assert.equal(out.sources.length, 2);
  assert.equal(out.sources[0].meta.title, 'BBC One');
  assert.equal(out.sources[0].uri, 'http://s/bbc1');
});

test('an hls manifest resolves to a video playable, not a collection', async () => {
  const body = '#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:9.9,\nseg1.ts';
  const out = await playlistResolver.resolve(
    { uri: 'http://x/live.m3u8' }, { fetchImpl: fakeFetch(body) },
  );
  assert.equal(isCollection(out), false);
  assert.equal(out.render, RENDER.VIDEO);
  assert.equal(out.mime, 'application/vnd.apple.mpegurl');
});

test('claims .m3u and .m3u8 urls including ones with a query string', () => {
  assert.equal(playlistResolver.canHandle('http://x/a.m3u'), true);
  assert.equal(playlistResolver.canHandle('http://x/a.m3u8?token=1'), true);
  assert.equal(playlistResolver.canHandle('http://x/a.mp4'), false);
});
