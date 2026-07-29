import test from 'node:test';
import assert from 'node:assert/strict';

import {
  srtToVtt, findSubtitles, languageOf, isSupportedSubtitle, isUnsupportedSubtitle,
} from './subtitles.js';

test('SRT timestamps become VTT timestamps', () => {
  const srt = [
    '1',
    '00:00:01,000 --> 00:00:04,000',
    'Hello.',
    '',
    '2',
    '00:01:12,500 --> 00:01:15,250',
    'Goodbye.',
  ].join('\n');

  const vtt = srtToVtt(srt);
  assert.ok(vtt.startsWith('WEBVTT\n'), 'must carry the WEBVTT header');
  assert.ok(vtt.includes('00:00:01.000 --> 00:00:04.000'));
  assert.ok(vtt.includes('00:01:12.500 --> 00:01:15.250'));
  assert.ok(!vtt.includes(','), 'no comma separators may survive');
  assert.ok(vtt.includes('Hello.') && vtt.includes('Goodbye.'));
});

test('CRLF and a BOM do not break the header', () => {
  const srt = '﻿1\r\n00:00:01,000 --> 00:00:02,000\r\nHi.\r\n';
  const vtt = srtToVtt(srt);
  // A BOM before WEBVTT makes the whole file invalid, and it is exactly what
  // Windows-authored subtitles ship with.
  assert.ok(vtt.startsWith('WEBVTT'), `header was ${JSON.stringify(vtt.slice(0, 12))}`);
  assert.ok(!vtt.includes('\r'));
});

test('already-VTT input is left alone', () => {
  const vtt = 'WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHi.\n';
  assert.equal(srtToVtt(vtt).trim(), vtt.trim());
});

test('short timestamps are padded to full VTT form', () => {
  const srt = '1\n00:01,000 --> 00:04,000\nHi.\n';
  const vtt = srtToVtt(srt);
  assert.ok(vtt.includes('00:00:01.000 --> 00:00:04.000'),
    `got ${JSON.stringify(vtt)}`);
});

test('language is read from the filename, not the folder', () => {
  assert.equal(languageOf('Movie.2021.English.srt'), 'English');
  assert.equal(languageOf('Movie.2021.spa.srt'), 'Spanish');
  assert.equal(languageOf('English Movies/Some.Film.fr.srt'), 'French');
  assert.equal(languageOf('Some.Film.srt'), 'Unknown');
});

test('only renderable subtitle formats are offered', () => {
  assert.ok(isSupportedSubtitle('a.srt'));
  assert.ok(isSupportedSubtitle('a.VTT'));
  assert.ok(!isSupportedSubtitle('a.mkv'));
  // ASS carries styling VTT cannot express; a mangled track is worse than none.
  assert.ok(isUnsupportedSubtitle('a.ass'));
  assert.ok(!isSupportedSubtitle('a.ass'));
});

test('subtitles are discovered and ranked, forced and SDH last', () => {
  const found = findSubtitles([
    { name: 'Film.2021.1080p.mkv' },
    { name: 'Film.2021.forced.eng.srt' },
    { name: 'Film.2021.spa.srt' },
    { name: 'Film.2021.eng.sdh.srt' },
    { name: 'Film.2021.eng.srt' },
    { name: 'Film.2021.styles.ass' },
  ]);

  assert.equal(found.length, 4, 'video and ASS must not be offered as subtitles');
  assert.equal(found[0].language, 'English');
  assert.equal(found[0].forced, false);
  assert.equal(found[0].sdh, false);
  assert.equal(found.at(-1).forced, true, 'forced tracks rank last');
  assert.ok(found.some((f) => f.language === 'Spanish'));
});

test('a torrent with no subtitles yields an empty list, not an error', () => {
  assert.deepEqual(findSubtitles([{ name: 'a.mkv' }, { name: 'b.nfo' }]), []);
  assert.deepEqual(findSubtitles(), []);
  assert.deepEqual(findSubtitles([null, undefined, {}]), []);
});
