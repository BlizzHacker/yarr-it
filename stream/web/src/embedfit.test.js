import test from 'node:test';
import assert from 'node:assert/strict';

import { fitEmbed, applyEmbedFit, NATURAL } from './embedfit.js';

test('a 300x150 embed actually fills a real screen', () => {
  // The complaint this exists for: 3% of a 1600x900 frame.
  const before = (NATURAL.w * NATURAL.h) / (1600 * 900);
  assert.ok(before < 0.04, 'sanity: the unscaled embed really is tiny');

  const fit = fitEmbed(1600, 860);
  const after = (fit.width * fit.height) / (1600 * 860);
  assert.ok(after > 0.7, `scaled embed covers ${Math.round(after * 100)}%, want most of the frame`);
  assert.ok(fit.scale >= 5, `scale ${fit.scale} is too timid`);
});

test('it contains rather than covers, so nothing is cropped', () => {
  // Cropping a game to fill the screen loses the HUD, which is where the score
  // and the remaining lives are.
  for (const [w, h] of [[1600, 860], [1280, 720], [800, 600], [375, 667], [3840, 2160]]) {
    const fit = fitEmbed(w, h);
    assert.ok(fit.width <= w + 1, `width ${fit.width} overflows a ${w} stage`);
    assert.ok(fit.height <= h + 1, `height ${fit.height} overflows a ${h} stage`);
  }
});

test('aspect ratio is preserved exactly', () => {
  const target = NATURAL.w / NATURAL.h;
  for (const [w, h] of [[1600, 860], [500, 1000], [1000, 500], [375, 667]]) {
    const fit = fitEmbed(w, h);
    const got = fit.width / fit.height;
    assert.ok(Math.abs(got - target) < 0.02, `${w}x${h} distorted the picture: ${got} vs ${target}`);
  }
});

test('a near-integer scale snaps down, because pixel art blurs otherwise', () => {
  // 300*4 = 1200 wide, 155*4 = 620 tall. A stage slightly larger should use 4,
  // not 4.05, so each source pixel maps to a clean 4x4 block.
  const fit = fitEmbed(1230, 640);
  assert.equal(fit.scale, 4);
  assert.equal(fit.snapped, true);
  // Snapping must only ever go DOWN, or it overflows the stage.
  assert.ok(fit.width <= 1230 && fit.height <= 640);
});

test('a scale nowhere near an integer is left alone', () => {
  const fit = fitEmbed(1000, 500);
  assert.equal(fit.snapped, false);
  assert.ok(fit.scale > 3 && fit.scale < 3.5, `scale ${fit.scale}`);
});

test('a portrait phone gets the biggest picture that fits its width', () => {
  const fit = fitEmbed(375, 667);
  assert.ok(fit.width <= 375);
  // Width-limited, so it should use nearly all of it rather than sitting tiny.
  assert.ok(fit.width > 300, `only ${fit.width}px of 375 used`);
});

test('a zero-sized stage does not produce NaN or a collapsed box', () => {
  // The stage measures 0x0 for one frame before layout settles, and a NaN
  // transform silently removes the element from the page.
  for (const [w, h] of [[0, 0], [0, 500], [500, 0], [NaN, NaN], [undefined, undefined]]) {
    const fit = fitEmbed(w, h);
    assert.ok(Number.isFinite(fit.scale) && fit.scale > 0, `scale ${fit.scale} for ${w}x${h}`);
    assert.ok(Number.isFinite(fit.width) && fit.width > 0);
  }
});

