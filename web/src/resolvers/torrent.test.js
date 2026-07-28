import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createTorrentResolver, renderForKind, normalizeMagnet, splitFileChoice } from './torrent.js';
import { RENDER, isCollection } from '../source.js';
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

// ---------------------------------------------------- no-service-worker fallback --

// In a browser where WebTorrent's service worker is unavailable (private
// windows, some webviews), engine._serverReady resolves false and
// file.streamURL points at a URL nothing will ever answer. This restores the
// blob-URL fallback that used to live in main.js's now-deleted attachMedia().
test('falls back to a blob url when the engine reports the service worker is unavailable', async () => {
  const fakeBlob = new Blob(['fake'], { type: 'video/mp4' });
  let blobCalls = 0;
  const torrentA = { id: 'a' };
  const engine = {
    torrent: torrentA,
    _serverReady: Promise.resolve(false),
    add(_uri, { onReady }) {
      onReady({
        name: 'movie.mp4',
        streamURL: 'http://sw-scope/deadbeef/movie.mp4',
        blob: async () => { blobCalls += 1; return fakeBlob; },
      }, torrentA);
    },
    destroyTorrent() { this.destroyed = true; },
  };
  const r = createTorrentResolver({ engine, classify: () => 'video' });
  const playable = await r.resolve({ uri: 'abc'.padEnd(40, '0') });

  assert.equal(blobCalls, 1);
  assert.equal(playable.src.startsWith('blob:'), true);
  assert.equal(playable.render, RENDER.VIDEO);

  playable.cleanup();
  // Both teardowns fire: the blob url this Playable created, and the
  // torrent it was built for (still the engine's current one).
  assert.equal(engine.destroyed, true);
});

test('falls back to a blob url when the service worker is ready but the file has no streamURL', async () => {
  const fakeBlob = new Blob(['x']);
  let blobCalls = 0;
  const engine = {
    _serverReady: Promise.resolve(true),
    add(_uri, { onReady }) {
      onReady({
        name: 'song.mp3',
        streamURL: null,
        blob: async () => { blobCalls += 1; return fakeBlob; },
      }, {});
    },
  };
  const r = createTorrentResolver({ engine, classify: () => 'audio' });
  const playable = await r.resolve({ uri: 'abc'.padEnd(40, '0') });

  assert.equal(blobCalls, 1);
  assert.equal(playable.src.startsWith('blob:'), true);
});

test('does not fall back to a blob when the engine never claims one way or the other about the service worker (existing fakes)', async () => {
  // Guards the fakes used by every test above this one: none of them set
  // _serverReady, and none of their file objects implement blob(). If the
  // fallback ever became the default for "unspecified" instead of only for
  // an explicit false, this would start throwing "file.blob is not a
  // function" across the whole suite.
  const engine = {
    torrent: {},
    add(_uri, { onReady }) { onReady({ name: 'movie.mp4', streamURL: 'blob:already-a-blob' }, {}); },
  };
  const r = createTorrentResolver({ engine, classify: () => 'video' });
  const playable = await r.resolve({ uri: 'abc'.padEnd(40, '0') });
  assert.equal(playable.src, 'blob:already-a-blob');
});

// --- choosing a file out of a pack ---------------------------------------

test('a file choice splits off the uri and survives a round trip', () => {
  const magnet = 'magnet:?xt=urn:btih:EB7C1B7623104466A65034A5554DA59AE6C4517A&dn=N64+pack';
  assert.deepEqual(splitFileChoice(magnet), { uri: magnet, index: null });
  assert.deepEqual(splitFileChoice(`${magnet}#n=17`), { uri: magnet, index: 17 });
  assert.deepEqual(splitFileChoice(''), { uri: '', index: null });
  assert.deepEqual(splitFileChoice(undefined), { uri: '', index: null });
});

// Without this, picking an entry out of a ROM pack falls through to the url
// resolver, which cannot play a magnet, and nothing happens.
test('a chosen file is still recognised as the torrent it came from', () => {
  const r = createTorrentResolver({ engine: {}, classify: () => 'rom' });
  const magnet = 'magnet:?xt=urn:btih:EB7C1B7623104466A65034A5554DA59AE6C4517A';
  assert.equal(r.canHandle(magnet), true);
  assert.equal(r.canHandle(`${magnet}#n=3`), true);
  assert.equal(r.canHandle('https://example.com/a.mp4#n=3'), false);
});

// A ROM pack's biggest file is not "the game", it is whichever game happened
// to be biggest -- so a pack must offer the list rather than pick for you.
test('a rom pack resolves to a list of games, not to one of them', async () => {
  const files = [
    { name: 'Super Mario 64 (USA).z64', length: 8_388_608 },
    { name: 'GoldenEye 007 (USA).z64', length: 12_582_912 },
    { name: 'Zelda OoT (USA).z64', length: 33_554_432 },
    { name: 'readme.nfo', length: 900 },
  ];
  const engine = {
    add(_uri, { onFiles }) {
      onFiles({ name: 'Best N64 games', files });
    },
  };
  const r = createTorrentResolver({ engine, classify: () => 'rom', timeoutMs: 0 });
  const out = await r.resolve({ uri: 'magnet:?xt=urn:btih:' + 'e'.repeat(40) });

  assert.equal(isCollection(out), true);
  assert.equal(out.sources.length, 3, 'three games, and not the readme');
  // Every entry has to carry the index back, or picking one is ambiguous.
  for (const s of out.sources) assert.match(s.uri, /#n=\d+$/);
  assert.match(out.sources[0].meta.title, /\.z64$/);
});

// One playable file is not a choice, so it must play rather than ask.
test('a single-file torrent does not ask which file you meant', async () => {
  let readied = null;
  const engine = {
    add(_uri, { onFiles, onReady }) {
      const t = { name: 'One Movie',
        files: [{ name: 'Movie.mkv', length: 3e9, streamURL: '/stream/Movie.mkv' }] };
      if (onFiles(t)) return;
      readied = t.files[0];
      onReady(t.files[0], t);
    },
  };
  const r = createTorrentResolver({ engine, classify: () => 'video', timeoutMs: 0 });
  const out = await r.resolve({ uri: 'magnet:?xt=urn:btih:' + 'a'.repeat(40) });
  assert.equal(isCollection(out), false);
  assert.equal(readied.name, 'Movie.mkv');
});

test('an index that is no longer in the torrent fails loudly', async () => {
  const engine = {
    add(_uri, { onFiles }) {
      onFiles({ name: 'pack', files: [{ name: 'a.z64', length: 10 }] });
    },
  };
  const r = createTorrentResolver({ engine, classify: () => 'rom', timeoutMs: 0 });
  await assert.rejects(
    () => r.resolve({ uri: `magnet:?xt=urn:btih:${'b'.repeat(40)}#n=99` }),
    /no longer in this torrent/,
  );
});
