import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  AUDIO, audioState, badges, downloadUrl, failureMessage, formatDuration,
  groupFormats, isOurFault, isRetryable, isSaveable, isSilent, playbackWarning,
  qualityLabel, sizeLabel, summarise,
} from './link-format.js';

// Shapes taken from real /api/link/resolve responses on 2026-08-08.
const youtubeSilent = {
  id: '299', label: '1080p60', ext: 'mp4', height: 1080, fps: 60,
  vcodec: 'H.264', hasVideo: true, hasAudio: false, audioKnown: true,
  bytes: 257619653, bytesKnown: true, sizeHuman: '245.7 MiB', media: '/api/link/media?t=x',
};
const facebookUnknown = {
  id: 'sd', label: 'SD', ext: 'mp4', hasVideo: true, hasAudio: false,
  audioKnown: false, sizeHuman: '92.8 KiB', media: '/api/link/media?t=y',
};
const complete = {
  id: '18', label: '360p', ext: 'mp4', height: 360, vcodec: 'H.264', acodec: 'AAC',
  hasVideo: true, hasAudio: true, audioKnown: true, sizeHuman: '27.2 MiB',
  media: '/api/link/media?t=z',
};
const joined = {
  id: '299', label: '1080p60', ext: 'mp4', height: 1080, vcodec: 'H.264',
  hasVideo: true, hasAudio: false, audioKnown: true, muxed: true, pairedWith: '139',
  sizeHuman: '~249.4 MiB', bytesEstimated: true, media: '/api/link/media?t=m',
};
const audioTrack = {
  id: '140', label: 'audio 129k', ext: 'm4a', acodec: 'AAC',
  hasVideo: false, hasAudio: true, audioKnown: true, sizeHuman: '9.8 MiB',
  media: '/api/link/media?t=a',
};
const bareManifest = {
  id: 'hls-1', label: '720p', ext: 'mp4', height: 720, hasVideo: true, hasAudio: true,
  audioKnown: true, streaming: true, sizeHuman: 'size unknown (stream)', media: '',
};

// ------------------------------------------------------- the audio promise --

// The distinction the whole feature rests on. "Not reported" is not "silent",
// and collapsing the two is how a working Facebook video gets labelled as
// having no sound.
test('audio has three states, not two', () => {
  assert.equal(audioState(complete), AUDIO.YES);
  assert.equal(audioState(youtubeSilent), AUDIO.NO);
  assert.equal(audioState(facebookUnknown), AUDIO.UNKNOWN);
  assert.equal(audioState(joined), AUDIO.JOINED);
});

test('only a confirmed absence counts as silent', () => {
  assert.equal(isSilent(youtubeSilent), true);
  assert.equal(isSilent(facebookUnknown), false, 'unreported audio must not be called silent');
  assert.equal(isSilent(complete), false);
  assert.equal(isSilent(joined), false, 'a joined track arrives with sound');
});

test('a silent video is badged loudly and nothing else is', () => {
  const silentTags = badges(youtubeSilent);
  assert.ok(silentTags.some((b) => b.text === 'NO SOUND' && b.kind === 'warn'));

  for (const f of [complete, facebookUnknown, joined, audioTrack]) {
    assert.ok(
      !badges(f).some((b) => b.text === 'NO SOUND'),
      `${f.id} was wrongly badged as having no sound`,
    );
  }
});

test('unreported audio is described as unreported, not as absent', () => {
  const tags = badges(facebookUnknown);
  const tag = tags.find((b) => b.text === 'audio not reported');
  assert.ok(tag, 'no badge explained that the codec was not reported');
  assert.equal(tag.kind, 'muted', 'an unknown is not an alarm');
  assert.match(tag.title, /almost certainly sound/);
});

test('a joined track says so and names the audio it was paired with', () => {
  const tag = badges(joined).find((b) => b.text === 'video + audio');
  assert.ok(tag);
  assert.match(tag.title, /audio track 139/);
});

test('an audio-only track is labelled audio only', () => {
  assert.ok(badges(audioTrack).some((b) => b.text === 'audio only'));
});

// ------------------------------------------------------------- the sizes ---

test('an estimated size keeps its ~ marker', () => {
  assert.equal(sizeLabel(joined), '~249.4 MiB');
  assert.equal(sizeLabel(complete), '27.2 MiB');
});

test('an unknown size says unknown rather than showing zero', () => {
  assert.equal(sizeLabel({ }), 'size unknown');
  assert.equal(sizeLabel(bareManifest), 'size unknown (stream)');
});

// -------------------------------------------------------- saving vs not ---

test('a bare manifest is not offered as a download', () => {
  assert.equal(isSaveable(bareManifest), false);
  assert.equal(downloadUrl(bareManifest), null,
    'a download button here saves a 3 KB playlist and calls it a video');
});

