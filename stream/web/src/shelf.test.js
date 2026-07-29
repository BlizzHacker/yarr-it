import test from 'node:test';
import assert from 'node:assert/strict';

import {
  keyFor, watchedFraction, isFinished, isResumable, trackProgress,
  getLibrary, getContinueWatching, isSignedIn,
} from './shelf.js';

test('keyFor is stable across casing and spacing', () => {
  assert.equal(keyFor({ key: '  TT-1375666 ' }), 'tt-1375666');
  // Without an id, title and year identify the work -- and two clients that
  // capitalise differently must not create two shelf entries.
  assert.equal(keyFor({ title: 'The Matrix', year: 1999 }), 't:the matrix-1999');
  assert.equal(keyFor({ title: 'the matrix', year: 1999 }),
    keyFor({ title: 'The Matrix', year: 1999 }));
});

test('watched fraction is clamped and safe without a duration', () => {
  assert.equal(watchedFraction(null), 0);
  assert.equal(watchedFraction({ position: 10, duration: 0 }), 0);
  assert.equal(watchedFraction({ position: 50, duration: 100 }), 0.5);
  assert.equal(watchedFraction({ position: 500, duration: 100 }), 1);
});

test('the last minutes count as finished, not as something to resume', () => {
  assert.ok(isFinished({ position: 95, duration: 100 }));
  assert.ok(!isResumable({ position: 95, duration: 100 }));
  // A glance is not a resume point either.
  assert.ok(!isResumable({ position: 5, duration: 100 }));
  assert.ok(isResumable({ position: 40, duration: 100 }));
});

// Being signed out is the normal case for a visitor, not an error.
test('a 401 yields an empty shelf rather than throwing', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response('', { status: 401 });
  try {
    assert.deepEqual(await getLibrary(), []);
    assert.deepEqual(await getContinueWatching(), []);
    assert.equal(isSignedIn(), false);
  } finally {
    globalThis.fetch = original;
  }
});

// A fake <video> good enough to drive the tracker.
function fakeMedia() {
  const listeners = {};
  return {
    currentTime: 0,
    duration: 6000,
    addEventListener(k, fn) { (listeners[k] ||= []).push(fn); },
    removeEventListener(k, fn) {
      listeners[k] = (listeners[k] || []).filter((f) => f !== fn);
    },
    emit(k) { (listeners[k] || []).forEach((fn) => fn()); },
    listenerCount(k) { return (listeners[k] || []).length; },
  };
}

test('the position is flushed on pause, not only on the throttle', async () => {
  const sent = [];
  const original = globalThis.fetch;
  globalThis.fetch = async (url, opts) => {
    if (String(url).includes('/api/v1/progress')) sent.push(JSON.parse(opts.body));
    return new Response(JSON.stringify({ ok: true }), { status: 200 });
  };
  try {
    const media = fakeMedia();
    const stop = trackProgress(media, { title: 'Dune', year: 2021 });

    // Ticking alone is throttled, so nothing should have gone yet beyond the
    // first eligible send.
    media.currentTime = 120;
    media.emit('timeupdate');
    const afterTick = sent.length;

    media.currentTime = 300;
    media.emit('pause');
    await new Promise((r) => setTimeout(r, 10));

    assert.ok(sent.length > afterTick,
      'pausing must flush the position; that is when someone walks away');
    assert.equal(sent.at(-1).position, 300);
    assert.equal(sent.at(-1).device, 'web');

    stop();
    assert.equal(media.listenerCount('timeupdate'), 0, 'listeners must be released');
  } finally {
    globalThis.fetch = original;
  }
});

test('tracking never throws when the shelf is unavailable', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => { throw new Error('offline'); };
  try {
    const media = fakeMedia();
    const stop = trackProgress(media, { title: 'X' });
    media.currentTime = 400;
    media.emit('pause');
    await new Promise((r) => setTimeout(r, 10));
    stop(); // must not reject or throw -- playback outranks bookkeeping
  } finally {
    globalThis.fetch = original;
  }
});

// Personal state changes under the page's feet; a cached read makes a delete
// look like it silently failed.
test('shelf reads bypass the HTTP cache', async () => {
  const seen = [];
  const original = globalThis.fetch;
  globalThis.fetch = async (url, opts) => {
    seen.push(opts?.cache);
    return new Response(JSON.stringify({ items: [] }), { status: 200 });
  };
  try {
    await getLibrary();
    await getContinueWatching();
    assert.ok(seen.length >= 2);
    assert.ok(seen.every((c) => c === 'no-store'),
      `cache modes were ${JSON.stringify(seen)}`);
  } finally {
    globalThis.fetch = original;
  }
});
