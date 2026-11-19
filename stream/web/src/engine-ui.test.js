import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseQualities, releaseLine, libraryLine } from './engine-ui.js';

test('qualities keep the order typed, drop unknowns and repeats', () => {
  assert.deepEqual(parseQualities('1080P, 2160p 720p, banana, 1080p'), ['1080p', '2160p', '720p']);
  assert.deepEqual(parseQualities(''), []);
});

test('a release line says what a person compares on', () => {
  const line = releaseLine({ quality: '1080p', source: 'BluRay', codec: 'x265', size: 2 ** 31, seeders: 12, indexer: 'T' });
  assert.equal(line, '1080p · BluRay · x265 · 2.0 GB · 12 seeds · T');
  assert.match(releaseLine({}), /0 seeds/);
});

test('library lines count episodes for series and show quality for films', () => {
  assert.equal(libraryLine({ kind: 'series', state: 'requested', have: 3, total: 9 }), 'Wanted · 3/9 episodes');
  assert.equal(libraryLine({ kind: 'movie', state: 'available', quality: '1080p' }), 'On disk · 1080p');
});
