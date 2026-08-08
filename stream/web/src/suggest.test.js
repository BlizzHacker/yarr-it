import test from 'node:test';
import assert from 'node:assert/strict';
import { createSuggester, suggestKey, suggestionHint } from './suggest.js';

// Type-ahead.
//
// The defect this guards against is not "suggestions are slow" -- it is that a
// keystroke leaves work behind. A superseded request that is merely ignored
// still runs, still occupies a connection, and still wins the race if it
// happens to finish last.

const tick = (ms) => new Promise((r) => setTimeout(r, ms));

test('typing again cancels the request the previous keystroke started', async () => {
  const aborted = [];
  const s = createSuggester({
    debounceMs: 5,
    fetchJson: (url, { signal }) => new Promise((resolve) => {
      signal.addEventListener('abort', () => aborted.push(url));
      setTimeout(() => resolve({ suggestions: [{ title: url }] }), 60);
    }),
  });

  const first = s.query('zel');
  await tick(20); // long enough for the first request to be in flight
  const second = s.query('zeld');

  assert.equal(await first, null, 'the superseded request resolves to null, not to stale data');
  assert.ok(aborted.length >= 1, 'the in-flight request was aborted, not merely ignored');
  await second;
  s.cancel();
});

test('a keystroke inside the debounce window never becomes a request', async () => {
  let requests = 0;
  const s = createSuggester({
    debounceMs: 40,
    fetchJson: async () => { requests++; return { suggestions: [] }; },
  });

  s.query('z');
  s.query('ze');
  s.query('zel');
  const last = s.query('zeld');
  await last;

  assert.equal(requests, 1, 'four keystrokes made one request');
  s.cancel();
});

test('a response that arrives after a newer request is discarded', async () => {
  const drawn = [];
  let n = 0;
  const s = createSuggester({
    debounceMs: 1,
    // The first response is slow, the second fast -- so without the generation
    // check the stale one would arrive last and win. This fake deliberately
    // ignores the abort signal, because a real fetch that has already reached
    // the `await res.json()` stage does too.
    fetchJson: () => {
      const mine = n++;
      return new Promise((resolve) => setTimeout(
        () => resolve({ suggestions: [{ title: `r${mine}` }] }), mine === 0 ? 80 : 5,
      ));
    },
    onResult: ({ suggestions }) => drawn.push(suggestions[0]?.title),
  });

  s.query('zel');
  await tick(10);
  s.query('zeld');
  await tick(150);

  assert.deepEqual(drawn, ['r1'], 'only the newest response was drawn');
  s.cancel();
});

test('too few characters asks nobody', async () => {
  let requests = 0;
  const s = createSuggester({
    debounceMs: 1, minChars: 2,
    fetchJson: async () => { requests++; return { suggestions: [] }; },
  });
  assert.deepEqual(await s.query('z'), []);
  await tick(20);
  assert.equal(requests, 0);
});

// A suggestion list that cannot be drawn is a missing convenience, not a
// failure worth putting over a page somebody is typing into.
test('a failed suggestion request is swallowed rather than shown', async () => {
  let shown = false;
  const s = createSuggester({
    debounceMs: 1,
    fetchJson: async () => { throw new Error('offline'); },
    onResult: () => { shown = true; },
  });
  assert.equal(await s.query('zelda'), null);
  assert.equal(shown, false);
});

// ------------------------------------------------------------------ keyboard --

test('the arrow keys wrap around the list', () => {
  assert.deepEqual(suggestKey('ArrowDown', { index: -1, count: 3 }), { index: 0, action: 'move' });
  assert.deepEqual(suggestKey('ArrowDown', { index: 2, count: 3 }), { index: 0, action: 'move' });
  assert.deepEqual(suggestKey('ArrowUp', { index: 0, count: 3 }), { index: 2, action: 'move' });
});

// The important one. Enter with nothing highlighted has to stay an ordinary
// search for what was typed -- swallowing it makes the button the only way to
// search, which is worse than having no suggestions at all.
test('Enter with nothing highlighted is not handled by the list', () => {
  assert.equal(suggestKey('Enter', { index: -1, count: 3 }), null);
  assert.deepEqual(suggestKey('Enter', { index: 1, count: 3 }), { index: 1, action: 'choose' });
});

test('an ordinary letter is left alone so typing still works', () => {
  assert.equal(suggestKey('a', { index: 0, count: 3 }), null);
});

test('nothing is handled when the list is empty', () => {
  assert.equal(suggestKey('ArrowDown', { index: -1, count: 0 }), null);
});

test('Escape closes without choosing', () => {
  assert.deepEqual(suggestKey('Escape', { index: 1, count: 3 }), { index: -1, action: 'close' });
});

// ---------------------------------------------------------------------- hint --

test('a suggestion says where it came from', () => {
  assert.match(suggestionHint({ title: 'Zelda', source: 'archive', platform: 'NES' }), /NES/);
  assert.match(suggestionHint({ title: 'Zelda', source: 'archive' }), /plays instantly/);
  assert.match(suggestionHint({ title: 'Dune', year: 2021, source: 'results' }), /2021/);
  assert.match(suggestionHint({ title: 'Dune', source: 'catalogue' }), /catalogue/);
});