test('applying it scales the iframe rather than resizing it', () => {
  // Resizing the iframe would just give their fixed canvas more empty page to
  // sit in, which is exactly how it ended up at 3% of the frame.
  const style = () => ({ });
  const wrapper = { style: style() };
  const iframe = { style: style() };

  const fit = applyEmbedFit(wrapper, iframe, 1600, 860);

  assert.equal(iframe.style.width, '300px', 'the iframe must keep its natural width');
  assert.equal(iframe.style.height, '155px');
  assert.match(iframe.style.transform, /^scale\(/);
  assert.equal(iframe.style.transformOrigin, '0 0', 'any other origin shifts the picture off-centre');

  // The wrapper takes the scaled size, so the flex stage can centre it.
  assert.equal(wrapper.style.width, `${fit.width}px`);
  assert.equal(wrapper.style.height, `${fit.height}px`);
  assert.equal(wrapper.style.overflow, 'hidden', 'a scaled iframe paints outside its box');
  assert.equal(wrapper.style.margin, 'auto');
});

// --- the frame the Archive's own player gets --------------------------------

import { containBox, CRT, MAX_EMBED_WIDTH } from './embedfit.js';

/**
 * The clamp above was one half of the mistake; giving the frame the whole stage
 * was the other. Their canvas keeps its own size and place inside whatever
 * viewport it is handed, so a 16:9 stage produces a small picture against a wall
 * of black -- which is exactly the complaint. A 4:3 box, the shape of the
 * television every one of these machines drew to, is a room their layout has far
 * less space to be wrong in.
 */
test('the archive frame is 4:3 and never overflows, at every real size', () => {
  for (const [w, h] of [[1920, 1080], [1280, 720], [375, 667], [1600, 860], [3840, 2160]]) {
    const box = containBox(w, h);
    assert.ok(box.width <= w, `width ${box.width} overflows a ${w} stage`);
    assert.ok(box.height <= h, `height ${box.height} overflows a ${h} stage`);
    // Within a pixel of 4:3 -- the floor() can cost at most one.
    const ratio = box.width / box.height;
    assert.ok(Math.abs(ratio - CRT.w / CRT.h) < 0.01,
      `${w}x${h} produced ${box.width}x${box.height}, ratio ${ratio.toFixed(3)}`);
  }
});

// Measured on live archive.org, post-boot, 2026-08-08: their canvas is 512x480
// for NES, 704x446 for the 2600, 640x448 for Genesis and 640x400 for DOS --
// IDENTICAL at 1920x1080, 1280x720, 800x600 and 640x480. A bigger frame
// therefore buys more black around the same picture rather than a bigger one,
// which is the whole reason the cap exists.
test('the frame is capped, because a bigger one only buys more black', () => {
  const big = containBox(3840, 2160);
  assert.equal(big.width, MAX_EMBED_WIDTH);
  assert.equal(big.height, Math.floor(MAX_EMBED_WIDTH * 3 / 4));

  // At 1080p the cap turns a 512px canvas adrift in a 1440px box into a 1024px
  // window it nearly fills.
  const desktop = containBox(1920, 1080);
  assert.equal(desktop.width, MAX_EMBED_WIDTH);
  assert.ok(512 / desktop.width > 0.45,
    'their widest-measured canvas should fill most of the frame, not a fifth of it');

  // And the cap must stay above every canvas size measured, because below it
  // their player squashes the canvas horizontally instead of leaving space --
  // measured at a 400px viewport, where a 512px canvas rendered 400px wide.
  for (const measured of [512, 704, 640]) {
    assert.ok(MAX_EMBED_WIDTH > measured, `${measured}px canvas would be squashed`);
  }
});

test('a stage smaller than the cap still gets the largest box that fits', () => {
  const box = containBox(600, 400);
  assert.equal(box.height, 400, 'height-limited here, so the height should be used up');
  assert.equal(box.width, 533);
});

test('a phone in portrait gets a width-limited frame, not a sliver', () => {
  const box = containBox(375, 667);
  assert.equal(box.width, 375);
  assert.equal(box.height, 281);
});

test('a stage with no size yields no box rather than a negative one', () => {
  for (const [w, h] of [[0, 0], [-1, 100], [100, 0], [NaN, 100], ['', '']]) {
    assert.deepEqual(containBox(w, h), { width: 0, height: 0 });
  }
});
