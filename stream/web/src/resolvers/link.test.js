import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  LinkError, createLinkResolver, looksLikeDirectMedia, playableForFormat,
  resolveLink, shouldResolveAsLink,
} from './link.js';
import { createRegistry, makeSource, RENDER, TIER } from '../source.js';
import { urlResolver } from './url.js';
import { embedResolver } from './embed.js';
import { playlistResolver } from './playlist.js';
import { flashResolver } from './flash.js';
import { gameResolver } from './game.js';

const okResponse = (body, status = 200) => ({
  ok: status >= 200 && status < 300,
  status,
  json: async () => body,
});

const resolved = {
  kind: 'video', title: 'Clip', extractor: 'youtube', best: 0,
  formats: [{
    id: '18', label: '360p', ext: 'mp4', hasVideo: true, hasAudio: true,
    audioKnown: true, media: '/api/link/media?t=abc',
  }],
};

// ------------------------------------------------------------- resolving ---

test('resolveLink asks the server and returns the parsed body', async () => {
  let seen;
  const fetchImpl = async (url) => { seen = url; return okResponse(resolved); };
  const out = await resolveLink('https://youtu.be/abc', { fetchImpl });
  assert.equal(out.title, 'Clip');
  assert.equal(seen, '/api/link/resolve?u=https%3A%2F%2Fyoutu.be%2Fabc');
});

test('the pasted URL is encoded, so one with a query does not truncate', async () => {
  let seen;
  const fetchImpl = async (url) => { seen = url; return okResponse(resolved); };
  await resolveLink('https://www.youtube.com/watch?v=abc&t=30', { fetchImpl });
  assert.ok(seen.endsWith('u=https%3A%2F%2Fwww.youtube.com%2Fwatch%3Fv%3Dabc%26t%3D30'));
});

// The server's own code has to survive to the UI, because that code is the
// entire difference between "your post is private" and "we are broken".
test('a server failure keeps its code, extractor and upstream detail', async () => {
  const fetchImpl = async () => okResponse({
    error: 'that post is private', code: 'private',
    extractor: 'Instagram', upstream: '[Instagram] x: private',
  }, 403);
  await assert.rejects(
    () => resolveLink('https://instagram.com/p/x/', { fetchImpl }),
    (err) => {
      assert.ok(err instanceof LinkError);
      assert.equal(err.code, 'private');
      assert.equal(err.extractor, 'Instagram');
      assert.match(err.upstream, /private/);
      return true;
    },
  );
});

test('a network failure is its own code, not a fake extractor breakage', async () => {
  const fetchImpl = async () => { throw new Error('offline'); };
  await assert.rejects(
    () => resolveLink('https://x.com/i/status/1', { fetchImpl }),
    (err) => err.code === 'network',
  );
});

test('a 200 with no formats is a failure, not an empty success', async () => {
  const fetchImpl = async () => okResponse({ kind: 'video', formats: [] });
  await assert.rejects(
    () => resolveLink('https://example.com/v', { fetchImpl }),
    (err) => err.code === 'no_formats',
  );
});

test('an abort propagates rather than becoming a network error', async () => {
  const fetchImpl = async () => {
    const e = new Error('aborted');
    e.name = 'AbortError';
    throw e;
  };
  await assert.rejects(
    () => resolveLink('https://example.com/v', { fetchImpl }),
    (err) => err.name === 'AbortError',
  );
});

// -------------------------------------------------------------- playing ---

test('a resolved format becomes a Playable with no extra probe', async () => {
  const p = playableForFormat(resolved.formats[0], 'video');
  assert.equal(p.render, RENDER.VIDEO);
  assert.equal(p.src, '/api/link/media?t=abc');
  assert.equal(p.mime, 'video/mp4');
  assert.equal(p.tier, TIER.DIRECT, 'our own origin is never proxied again');
});

test('an audio-only format renders as audio, not as a blank video element', () => {
  const p = playableForFormat({
    ext: 'm4a', hasVideo: false, hasAudio: true, media: '/api/link/media?t=a',
  });
  assert.equal(p.render, RENDER.AUDIO);
});

test('a repacked stream is announced as mp4, because that is what arrives', () => {
  const p = playableForFormat({
    ext: 'mp4', streaming: true, muxed: true, hasVideo: true, media: '/api/link/media?t=r',
  });
  assert.equal(p.mime, 'video/mp4');
});

