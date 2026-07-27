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
