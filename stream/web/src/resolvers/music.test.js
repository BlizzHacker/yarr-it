import test from 'node:test';
import assert from 'node:assert/strict';

import { archiveResolver } from './archive.js';
import { urlResolver } from './url.js';
import { isCollection, makeSource, createRegistry } from '../source.js';
import { FAILURE } from '../failures.js';

/**
 * Opening a concert as music.
 *
 * The fixture is the Barton Hall show as /api/music/item actually answers for
 * it -- the stream-only marker, the mm:ss durations, the per-track titles.
 */
const bartonHall = {
  id: 'gd77-05-08.sbd.hicks.4982.sbeok.shnf',
  domain: 'music',
  type: 'album',
  form: 'concert',
  title: 'Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08',
  artist: 'Grateful Dead',
  album: '1977-05-08 - Barton Hall, Cornell University',
  year: 1977,
  date: '1977-05-08',
  venue: 'Barton Hall, Cornell University',
  place: 'Ithaca, NY',
  artwork: 'https://archive.org/services/img/gd77-05-08.sbd.hicks.4982.sbeok.shnf',
  details: 'https://archive.org/details/gd77-05-08.sbd.hicks.4982.sbeok.shnf',
  downloadable: false,
  totalSeconds: 913,
  tracks: [
    {
      number: 1,
      title: 'Minglewood Blues',
      artist: 'Grateful Dead',
      album: '1977-05-08 - Barton Hall, Cornell University',
      durationSeconds: 381,
      url: 'https://archive.org/download/gd77-05-08.sbd.hicks.4982.sbeok.shnf/gd77-05-08eaton-d1t01.mp3',
      mimeType: 'audio/mpeg',
      file: 'gd77-05-08eaton-d1t01.mp3',
    },
    {
      number: 2,
      title: 'Loser',
      artist: 'Grateful Dead',
      durationSeconds: 532,
      url: 'https://archive.org/download/gd77-05-08.sbd.hicks.4982.sbeok.shnf/gd77-05-08eaton-d1t02.mp3',
      mimeType: 'audio/mpeg',
      file: 'gd77-05-08eaton-d1t02.mp3',
    },
  ],
};

function stub(body, { ok = true, status = 200 } = {}) {
  const calls = [];
  const fetchImpl = async (url) => {
    calls.push(url);
    return { ok, status, json: async () => body };
  };
  return { fetchImpl, calls };
}

const musicSource = (id = bartonHall.id) =>
  makeSource({ kind: 'url', uri: `https://archive.org/details/${id}#music` });

test('a concert opens as a list of tracks rather than an iframe', async () => {
  const { fetchImpl, calls } = stub(bartonHall);
  const out = await archiveResolver.resolve(musicSource(), { fetchImpl, musicApi: '/api/music/item' });

  assert.ok(isCollection(out), 'a concert resolved to a single stream; it is twenty recordings');
  assert.equal(out.sources.length, 2);
  assert.equal(out.title, '1977-05-08 - Barton Hall, Cornell University');
  assert.match(calls[0], /^\/api\/music\/item\?id=gd77-05-08/);
});

test('each track carries what a track list has to show', async () => {
  const { fetchImpl } = stub(bartonHall);
  const out = await archiveResolver.resolve(musicSource(), { fetchImpl });

  const [first, second] = out.sources;
  assert.equal(first.meta.title, 'Minglewood Blues');
  assert.equal(first.meta.number, 1);
  assert.equal(first.meta.durationSeconds, 381);
  assert.equal(first.meta.artist, 'Grateful Dead');
  // The item's artist stands in where the file named none, so a track never
  // shows a blank where a name belongs.
  assert.equal(second.meta.artist, 'Grateful Dead');
  assert.equal(second.meta.album, '1977-05-08 - Barton Hall, Cornell University');
  assert.equal(first.meta.venue, 'Barton Hall, Cornell University');
  assert.equal(first.meta.date, '1977-05-08');
});

test('a stream-only item still plays, and says a download must not be offered', async () => {
  const { fetchImpl } = stub(bartonHall);
  const out = await archiveResolver.resolve(musicSource(), { fetchImpl });

  assert.equal(out.sources.length, 2, 'a stream-only item was refused outright');
  for (const s of out.sources) {
    assert.ok(s.uri.startsWith('https://archive.org/download/'), 'no bytes to play');
    assert.equal(s.meta.downloadable, false);
  }
});

test('each track resolves onward to an audio element, not another iframe', async () => {
  const { fetchImpl } = stub(bartonHall);
  const out = await archiveResolver.resolve(musicSource(), { fetchImpl });

  // The same registry order main.js builds: archive before url, so an
  // archive.org file URL is claimed here rather than falling through.
  const registry = createRegistry().register(archiveResolver).register(urlResolver);
  const playable = await registry.resolve(out.sources[0], { fetchImpl });

  assert.equal(playable.render, 'audio', `a track rendered as ${playable.render}`);
  assert.equal(playable.src, bartonHall.tracks[0].url);
});

test('an item with nothing playable says why instead of returning an empty list', async () => {
  const { fetchImpl } = stub({
    id: 'musopen-x',
    tracks: [],
    reason: 'this item holds nothing a browser can play',
  });
  await assert.rejects(
    () => archiveResolver.resolve(musicSource('musopen-x'), { fetchImpl }),
    (err) => {
      assert.equal(err.code, FAILURE.UNSUPPORTED_CODEC);
      assert.match(err.message, /nothing a browser can play/);
      return true;
    },
  );
});

test('a server that does not answer is a dead stream, not an empty album', async () => {
  const { fetchImpl } = stub(null, { ok: false, status: 502 });
  await assert.rejects(
    () => archiveResolver.resolve(musicSource(), { fetchImpl }),
    (err) => err.code === FAILURE.DEAD_STREAM,
  );

  const throwing = async () => { throw new Error('offline'); };
  await assert.rejects(
    () => archiveResolver.resolve(musicSource(), { fetchImpl: throwing }),
    (err) => err.code === FAILURE.DEAD_STREAM,
  );
});

test('a track with no url is dropped rather than played as undefined', async () => {
  const { fetchImpl } = stub({
    ...bartonHall,
    tracks: [{ title: 'Broken' }, bartonHall.tracks[0]],
  });
  const out = await archiveResolver.resolve(musicSource(), { fetchImpl });
  assert.equal(out.sources.length, 1);
  assert.equal(out.sources[0].meta.title, 'Minglewood Blues');
});

test('#music does not steal an ordinary archive.org link', async () => {
  const called = [];
  const fetchImpl = async (url) => {
    called.push(url);
    return { ok: true, status: 200, json: async () => bartonHall };
  };
  const plain = makeSource({ kind: 'url', uri: `https://archive.org/details/${bartonHall.id}` });
  const out = await archiveResolver.resolve(plain, { fetchImpl });

  assert.equal(out.render, 'embed', 'a plain details link stopped opening the archive player');
  assert.equal(called.length, 0, 'a plain details link cost a request it did not need');
});
