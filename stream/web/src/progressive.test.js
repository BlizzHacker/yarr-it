import test from 'node:test';
import assert from 'node:assert/strict';
import {
  mergeCards, cardKey, pollDelay, shouldKeepPolling, describeProgress,
  runSearch, sleeper, POLL_CEILING_MS,
} from './progressive.js';

// Progressive search, client side.
//
// The behaviour these pin down is the one a person notices: results appear and
// then more appear, nothing that was already on screen moves, and a search that
// was superseded stops rather than painting over its replacement.

test('cards already painted keep their positions', () => {
  const first = [{ key: 'a', title: 'A' }, { key: 'b', title: 'B' }];
  const { cards, added } = mergeCards(first, [
    { key: 'c', title: 'C' }, { key: 'a', title: 'A' },
  ]);

  assert.deepEqual(cards.map((c) => c.key), ['a', 'b', 'c']);
  assert.deepEqual(added.map((c) => c.key), ['c'],
    'only the genuinely new card is reported, so the grid can be appended to');
});

test('a card that arrives twice is drawn once', () => {
  const { cards } = mergeCards(
    [{ key: 'a' }],
    [{ key: 'a' }, { key: 'a' }],
  );
  assert.equal(cards.length, 1);
});

// archive.org results carry a key; a hand-built or legacy card may not. Two
// keyless results must still be two results.
test('cards with no key are told apart by title and year', () => {
  assert.notEqual(
    cardKey({ title: 'Dune', year: 1984 }),
    cardKey({ title: 'Dune', year: 2021 }),
  );
  const { cards } = mergeCards([], [
    { title: 'Dune', year: 1984 }, { title: 'Dune', year: 2021 },
  ]);
  assert.equal(cards.length, 2);
});

test('polling starts tight and backs off', () => {
  assert.ok(pollDelay(0) <= 400, 'the first collection has to be soon; archive.org lands early');
  assert.ok(pollDelay(5) > pollDelay(0));
  assert.ok(pollDelay(50) <= 2000, 'the ladder has a ceiling');
});

test('polling stops when the search is complete', () => {
  assert.equal(shouldKeepPolling({ complete: true, elapsedMs: 10 }), false);
});

// A job that never reports itself finished -- because the process restarted, or
// something in front of it is answering -- must not leave a tab polling forever.
test('polling stops at a ceiling even if the server never says complete', () => {
  assert.equal(shouldKeepPolling({ complete: false, elapsedMs: POLL_CEILING_MS + 1 }), false);
  assert.equal(shouldKeepPolling({ complete: false, elapsedMs: 1000 }), true);
});

// The count must never claim to be final while indexers are still out. "Six
// results" and "six results so far" are different claims.
test('an unfinished search says more is coming', () => {
  const p = describeProgress({ complete: false, sources: { indexers: 'pending' } });
  assert.equal(p.done, false);
  assert.match(p.text, /searching/i);
});

test('a finished search says nothing at all', () => {
  const p = describeProgress({ complete: true, sources: { indexers: 'ok', archive: 'ok' } });
  assert.equal(p.done, true);
  assert.equal(p.text, '');
});

// The requirement this exists for: with Prowlarr unreachable, a search must say
// so plainly rather than presenting a short list as a whole answer.
test('unreachable indexers are named, not hidden behind a small result count', () => {
  const p = describeProgress({ complete: false, sources: { indexers: 'unavailable' } });
  assert.equal(p.tone, 'degraded');
  assert.match(p.text, /indexers are unavailable/i);
  assert.equal(p.done, true, 'nothing more is coming, so the count is final');
});

test('an unconfigured indexer is distinguished from a broken one', () => {
  const p = describeProgress({ complete: true, sources: { indexers: 'not-configured' } });
  assert.match(p.text, /no torrent indexer is configured/i);
});

// ------------------------------------------------------------------ runSearch --

/** A fake clock: resolves immediately but still honours abort. */
const instantly = (_ms, { signal } = {}) => {
  if (signal && signal.aborted) {
    const e = new Error('aborted');
    e.name = 'AbortError';
    return Promise.reject(e);
  }
  return Promise.resolve();
};

