import { test } from 'node:test';
import assert from 'node:assert/strict';
import { playlistResolver } from './playlist.js';
import { isCollection, RENDER } from '../source.js';
import { FAILURE } from '../failures.js';

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

test('does not claim a media url whose query string merely contains .m3u8', () => {
  assert.equal(
    playlistResolver.canHandle('https://cdn.example.com/movie.mp4?ref=http://x.com/live.m3u8'),
    false,
  );
});

test('does not claim a media url whose query string merely contains .m3u', () => {
  assert.equal(playlistResolver.canHandle('http://x/video.mp4?next=playlist.m3u'), false);
});

test('still claims genuine playlist urls carrying a query string, case-insensitively', () => {
  assert.equal(playlistResolver.canHandle('http://x/a.m3u8?token=1'), true);
  assert.equal(playlistResolver.canHandle('http://x/list.M3U'), true);
});

test('canHandle returns false and does not throw on non-url input', () => {
  assert.doesNotThrow(() => {
    assert.equal(playlistResolver.canHandle('not a url'), false);
  });
});

test('an empty body is a typed dead-stream failure, not a silent empty collection', async () => {
  await assert.rejects(
    playlistResolver.resolve(
      { uri: 'http://x/list.m3u' }, { fetchImpl: fakeFetch('') },
    ),
    (err) => {
      assert.equal(err.code, FAILURE.DEAD_STREAM);
      return true;
    },
  );
});

test('a header-only body is a typed dead-stream failure', async () => {
  await assert.rejects(
    playlistResolver.resolve(
      { uri: 'http://x/list.m3u' }, { fetchImpl: fakeFetch('#EXTM3U\n') },
    ),
    (err) => {
      assert.equal(err.code, FAILURE.DEAD_STREAM);
      return true;
    },
  );
});

test('a relative channel uri is resolved against the playlist url', async () => {
  const body = [
    '#EXTM3U',
    '#EXTINF:-1,Channel 1',
    'chan1.ts',
  ].join('\n');
  const out = await playlistResolver.resolve(
    { uri: 'http://host/dir/list.m3u' }, { fetchImpl: fakeFetch(body) },
  );
  assert.equal(isCollection(out), true);
  assert.equal(out.sources.length, 1);
  assert.equal(out.sources[0].uri, 'http://host/dir/chan1.ts');
});

test('a rejecting fetchImpl is a typed dead-stream failure', async () => {
  const failingFetch = async () => { throw new Error('network down'); };
  await assert.rejects(
    playlistResolver.resolve(
      { uri: 'http://x/list.m3u' }, { fetchImpl: failingFetch },
    ),
    (err) => {
      assert.equal(err.code, FAILURE.DEAD_STREAM);
      return true;
    },
  );
});
