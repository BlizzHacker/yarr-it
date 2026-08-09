import test from 'node:test';
import assert from 'node:assert/strict';

import { musicBits, trackLength, entryLabel } from './music.js';
import { domainOfRow } from './home.js';

const concert = {
  title: 'Grateful Dead Live at Barton Hall, Cornell University on 1977-05-08',
  year: 1977,
  platform: 'Grateful Dead',
  music: {
    artist: 'Grateful Dead',
    form: 'concert',
    venue: 'Barton Hall, Cornell University',
    place: 'Ithaca, NY',
    date: '1977-05-08',
  },
};

test('a concert card shows where it was and when', () => {
  // Two Grateful Dead cards differ by nothing else. Without this the tile is
  // four hundred identical rows.
  assert.deepEqual(musicBits(concert), ['Barton Hall, Cornell University', '1977-05-08']);
});

test('a release shows its town and not a redundant day', () => {
  const single = {
    title: 'Crazy Blues',
    year: 1920,
    music: { artist: 'Mamie Smith', form: 'release', place: 'USA', date: '1920-08-10' },
  };
  // The year is already on the line above, so the day is noise for a single.
  assert.deepEqual(musicBits(single), ['USA']);
});

test('a card from any other domain is untouched', () => {
  assert.deepEqual(musicBits({ title: 'Chrono Trigger', platform: 'SNES' }), []);
  assert.deepEqual(musicBits(null), []);
  assert.deepEqual(musicBits({ music: {} }), []);
});

test('a track length reads the way a person writes one', () => {
  assert.equal(trackLength(381), '6:21');
  assert.equal(trackLength(4121), '1:08:41');
  assert.equal(trackLength(59), '0:59');
  // A track whose length nobody recorded shows nothing rather than claiming to
  // be instantaneous.
  assert.equal(trackLength(0), '');
  assert.equal(trackLength(undefined), '');
  assert.equal(trackLength(-5), '');
  assert.equal(trackLength('nonsense'), '');
});

test('a track is labelled with its number and length', () => {
  assert.equal(
    entryLabel({ uri: 'x', meta: { number: 1, title: 'Minglewood Blues', durationSeconds: 381 } }),
    '1. Minglewood Blues · 6:21',
  );
});

test('a playlist entry with neither is labelled exactly as before', () => {
  assert.equal(entryLabel({ uri: 'http://x/1.ts', meta: { title: 'BBC One' } }), 'BBC One');
  assert.equal(entryLabel({ uri: 'http://x/1.ts', meta: {} }), 'http://x/1.ts');
  assert.equal(entryLabel({ uri: 'http://x/1.ts' }), 'http://x/1.ts');
});

test('every music shelf lands under Music on the landing page', () => {
  // domainOfRow reads the key right to left and falls back to the title and
  // then to mediaType. All three keys have to resolve without needing the last
  // of those, because mediaType `audio` is also what the audiobooks row sends
  // -- and an audiobook belongs to literature.
  for (const key of ['ia-music-live', 'ia-music-early', 'ia-music-netlabels']) {
    assert.equal(domainOfRow({ key, title: 'x', items: [{ mediaType: 'audio' }] }), 'music', key);
  }
  // Still not music, and this is the row that proves the fallback is not being
  // relied on.
  assert.equal(
    domainOfRow({ key: 'ia-audiobooks', title: 'Audiobooks', items: [{ mediaType: 'audio' }] }),
    'literature',
  );
});
