import { test } from 'node:test';
import assert from 'node:assert/strict';
import { urlResolver, renderForMime } from './url.js';
import { embedResolver, embedUrlFor } from './embed.js';
import { RENDER } from '../source.js';
import { FAILURE } from '../failures.js';

// A minimal stand-in for the real `Headers` interface: case-insensitive
// `get(name)`, backed by a plain object of header values.
function makeHeaders(headers = {}) {
  const lower = {};
  for (const [k, v] of Object.entries(headers)) lower[k.toLowerCase()] = v;
  return { get: (name) => lower[name.toLowerCase()] ?? null };
}

function stubResponse({ status = 200, headers = {} } = {}) {
  return { ok: status >= 200 && status < 300, status, headers: makeHeaders(headers) };
}

test('mime maps to the right render path', () => {
  assert.equal(renderForMime('video/mp4'), RENDER.VIDEO);
  assert.equal(renderForMime('audio/mpeg'), RENDER.AUDIO);
  assert.equal(renderForMime('image/png'), RENDER.IMAGE);
  assert.equal(renderForMime('application/pdf'), null);
});

test('url resolver claims http(s) but not magnets', () => {
  assert.equal(urlResolver.canHandle('https://a/b.mp4'), true);
  assert.equal(urlResolver.canHandle('magnet:?xt=urn:btih:abc'), false);
});

test('youtube watch, short and embed forms all become one embed url', () => {
  const want = 'https://www.youtube.com/embed/dQw4w9WgXcQ';
  assert.equal(embedUrlFor('https://www.youtube.com/watch?v=dQw4w9WgXcQ'), want);
  assert.equal(embedUrlFor('https://youtu.be/dQw4w9WgXcQ'), want);
  assert.equal(embedUrlFor('https://www.youtube.com/embed/dQw4w9WgXcQ'), want);
});

test('vimeo becomes a player embed url', () => {
  assert.equal(embedUrlFor('https://vimeo.com/123456789'),
    'https://player.vimeo.com/video/123456789');
});

test('a non-embeddable url is not claimed by the embed resolver', () => {
  assert.equal(embedUrlFor('https://example.com/x.mp4'), null);
  assert.equal(embedResolver.canHandle('https://example.com/x.mp4'), false);
});

test('embed resolver produces an embed playable', async () => {
  const p = await embedResolver.resolve({ uri: 'https://youtu.be/dQw4w9WgXcQ' });
  assert.equal(p.render, RENDER.EMBED);
  assert.equal(p.src, 'https://www.youtube.com/embed/dQw4w9WgXcQ');
});

test('a youtube-lookalike path on an unrelated host is not hijacked', () => {
  const evil = 'https://evil.com/youtube.com/watch?v=dQw4w9WgXcQ';
  assert.equal(embedUrlFor(evil), null);
  assert.equal(embedResolver.canHandle(evil), false);
});

test('youtube mobile host still embeds correctly', () => {
  assert.equal(embedUrlFor('https://m.youtube.com/watch?v=dQw4w9WgXcQ'),
    'https://www.youtube.com/embed/dQw4w9WgXcQ');
});

test('vimeo channels url embeds the trailing numeric id', () => {
  assert.equal(embedUrlFor('https://vimeo.com/channels/staffpicks/123456789'),
    'https://player.vimeo.com/video/123456789');
});

test('vimeo groups/videos url embeds the trailing numeric id', () => {
  assert.equal(embedUrlFor('https://vimeo.com/groups/x/videos/987654321'),
    'https://player.vimeo.com/video/987654321');
});

test('a malformed input returns null instead of throwing', () => {
  assert.doesNotThrow(() => embedUrlFor('not a url'));
  assert.equal(embedUrlFor('not a url'), null);
  assert.equal(embedResolver.canHandle('not a url'), false);
});

