import { test } from 'node:test';
import assert from 'node:assert/strict';
import { urlResolver, renderForMime } from './url.js';
import { embedResolver, embedUrlFor } from './embed.js';
import { RENDER } from '../source.js';

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
