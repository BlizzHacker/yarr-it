import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createTorrentResolver, renderForKind, normalizeMagnet } from './torrent.js';
import { RENDER } from '../source.js';
import { FAILURE } from '../failures.js';

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

test('normalizeMagnet appends trackers to a bare, lowercased infohash', () => {
  const magnet = normalizeMagnet('08ADA5A7A6183AAE1E09D831DF6748D566095A10');
  assert.match(magnet, /xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10/);
  assert.match(magnet, /&tr=/);
});

test('normalizeMagnet passes a full magnet URI through unchanged', () => {
  const input = 'magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=foo';
  assert.equal(normalizeMagnet(input), input);
});

test('cleanup() is a no-op when the engine has moved on to a different torrent', async () => {
  const torrentA = { id: 'a' };
  const torrentB = { id: 'b' };
  const engine = {
    torrent: torrentA,
    add(_uri, { onReady }) { onReady({ name: 'movie.mp4', streamURL: 'blob:a' }, torrentA); },
    destroyTorrent() { this.destroyed = true; },
  };
  const r = createTorrentResolver({ engine, classify: () => 'video' });
  const playable = await r.resolve({ uri: 'abc'.padEnd(40, '0') });

  engine.torrent = torrentB; // engine has since moved on
  playable.cleanup();
  assert.equal(engine.destroyed, undefined);
});

test('cleanup() destroys the torrent when it is still the engine\'s current one', async () => {
  const torrentA = { id: 'a' };
  const engine = {
    torrent: torrentA,
    add(_uri, { onReady }) { onReady({ name: 'movie.mp4', streamURL: 'blob:a' }, torrentA); },
    destroyTorrent() { this.destroyed = true; },
  };
  const r = createTorrentResolver({ engine, classify: () => 'video' });
  const playable = await r.resolve({ uri: 'abc'.padEnd(40, '0') });

  playable.cleanup();
  assert.equal(engine.destroyed, true);
});

test('resolve times out with DEAD_STREAM when the engine never calls back', async () => {
  const engine = { add() {} };
  const r = createTorrentResolver({ engine, timeoutMs: 20 });
  await assert.rejects(
    () => r.resolve({ uri: 'abc'.padEnd(40, '0') }),
    (err) => err.code === FAILURE.DEAD_STREAM,
  );
});

test('renderForKind falls back to video for unknown kinds', () => {
  assert.equal(renderForKind('other'), RENDER.VIDEO);
});

test('mime is derived from the file extension', async () => {
  const makeEngine = (name) => ({
    add(_uri, { onReady }) { onReady({ name, streamURL: 'blob:x' }, {}); },
  });
  const classify = () => 'video'; // irrelevant here; only .mime is under test

  const mp3 = createTorrentResolver({ engine: makeEngine('song.mp3'), classify });
  assert.equal((await mp3.resolve({ uri: 'abc'.padEnd(40, '0') })).mime, 'audio/mpeg');

  const mkv = createTorrentResolver({ engine: makeEngine('movie.mkv'), classify });
  assert.equal((await mkv.resolve({ uri: 'abc'.padEnd(40, '0') })).mime, 'video/x-matroska');

  const xyz = createTorrentResolver({ engine: makeEngine('file.xyz'), classify });
  assert.equal((await xyz.resolve({ uri: 'abc'.padEnd(40, '0') })).mime, '');
});

test('canHandle never throws on non-string input', () => {
  const r = createTorrentResolver({ engine: null });
  assert.equal(r.canHandle(null), false);
  assert.equal(r.canHandle(42), false);
  assert.equal(r.canHandle(undefined), false);
});