test('results are painted on every arrival, not once at the end', async () => {
  const replies = [
    { job: 'j1', complete: false, cards: [{ key: 'a' }], sources: { indexers: 'pending' } },
    { job: 'j1', complete: false, cards: [{ key: 'a' }, { key: 'b' }], sources: { indexers: 'pending' } },
    { job: 'j1', complete: true, cards: [{ key: 'a' }, { key: 'b' }, { key: 'c' }], sources: { indexers: 'ok' } },
  ];
  let n = 0;
  const paints = [];

  const final = await runSearch({
    url: '/api/search?q=x',
    updatesURL: (job) => `/api/search/updates?job=${job}`,
    fetchJson: async () => replies[n++],
    wait: instantly,
    onPaint: ({ cards, added }) => paints.push({ total: cards.length, added: added.length }),
  });

  assert.equal(paints.length, 3, 'one paint per arrival');
  assert.deepEqual(paints.map((p) => p.total), [1, 2, 3]);
  assert.deepEqual(paints.map((p) => p.added), [1, 1, 1],
    'each paint adds only what is new; the grid is appended to, never rebuilt');
  assert.equal(final.complete, true);
});

test('a search that is aborted stops instead of painting over its replacement', async () => {
  const ctl = new AbortController();
  let calls = 0;

  await assert.rejects(
    runSearch({
      url: '/api/search?q=x',
      updatesURL: (job) => `/u?job=${job}`,
      fetchJson: async () => {
        calls++;
        ctl.abort();
        return { job: 'j1', complete: false, cards: [], sources: {} };
      },
      wait: instantly,
      signal: ctl.signal,
    }),
    (e) => e.name === 'AbortError',
  );
  assert.equal(calls, 1, 'no poll went out after the abort');
});

// A dropped poll is not a failed search. Whatever is painted stays painted.
test('one failed poll does not end the search', async () => {
  const replies = [
    { job: 'j1', complete: false, cards: [{ key: 'a' }], sources: {} },
    null, // this one throws
    { job: 'j1', complete: true, cards: [{ key: 'a' }, { key: 'b' }], sources: {} },
  ];
  let n = 0;
  const final = await runSearch({
    url: '/api/search?q=x',
    updatesURL: (job) => `/u?job=${job}`,
    fetchJson: async () => {
      const r = replies[n++];
      if (!r) throw new Error('network hiccup');
      return r;
    },
    wait: instantly,
  });
  assert.equal(final.complete, true);
  assert.equal(final.cards.length, 2);
});

// A job swept while a tab was in the background. What is on screen is still a
// real answer; it simply stopped growing.
test('a job that has expired ends the search without an error', async () => {
  const replies = [
    { job: 'j1', complete: false, cards: [{ key: 'a' }], sources: {} },
    { gone: true, error: 'that search has expired — run it again' },
  ];
  let n = 0;
  const paints = [];
  const final = await runSearch({
    url: '/api/search?q=x',
    updatesURL: (job) => `/u?job=${job}`,
    fetchJson: async () => replies[n++],
    wait: instantly,
    onPaint: ({ cards }) => paints.push(cards.length),
  });
  assert.deepEqual(paints, [1], 'the expired reply is not painted over the results');
  assert.equal(final.job, 'j1');
});

// A first response with no job is a cache hit: complete on arrival.
test('a complete first response is not polled', async () => {
  let calls = 0;
  await runSearch({
    url: '/api/search?q=x',
    updatesURL: () => '/u',
    fetchJson: async () => { calls++; return { complete: true, cards: [{ key: 'a' }] }; },
    wait: instantly,
  });
  assert.equal(calls, 1);
});

test('the sleeper rejects rather than resolving when aborted mid-wait', async () => {
  const ctl = new AbortController();
  const p = sleeper(5000, { signal: ctl.signal });
  ctl.abort();
  await assert.rejects(p, (e) => e.name === 'AbortError');
});
