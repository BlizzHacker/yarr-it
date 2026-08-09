import test from 'node:test';
import assert from 'node:assert/strict';

import {
  fitVideo, keepVideoFitted, MAX_UPSCALE, CRISP_BELOW_WIDTH,
} from './videofit.js';

test('the measured case: 320x240 on a 1080p stage stops being 3% of the screen', () => {
  // The actual complaint. Nostalgia TV files are 320x240 to 496x368 and the
  // only rule that applied to them capped a picture without ever lifting one.
  const before = { width: 320, height: 240 };
  const out = fitVideo(320, 240, 1900, 940);
  assert.equal(out.width, 960);
  assert.equal(out.height, 720);
  assert.equal(out.scale, 3);
  assert.ok(out.width * out.height > before.width * before.height * 8,
    'the picture is an order of magnitude larger than it was');
});

test('the upscale is capped rather than filling at any cost', () => {
  // Beyond a point more magnification is not more picture, it is the same
  // picture with bigger flaws.
  const out = fitVideo(320, 240, 3840, 2160);
  assert.equal(out.scale, MAX_UPSCALE);
  assert.equal(out.width, 320 * MAX_UPSCALE);
});

test('a high-resolution source is left exactly as it was', () => {
  // 1920x1080 in a 1900x940 stage has to come DOWN, and by a fraction. This is
  // the case the fix must not touch: it was already correct.
  const out = fitVideo(1920, 1080, 1900, 940);
  assert.ok(out.scale < 1);
  assert.equal(out.height, 940);
  assert.equal(out.crisp, false, 'never nearest-neighbour on the way down');
});

test('nothing is ever cropped', () => {
  // A 4:3 broadcast in a 16:9 stage: contain, never cover. On old television
  // the captions live in the part cover would throw away.
  const out = fitVideo(320, 240, 1600, 400);
  assert.ok(out.width <= 1600 && out.height <= 400);
  // Whole pixels, so the shape is preserved to within a rounding of one.
  assert.ok(Math.abs(out.width / out.height - 320 / 240) < 0.01);
});

test('a whole-number scale is preferred when one is close', () => {
  // 320 into 1000 wide is 3.125x. One source pixel to an exact 3x3 block beats
  // three-and-an-eighth screen pixels per source pixel, which is what
  // shimmering is.
  const out = fitVideo(320, 240, 1000, 1000);
  assert.equal(out.scale, 3);
  assert.equal(out.snapped, true);
});

test('snapping only ever goes down', () => {
  // Rounding up would overflow the stage, and an overflowing video is clipped
  // by the player's own edges with no way to see what is missing.
  const out = fitVideo(320, 240, 1000, 1000);
  assert.ok(out.width <= 1000 && out.height <= 1000);
  const far = fitVideo(320, 240, 880, 880); // 2.75x -- too far from 2 to snap
  assert.equal(far.snapped, false);
  assert.ok(far.width <= 880);
});

test('crisp upscaling is asked for on small sources and nowhere else', () => {
  assert.equal(fitVideo(320, 240, 1900, 940).crisp, true);
  assert.equal(fitVideo(496, 368, 1900, 940).crisp, true, 'the largest TV file');
  // A 1280-wide source nudged up slightly: the browser's own filtering is
  // better than nearest-neighbour here, and asking for pixelated would make a
  // good picture worse.
  assert.equal(fitVideo(1280, 720, 1400, 800).crisp, false);
  assert.equal(fitVideo(CRISP_BELOW_WIDTH + 1, 480, 4000, 4000).crisp, false);
});

test('"keep original size" shrinks an oversized source but never lifts a small one', () => {
  const small = fitVideo(320, 240, 1900, 940, { mode: 'natural' });
  assert.equal(small.width, 320);
  assert.equal(small.scale, 1);
  // Still contained: the alternative is a picture running off the page.
  const big = fitVideo(3840, 2160, 1000, 500, { mode: 'natural' });
  assert.ok(big.width <= 1000 && big.height <= 500);
});

test('an unknown size means leave it alone, not size it to zero', () => {
  // videoWidth is 0 until metadata loads, which is the normal state for the
  // first frames of a torrent stream.
  for (const args of [[0, 0, 1900, 940], [320, 240, 0, 0], [320, 0, 100, 100]]) {
    assert.equal(fitVideo(...args).width, 0);
  }
});

// ------------------------------------------------------------------- element

/** The smallest thing that behaves like the bits of the DOM this touches. */
function fakeVideo(videoWidth, videoHeight) {
  const listeners = new Map();
  return {
    videoWidth, videoHeight,
    style: {},
    addEventListener(type, fn) { listeners.set(type, [...(listeners.get(type) || []), fn]); },
    removeEventListener(type, fn) {
      listeners.set(type, (listeners.get(type) || []).filter((f) => f !== fn));
    },
    fire(type) { for (const fn of listeners.get(type) || []) fn(); },
    listenerCount() { return [...listeners.values()].reduce((n, l) => n + l.length, 0); },
  };
}

function fakeStage(width, height) {
  return { getBoundingClientRect: () => ({ width, height }) };
}

test('the element is sized once metadata arrives, not before', () => {
  const video = fakeVideo(0, 0);
  const stop = keepVideoFitted(video, fakeStage(1900, 940));
  assert.equal(video.style.width, '', 'nothing to measure yet');

  video.videoWidth = 320;
  video.videoHeight = 240;
  video.fire('loadedmetadata');
  assert.equal(video.style.width, '960px');
  assert.equal(video.style.imageRendering, 'pixelated');
  stop();
});

test('stopping puts the element back exactly as it was', () => {
  // Otherwise the next source -- which may be a 4K film needing no help at all
  // -- inherits the last one's inline 960px.
  const video = fakeVideo(320, 240);
  const stop = keepVideoFitted(video, fakeStage(1900, 940));
  assert.equal(video.style.width, '960px');
  stop();
  assert.equal(video.style.width, '');
  assert.equal(video.style.height, '');
  assert.equal(video.style.imageRendering, '');
  assert.equal(video.listenerCount(), 0, 'and leaves no listeners behind');
});

test('a stream that changes resolution mid-play is re-fitted', () => {
  const video = fakeVideo(320, 240);
  const stop = keepVideoFitted(video, fakeStage(1900, 940));
  assert.equal(video.style.width, '960px');
  video.videoWidth = 1920;
  video.videoHeight = 1080;
  video.fire('resize');
  assert.equal(video.style.width, '1671px');
  assert.equal(video.style.imageRendering, '', 'and stops asking for pixelated');
  stop();
});

test('a missing element or stage is a no-op rather than a crash', () => {
  assert.doesNotThrow(() => keepVideoFitted(null, fakeStage(100, 100))());
  assert.doesNotThrow(() => keepVideoFitted(fakeVideo(320, 240), null)());
});
