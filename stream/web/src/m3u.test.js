import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseM3U, looksLikeHls } from './m3u.js';

test('parses attribute soup, logos and groups', () => {
  const text = [
    '#EXTM3U',
    '#EXTINF:-1 tvg-id="bbc1" tvg-logo="http://x/l.png" group-title="UK",BBC One',
    'http://stream.example/bbc1',
  ].join('\n');
  const { entries } = parseM3U(text);
  assert.equal(entries.length, 1);
  assert.deepEqual(entries[0], {
    title: 'BBC One',
    uri: 'http://stream.example/bbc1',
    logo: 'http://x/l.png',
    group: 'UK',
  });
});

// Real playlists are filthy: a BOM, CRLF endings and blank lines are normal.
test('survives a BOM, CRLF line endings and blank lines', () => {
  const text = '﻿#EXTM3U\r\n\r\n#EXTINF:-1,Chan\r\nhttp://a/b\r\n';
  const { entries } = parseM3U(text);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].title, 'Chan');
  assert.equal(entries[0].uri, 'http://a/b');
});

test('an entry with no URI line is dropped rather than half-parsed', () => {
  const text = '#EXTM3U\n#EXTINF:-1,Orphan\n#EXTINF:-1,Real\nhttp://a/b\n';
  const { entries } = parseM3U(text);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].title, 'Real');
});

test('missing logo and group come back as empty strings, not undefined', () => {
  const { entries } = parseM3U('#EXTM3U\n#EXTINF:-1,Bare\nhttp://a/b\n');
  assert.equal(entries[0].logo, '');
  assert.equal(entries[0].group, '');
});

test('an HLS media manifest is not an IPTV playlist', () => {
  const hls = [
    '#EXTM3U',
    '#EXT-X-VERSION:3',
    '#EXT-X-TARGETDURATION:10',
    '#EXTINF:9.9,',
    'seg1.ts',
  ].join('\n');
  assert.equal(looksLikeHls(hls), true);
  assert.equal(looksLikeHls('#EXTM3U\n#EXTINF:-1,Chan\nhttp://a/b'), false);
});

test('an HLS master playlist is also HLS', () => {
  const master = '#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nlow.m3u8';
  assert.equal(looksLikeHls(master), true);
});

// Regression: real IPTV playlists routinely quote group-title values that
// contain commas (e.g. group-title="News, UK"). A naive "first comma on the
// line" split mistakes the comma inside the quoted attribute for the
// name/attribute separator and mangles both the title and misses the group.
test('a group-title containing a comma does not corrupt the channel name or the group', () => {
  const text = [
    '#EXTM3U',
    '#EXTINF:-1 tvg-logo="http://x/l.png" group-title="News, UK",BBC News, HD',
    'http://s/1',
  ].join('\n');
  const { entries } = parseM3U(text);
  assert.equal(entries.length, 1);
  assert.deepEqual(entries[0], {
    title: 'BBC News, HD',
    uri: 'http://s/1',
    logo: 'http://x/l.png',
    group: 'News, UK',
  });
});

// Regression: the channel name itself is free to contain commas once we're
// past the (unquoted) name/attribute separator - it must be preserved whole,
// not truncated at the first comma inside it.
test('a channel name that itself contains commas is preserved in full', () => {
  const text = '#EXTM3U\n#EXTINF:-1 group-title="UK",BBC One, Extra, Channel\nhttp://a/b\n';
  const { entries } = parseM3U(text);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].title, 'BBC One, Extra, Channel');
  assert.equal(entries[0].group, 'UK');
});

// Guard against the quote-scanning rewrite breaking the simplest case: an
// #EXTINF line with no attributes at all still parses correctly.
test('an #EXTINF line with no attributes at all still parses', () => {
  const text = '#EXTM3U\n#EXTINF:-1,Simple Channel\nhttp://a/b\n';
  const { entries } = parseM3U(text);
  assert.equal(entries.length, 1);
  assert.deepEqual(entries[0], {
    title: 'Simple Channel',
    uri: 'http://a/b',
    logo: '',
    group: '',
  });
});

// Guard against hangs/crashes on malformed input: an unterminated quote must
// not loop forever or throw. The scanner treats everything after an unclosed
// quote as still "inside quotes", so no unquoted comma is found and the
// title falls back to '' - deterministic, not a crash.
test('an unterminated quote on the #EXTINF line does not hang or throw', () => {
  const text = '#EXTM3U\n#EXTINF:-1 group-title="News,Chan\nhttp://a/b\n';
  assert.doesNotThrow(() => parseM3U(text));
  const { entries } = parseM3U(text);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].title, '');
  assert.equal(entries[0].group, '');
  assert.equal(entries[0].uri, 'http://a/b');
});