test('youtube watch url with extra query params before v still resolves', () => {
  assert.equal(embedUrlFor('https://www.youtube.com/watch?list=PL1&v=dQw4w9WgXcQ'),
    'https://www.youtube.com/embed/dQw4w9WgXcQ');
});

test('url resolver resolves a video content-type to a video playable', async () => {
  const fetchImpl = async () => stubResponse({ headers: { 'content-type': 'video/mp4' } });
  const p = await urlResolver.resolve({ uri: 'https://a/b.mp4' }, { fetchImpl });
  assert.equal(p.render, RENDER.VIDEO);
  assert.equal(p.src, 'https://a/b.mp4');
});

test('url resolver resolves audio and image content-types', async () => {
  const audioFetch = async () => stubResponse({ headers: { 'content-type': 'audio/mpeg' } });
  const audio = await urlResolver.resolve({ uri: 'https://a/b.mp3' }, { fetchImpl: audioFetch });
  assert.equal(audio.render, RENDER.AUDIO);

  const imageFetch = async () => stubResponse({ headers: { 'content-type': 'image/png' } });
  const image = await urlResolver.resolve({ uri: 'https://a/b.png' }, { fetchImpl: imageFetch });
  assert.equal(image.render, RENDER.IMAGE);
});

test('url resolver strips content-type parameters before matching', async () => {
  const fetchImpl = async () =>
    stubResponse({ headers: { 'content-type': 'video/mp4; charset=utf-8' } });
  const p = await urlResolver.resolve({ uri: 'https://a/b.mp4' }, { fetchImpl });
  assert.equal(p.render, RENDER.VIDEO);
});

test('url resolver matches mixed-case content-type', async () => {
  const fetchImpl = async () => stubResponse({ headers: { 'content-type': 'Video/MP4' } });
  const p = await urlResolver.resolve({ uri: 'https://a/b.mp4' }, { fetchImpl });
  assert.equal(p.render, RENDER.VIDEO);
});

test('url resolver throws UnsupportedCodec when there is no content-type header', async () => {
  const fetchImpl = async () => stubResponse({ headers: {} });
  await assert.rejects(
    urlResolver.resolve({ uri: 'https://a/b' }, { fetchImpl }),
    (err) => err.code === FAILURE.UNSUPPORTED_CODEC,
  );
});

test('url resolver throws UnsupportedCodec for an unrecognised content-type', async () => {
  const fetchImpl = async () => stubResponse({ headers: { 'content-type': 'application/pdf' } });
  await assert.rejects(
    urlResolver.resolve({ uri: 'https://a/b.pdf' }, { fetchImpl }),
    (err) => err.code === FAILURE.UNSUPPORTED_CODEC,
  );
});

test('url resolver throws DeadStream for a non-2xx response', async () => {
  const fetchImpl = async () => stubResponse({ status: 404 });
  await assert.rejects(
    urlResolver.resolve({ uri: 'https://a/missing.mp4' }, { fetchImpl }),
    (err) => err.code === FAILURE.DEAD_STREAM,
  );
});

test('url resolver throws DeadStream when fetch itself rejects', async () => {
  const fetchImpl = async () => {
    throw new Error('network down');
  };
  await assert.rejects(
    urlResolver.resolve({ uri: 'https://a/b.mp4' }, { fetchImpl }),
    (err) => err.code === FAILURE.DEAD_STREAM,
  );
});

test('url resolver falls back to a ranged GET when HEAD returns 405', async () => {
  let calls = 0;
  const fetchImpl = async (uri, init) => {
    calls += 1;
    if (init.method === 'HEAD') return stubResponse({ status: 405 });
    assert.equal(init.method, 'GET');
    assert.equal(init.headers.Range, 'bytes=0-0');
    return stubResponse({ headers: { 'content-type': 'video/mp4' } });
  };
  const p = await urlResolver.resolve({ uri: 'https://a/b.mp4' }, { fetchImpl });
  assert.equal(p.render, RENDER.VIDEO);
  assert.equal(calls, 2);
});