test('a format with no media URL fails loudly rather than rendering nothing', () => {
  assert.throws(() => playableForFormat({ ext: 'mp4', hasVideo: true }), /playable URL/);
});

// The resolver must claim ONLY our own signed media URLs. Claiming more would
// silently take playback away from the torrent and playlist resolvers.
test('the resolver claims only /api/link/media URLs', () => {
  const r = createLinkResolver();
  assert.equal(r.canHandle('/api/link/media?t=abc'), true);
  for (const other of [
    'magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567',
    'https://www.youtube.com/watch?v=abc',
    'https://example.com/stream.m3u8',
    'https://example.com/video.mp4',
  ]) {
    assert.equal(r.canHandle(other), false, `wrongly claimed ${other}`);
  }
});

test('the resolver uses the format handed to it on the source meta', async () => {
  const r = createLinkResolver();
  const p = await r.resolve(makeSource({
    kind: 'link', uri: '/api/link/media?t=abc',
    meta: { format: resolved.formats[0], kind: 'video' },
  }));
  assert.equal(p.src, '/api/link/media?t=abc');
  assert.equal(p.render, RENDER.VIDEO);
});

test('a media URL with no format attached still plays', async () => {
  const r = createLinkResolver();
  const p = await r.resolve({ uri: '/api/link/media?t=abc', meta: {} });
  assert.equal(p.render, RENDER.VIDEO);
});

// --------------------------------------------------------------- routing ---

function fullRegistry() {
  return createRegistry()
    .register(embedResolver)
    .register(flashResolver)
    .register(gameResolver)
    .register(playlistResolver)
    .register(urlResolver);
}

// YouTube and Vimeo are the point of the feature: today the embed resolver
// claims them and shows an iframe, which is not a downloader.
test('links the generic resolvers would claim go to the link resolver instead', () => {
  const reg = fullRegistry();
  for (const uri of [
    'https://www.youtube.com/watch?v=aqz-KE-bpKQ',
    'https://youtu.be/aqz-KE-bpKQ',
    'https://vimeo.com/1212617765',
    'https://www.tiktok.com/@nasa/video/7670721000471891214',
    'https://www.instagram.com/reel/DMmxU4bKcsR/',
    'https://www.facebook.com/watch/?v=123',
    'https://x.com/SpaceX/status/2075621981488033835',
    'https://www.reddit.com/r/aww/comments/abc/x/',
    'https://www.twitch.tv/user/clip/Slug',
    'https://www.dailymotion.com/video/x7tpzb4',
  ]) {
    assert.equal(shouldResolveAsLink(uri, reg), true, `${uri} should use the link resolver`);
  }
});

// The specific resolvers already do something better with these, and routing
// them through a video extractor would break behaviour that works today.
test('playlists, flash and ROMs keep their existing resolvers', () => {
  const reg = fullRegistry();
  for (const uri of [
    'https://example.com/channels.m3u',
    'https://example.com/stream.m3u8',
    'https://example.com/game.swf',
    'https://example.com/mario.nes',
  ]) {
    assert.equal(shouldResolveAsLink(uri, reg), false, `${uri} must keep its own resolver`);
  }
});

test('a plain media file plays immediately rather than costing an extraction', () => {
  const reg = fullRegistry();
  assert.equal(shouldResolveAsLink('https://example.com/movie.mp4', reg), false);
  assert.equal(shouldResolveAsLink('https://example.com/song.mp3?x=1', reg), false);
  assert.equal(shouldResolveAsLink('https://example.com/pic.jpg', reg), false);
});

test('magnets and non-http input are never sent to the link resolver', () => {
  const reg = fullRegistry();
  assert.equal(shouldResolveAsLink('magnet:?xt=urn:btih:abc', reg), false);
  assert.equal(shouldResolveAsLink('not a url', reg), false);
  assert.equal(shouldResolveAsLink('', reg), false);
  assert.equal(shouldResolveAsLink(undefined, reg), false);
});

test('an already-resolved media URL is not resolved a second time', () => {
  assert.equal(shouldResolveAsLink('/api/link/media?t=abc', fullRegistry()), false);
});

test('direct-media detection ignores the query string', () => {
  assert.equal(looksLikeDirectMedia('https://cdn.example.com/a/b.mp4?sig=xyz'), true);
  assert.equal(looksLikeDirectMedia('https://example.com/watch?v=mp4'), false,
    'the extension has to be in the path, not the query');
  assert.equal(looksLikeDirectMedia('https://example.com/'), false);
});
