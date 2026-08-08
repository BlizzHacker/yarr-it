import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  pagesPath, embedFallbackUrl,
  createPager, canGoNext, canGoPrev, pagerLabel, notePage,
} from './reader.js';

test('a comic asks for derived pages; a picture set asks for the item files', () => {
  // kind=comic short-circuits the metadata call on the server and returns
  // derived page images, which is what makes a PDF-only scan viewable at all.
  assert.equal(pagesPath('SpaceOdyssey_819', 'read'), '/api/pages?id=SpaceOdyssey_819&kind=comic');
  assert.equal(pagesPath('amonguscraft_202010', 'view'), '/api/pages?id=amonguscraft_202010');
});

test('an identifier with characters a URL cares about is encoded, not pasted in', () => {
  assert.equal(pagesPath('a b&c', 'view'), '/api/pages?id=a+b%26c');
  assert.equal(embedFallbackUrl('a b&c'), 'https://archive.org/embed/a%20b%26c');
});

// --- how far a comic goes -------------------------------------------------
//
// /api/pages sends `probe: true` and documents that the client should stop at
// the first page that fails to load. Measured against archive.org, that never
// happens: a page past the end returns HTTP 200 carrying page 0's bytes.
//
// Recognising the repeat by its decoded size -- the only thing readable from a
// cross-origin image -- was tried and removed. These two tests pin why.

test('a comic whose pages all share one trim size is not cut off at page two', () => {
  // i-have-no-mouth-and-i-must-scream_202202: n0 and n1 both decode 1233x1595,
  // and it runs to roughly thirty pages. Ending on a size match reported
  // "Page 1 of 1" for the whole book.
  const p = createPager(new Array(60).fill('u'), { probe: true });
  assert.equal(notePage(p, 0, { w: 1233, h: 1595 }), true);
  assert.equal(notePage(p, 1, { w: 1233, h: 1595 }), true);
  p.index = 1;
  assert.equal(canGoNext(p), true);
});

test('a picture set is not truncated by two pictures sharing a size either', () => {
  // amonguscraft_202010 is eight Minecraft screenshots, the first two both
  // 700x394. The server listed the item's own files, so the count is exact.
  const p = createPager(new Array(8).fill('u'), { probe: false });
  notePage(p, 0, { w: 700, h: 394 });
  assert.equal(notePage(p, 1, { w: 700, h: 394 }), true);
  p.index = 1;
  assert.equal(canGoNext(p), true);
  assert.equal(pagerLabel(p), 'Page 2 of 8');
});

test('a page that genuinely fails to load ends the item there', () => {
  const p = createPager(new Array(60).fill('u'), { probe: true });
  notePage(p, 0, { w: 1233, h: 1595 });
  assert.equal(notePage(p, 12, null), false);
  assert.equal(p.end, 12);
  p.index = 11;
  assert.equal(canGoNext(p), false);
});

// --- paging ---------------------------------------------------------------

test('a probed comic never quotes a total, because nobody knows it', () => {
  // The 60 URLs are how many pages might exist, not how many do. "Page 4 of
  // 60" for a thirty-page book is a specific claim, and it is wrong.
  const p = createPager(new Array(60).fill('u'), { probe: true });
  assert.equal(pagerLabel(p), 'Page 1');
  p.index = 3;
  assert.equal(pagerLabel(p), 'Page 4');
});

test('a picture set knows its own length, so it says so', () => {
  const p = createPager(['a', 'b', 'c'], { probe: false });
  assert.equal(pagerLabel(p), 'Page 1 of 3');
});

test('the first page failing means there are no pages, not a one-page book', () => {
  const p = createPager(new Array(60).fill('u'), { probe: true });
  assert.equal(notePage(p, 0, { w: 0, h: 0 }), false);
  assert.equal(p.end, 0);
  assert.equal(canGoNext(p), false);
});

test('Previous is off on the first page and on after that', () => {
  const p = createPager(['a', 'b'], {});
  assert.equal(canGoPrev(p), false);
  p.index = 1;
  assert.equal(canGoPrev(p), true);
});

test('an item with no pages at all can go nowhere', () => {
  const p = createPager([], { probe: true });
  assert.equal(canGoNext(p), false);
  assert.equal(canGoPrev(p), false);
});