test('a saveable format gets a dl=1 download URL', () => {
  assert.equal(downloadUrl(complete), '/api/link/media?t=z&dl=1');
});

test('a stream the server will repack IS saveable', () => {
  const repacked = { ...bareManifest, muxed: true, media: '/api/link/media?t=r' };
  assert.equal(isSaveable(repacked), true);
  assert.equal(downloadUrl(repacked), '/api/link/media?t=r&dl=1');
});

// ----------------------------------------------------------- the grouping --

test('silent tracks are separated from the ones with sound', () => {
  const { complete: ok, audioOnly, silent } = groupFormats(
    [youtubeSilent, complete, audioTrack, joined, facebookUnknown],
  );
  assert.deepEqual(ok.map((f) => f.id), ['18', '299', 'sd'],
    'joined and unreported-audio tracks belong with the usable ones');
  assert.deepEqual(audioOnly.map((f) => f.id), ['140']);
  assert.deepEqual(silent.map((f) => f.id), ['299']);
  assert.equal(silent[0].muxed, undefined, 'only the un-joinable silent track is quarantined');
});

test('grouping an empty list does not throw', () => {
  const g = groupFormats();
  assert.deepEqual([g.complete, g.audioOnly, g.silent], [[], [], []]);
});

// ------------------------------------------------------------- the labels --

test('quality labels never fall back to the word HD', () => {
  assert.equal(qualityLabel(youtubeSilent), '1080p60');
  assert.equal(qualityLabel(facebookUnknown), 'SD');
  assert.equal(qualityLabel({ height: 720, fps: 30 }), '720p');
  assert.equal(qualityLabel({ height: 720, fps: 60 }), '720p60');
  assert.equal(qualityLabel({ id: 'weird' }), 'weird');
});

test('durations render as clock time', () => {
  assert.equal(formatDuration(635), '10:35');
  assert.equal(formatDuration(3723), '1:02:03');
  assert.equal(formatDuration(9), '0:09');
  assert.equal(formatDuration(0), '');
});

test('the summary counts formats and names the reader', () => {
  const s = summarise({ formats: [complete, audioTrack], extractor: 'youtube', duration: 635 });
  assert.match(s, /2 formats/);
  assert.match(s, /via youtube/);
  assert.match(s, /10:35/);
});

// ---------------------------------------------------------- codec warnings --

test('HEVC is flagged because most browsers cannot decode it', () => {
  const warn = playbackWarning({ hasVideo: true, vcodec: 'HEVC' });
  assert.match(warn, /Safari/);
  assert.match(warn, /still download fine/, 'a playback limit is not a download limit');
});

test('ordinary H.264 raises no warning', () => {
  assert.equal(playbackWarning(complete), null);
});

// -------------------------------------------------------------- failures ---

// "Could not download" is what the competitors say and it is useless. Every
// code has to produce a sentence that names what actually happened.
test('every failure code has a specific message', () => {
  const codes = [
    'unsupported_site', 'no_video_in_post', 'login_required', 'private',
    'not_found', 'geo_blocked', 'drm_protected', 'live_not_supported',
    'rate_limited', 'timeout', 'blocked_address', 'resolver_unavailable',
    'extractor_broken',
  ];
  const seen = new Set();
  for (const code of codes) {
    const msg = failureMessage({ code });
    assert.ok(msg.length > 20, `${code} has no real message`);
    assert.ok(!/could not download/i.test(msg), `${code} falls back to a useless message`);
    assert.ok(!seen.has(msg), `${code} reuses another code's message`);
    seen.add(msg);
  }
});

test('an Instagram photo post is explained, not reported as missing', () => {
  const msg = failureMessage({ code: 'no_video_in_post' });
  assert.match(msg, /photos/);
  assert.match(msg, /signed-in/);
  assert.ok(!/removed/.test(msg), 'the post exists; saying it is gone sends people to check the wrong thing');
});

test('our own breakage is owned rather than blamed on the link', () => {
  assert.equal(isOurFault({ code: 'extractor_broken' }), true);
  assert.equal(isOurFault({ code: 'resolver_unavailable' }), true);
  assert.equal(isOurFault({ code: 'private' }), false);
  assert.match(failureMessage({ code: 'extractor_broken' }), /not yours/);
});

test('only failures that might pass on a retry offer one', () => {
  assert.equal(isRetryable({ code: 'timeout' }), true);
  assert.equal(isRetryable({ code: 'rate_limited' }), true);
  assert.equal(isRetryable({ code: 'private' }), false,
    'a private post will still be private in ten seconds');
  assert.equal(isRetryable({ code: 'drm_protected' }), false);
});

test('an unknown code still yields the server sentence rather than nothing', () => {
  assert.equal(failureMessage({ code: 'something_new', error: 'the sky fell' }), 'the sky fell');
});
