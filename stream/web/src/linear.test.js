import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  LINEAR_STATE, LINEAR_URI_PREFIX, linearURI, normaliseChannels, fetchChannels, fetchNow,
  describeChannel, nextPollDelay, positionLine, nextLine, minutes, clockTime,
  tuneIn, playableFor, linearResolver, createCard, mountLinear,
  HEARTBEAT_MS, WARMING_POLL_MS, MIN_POLL_MS,
} from './linear.js';
import { PlaybackError } from './failures.js';

// The exact shapes the live server returned on 2026-08-08, pinned as data.
// Everything in this file is only worth anything if it survives the actual
// bytes production sends -- the schedule arithmetic in particular, which is
// invisible when it is wrong because both halves stay internally consistent.

const CHANNELS_BODY = {
  channels: [
    {
      id: 'nostalgia-cartoons',
      number: 901,
      name: 'Nostalgia Cartoons',
      source: 'NATIVE_YARR',
      providerId: 'yarr-linear',
      definition: { id: 'nostalgia-cartoons', enabled: true, description: 'Public-domain animation.' },
      status: 'on-air',
      scheduledPrograms: 608,
    },
    {
      id: 'nostalgia-classic-tv',
      number: 902,
      name: 'Nostalgia Classic TV',
      source: 'NATIVE_YARR',
      providerId: 'yarr-linear',
      definition: { id: 'nostalgia-classic-tv', enabled: true, description: 'Public-domain television.' },
      status: 'on-air',
      scheduledPrograms: 160,
    },
  ],
};

const NOW_ON_AIR = {
  channel: { id: 'nostalgia-classic-tv', number: 902, name: 'Nostalgia Classic TV' },
  program: {
    channelId: 'nostalgia-classic-tv',
    startTime: 1786235118,
    endTime: 1786236610,
    canonicalId: 'ia:Beverly_Hillbillies_Ep01/BH01.mp4',
    title: 'Beverly Hillbillies Ep01 The Clampetts Strike Oil',
    artwork: 'https://archive.org/services/img/Beverly_Hillbillies_Ep01',
  },
  next: {
    channelId: 'nostalgia-classic-tv',
    startTime: 1786236610,
    endTime: 1786238104,
    title: 'The Big Frank',
  },
  offsetSeconds: 832,
  remainingSeconds: 660,
  state: 'on-air',
  serverNow: 1786235950,
};

// A channel whose pool is still being fetched. This is the state
// nostalgia-cartoons was in for its first ~90 seconds, and the one that used to
// have nothing at all drawn for it.
const NOW_WARMING = {
  channel: { id: 'nostalgia-cartoons', number: 901, name: 'Nostalgia Cartoons' },
  state: 'empty',
  detail: 'this channel is still loading its programmes from archive.org; it will start playing shortly',
  offsetSeconds: 0,
  remainingSeconds: 0,
  serverNow: 1786235950,
};

const TUNE_OK = {
  channel: { id: 'nostalgia-classic-tv', name: 'Nostalgia Classic TV' },
  program: NOW_ON_AIR.program,
  offsetSeconds: 1098,
  state: 'on-air',
  serverNow: 1786236216,
  source: {
    url: 'https://archive.org/download/Beverly_Hillbillies_Ep01/BH01.mp4#t=1098',
    mimeType: 'video/mp4',
    directPlay: true,
    seekable: true,
  },
  available: true,
};

const TUNE_UNAVAILABLE = {
  channel: { id: 'nostalgia-cartoons', name: 'Nostalgia Cartoons' },
  state: 'on-air',
  detail: '"Popeye meets Sinbad" cannot be played right now: the source is unavailable',
  available: false,
};

const ok = (body) => ({ ok: true, status: 200, json: async () => body });

// ------------------------------------------------------------ channel list --

test('the two public channels survive the live listing shape', () => {
  const list = normaliseChannels(CHANNELS_BODY);
  assert.deepEqual(list.map((c) => c.id), ['nostalgia-cartoons', 'nostalgia-classic-tv']);
  assert.equal(list[1].number, 902);
  assert.equal(list[1].name, 'Nostalgia Classic TV');
});

test('a channel its owner switched off is not published to a stranger', () => {
  const list = normaliseChannels({
    channels: [
      { id: 'a', name: 'A', status: 'disabled' },
      { id: 'b', name: 'B', status: 'on-air', definition: { enabled: false } },
      { id: 'c', name: 'C', status: 'on-air' },
    ],
  });
  assert.deepEqual(list.map((c) => c.id), ['c']);
});

test('a channel service that is down produces no channels rather than an error', async () => {
  for (const impl of [
    () => Promise.reject(new Error('offline')),
    async () => ({ ok: false, status: 502, json: async () => ({}) }),
    async () => ({ ok: true, status: 200, json: async () => { throw new Error('not json'); } }),
  ]) {
    assert.deepEqual(await fetchChannels({ fetchImpl: impl }), []);
  }
});

test('an unanswerable now is null, not a thrown request', async () => {
  assert.equal(await fetchNow('x', { fetchImpl: () => Promise.reject(new Error('nope')) }), null);
  assert.equal(await fetchNow('x', { fetchImpl: async () => ({ ok: false, status: 404 }) }), null);
});

test('the now request names the channel and nothing else', async () => {
  const seen = [];
  await fetchNow('nostalgia-classic-tv', {
    fetchImpl: async (url) => { seen.push(url); return ok(NOW_ON_AIR); },
  });
  assert.equal(seen[0], '/api/v1/linear/now?channel=nostalgia-classic-tv');
  // No credentials, no key, no session: these channels are for people who have
  // none of those, which is the whole reason they are the first thing on the page.
  assert.ok(!seen[0].includes('token') && !seen[0].includes('key'));
});

// -------------------------------------------------------------- describing --

const chan = (id) => normaliseChannels(CHANNELS_BODY).find((c) => c.id === id);

test('a live channel reports the programme, the offset and what is next', () => {
  const m = describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR,
    { receivedAt: 1000, at: 1000 });
  assert.equal(m.live, true);
  assert.equal(m.watchable, true);
  assert.equal(m.kicker, 'On now');
  assert.equal(m.title, 'Beverly Hillbillies Ep01 The Clampetts Strike Oil');
  assert.equal(Math.round(m.offsetSeconds), 832);
  assert.equal(Math.round(m.remainingSeconds), 660);
  assert.equal(Math.round(m.durationSeconds), 1492);
  assert.ok(Math.abs(m.progress - 832 / 1492) < 1e-9);
  assert.equal(m.next.title, 'The Big Frank');
});

// The bar has to move between polls or it is a still picture of a live thing.
test('the position advances on the local clock between polls', () => {
  const at0 = describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR,
    { receivedAt: 5_000, at: 5_000 });
  const at30 = describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR,
    { receivedAt: 5_000, at: 35_000 });
  assert.equal(Math.round(at30.offsetSeconds - at0.offsetSeconds), 30);
  assert.equal(Math.round(at0.remainingSeconds - at30.remainingSeconds), 30);
  assert.ok(at30.progress > at0.progress);
});

// The one that is invisible when it is wrong: a viewer's clock being out must
// not move the channel, because the schedule is the server's.
test("a viewer's wrong clock cannot move the channel", () => {
  const early = describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR,
    { receivedAt: 0, at: 0 });
  const late = describeChannel(chan('nostalgia-classic-tv'),
    { ...NOW_ON_AIR }, { receivedAt: 10_000_000, at: 10_000_000 });
  assert.equal(Math.round(early.offsetSeconds), Math.round(late.offsetSeconds));
});

test('the offset never runs past the end of the programme', () => {
  const m = describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR,
    { receivedAt: 0, at: 60 * 60 * 1000 });
  assert.equal(m.offsetSeconds, m.durationSeconds);
  assert.equal(m.remainingSeconds, 0);
  assert.equal(m.progress, 1);
});

test('a warming channel says it is coming on air and cannot be pressed', () => {
  const m = describeChannel(chan('nostalgia-cartoons'), NOW_WARMING,
    { receivedAt: 0, at: 0 });
  assert.equal(m.live, false);
  assert.equal(m.watchable, false);
  assert.equal(m.kicker, 'Coming on air');
  // The server's own sentence, carried through rather than re-decided here.
  assert.equal(m.detail, NOW_WARMING.detail);
  assert.equal(m.title, 'Nostalgia Cartoons');
});

test('a channel in a schedule gap says when it is back rather than nothing', () => {
  const m = describeChannel(chan('nostalgia-cartoons'), {
    channel: { id: 'nostalgia-cartoons', name: 'Nostalgia Cartoons' },
    state: 'off-air',
    detail: 'off air; Jack Frost starts in 240s',
    next: { title: 'Jack Frost', startTime: 1786237153 },
    serverNow: 1786236913,
  }, { receivedAt: 0, at: 0 });
  assert.equal(m.watchable, false);
  assert.equal(m.kicker, 'Off air');
  assert.equal(m.next.title, 'Jack Frost');
  assert.ok(nextLine(m).startsWith('Up next: Jack Frost'));
});

// The very first paint, before /now has answered. An empty box here is what
// requirement three exists to prevent.
test('a channel with no answer yet still says something true', () => {
  const listed = describeChannel(chan('nostalgia-classic-tv'), null, { receivedAt: 0, at: 0 });
  assert.equal(listed.kicker, 'Tuning in…');
  assert.equal(listed.watchable, false);
  assert.equal(listed.title, 'Nostalgia Classic TV');

  const settling = describeChannel(
    { id: 'x', name: 'X', status: 'empty', detail: 'still loading its programmes' },
    null, { receivedAt: 0, at: 0 },
  );
  assert.equal(settling.kicker, 'Coming on air');
  assert.equal(settling.detail, 'still loading its programmes');
});

test('nothing is watchable unless the server said on-air with a programme', () => {
  for (const state of ['off-air', 'empty', 'disabled', 'unknown']) {
    const m = describeChannel(chan('nostalgia-cartoons'),
      { state, serverNow: 1, channel: {} }, { receivedAt: 0, at: 0 });
    assert.equal(m.watchable, false, state);
  }
  // on-air with no programme attached is a contradiction; the safe reading is
  // the one that does not draw a button.
  assert.equal(describeChannel(chan('nostalgia-cartoons'),
    { state: 'on-air', serverNow: 1 }, { receivedAt: 0, at: 0 }).watchable, false);
});

// ------------------------------------------------------------------ polling --

test('a live channel is polled on the heartbeat, not every second', () => {
  const m = describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR, { receivedAt: 0, at: 0 });
  assert.equal(nextPollDelay(m), HEARTBEAT_MS);
});

test('a programme about to end is polled once at the boundary', () => {
  const delay = nextPollDelay({ live: true, remainingSeconds: 12 });
  assert.equal(delay, 13_500);
  // ...and never in a tight loop, however stuck the far end's clock is.
  assert.equal(nextPollDelay({ live: true, remainingSeconds: 0 }), MIN_POLL_MS);
  assert.ok(nextPollDelay({ live: true, remainingSeconds: 0 }) >= MIN_POLL_MS);
});

test('a channel that is still warming is asked about sooner', () => {
  assert.equal(nextPollDelay({ live: false, state: LINEAR_STATE.EMPTY }), WARMING_POLL_MS);
  assert.equal(nextPollDelay({ live: false, state: LINEAR_STATE.OFF_AIR }), HEARTBEAT_MS);
});

// -------------------------------------------------------------------- words --

test('the position line states both how far in and how much is left', () => {
  const m = describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR, { receivedAt: 0, at: 0 });
  assert.equal(positionLine(m), '13 min in · 11 min left');
});

test('the edges of the position line read as English', () => {
  assert.equal(positionLine({ live: true, offsetSeconds: 4, remainingSeconds: 900 }),
    'just started · 15 min left');
  assert.equal(positionLine({ live: true, offsetSeconds: 900, remainingSeconds: 12 }),
    '15 min in · ending now');
  assert.equal(positionLine({ live: false }), '');
});

test('minutes floor rather than round, and never go negative', () => {
  assert.equal(minutes(119), 1);
  assert.equal(minutes(-5), 0);
  assert.equal(minutes(undefined), 0);
});

test('a missing or nonsense time produces no clock rather than "Invalid Date"', () => {
  assert.equal(clockTime(0), '');
  assert.equal(clockTime(undefined), '');
  assert.ok(/\d/.test(clockTime(1786236610)));
});

test('there is no "up next" line when the server named nothing', () => {
  assert.equal(nextLine({ next: null }), '');
  assert.equal(nextLine({ next: { title: '', startTime: 1 } }), '');
});

// --------------------------------------------------------------- tuning in --

test('a tuned-in channel keeps the server media fragment exactly as given', () => {
  const p = playableFor(TUNE_OK);
  assert.equal(p.render, 'video');
  // THE line this whole module exists for. Rebuilding this URL -- which the
  // ordinary archive.org route does, from the identifier -- drops the fragment
  // and the channel silently starts every programme from zero.
  assert.equal(p.src, 'https://archive.org/download/Beverly_Hillbillies_Ep01/BH01.mp4#t=1098');
  assert.ok(p.src.includes('#t=1098'));
});

test('a channel that cannot be played repeats the reason instead of a dead player', () => {
  assert.throws(() => playableFor(TUNE_UNAVAILABLE), (err) => {
    assert.ok(err instanceof PlaybackError);
    assert.ok(err.message.includes('cannot be played right now'));
    return true;
  });
  assert.throws(() => playableFor(null), PlaybackError);
  assert.throws(() => playableFor({ available: true, source: {} }), PlaybackError);
});

test('a 503 is read for its body rather than treated as a broken request', async () => {
  const p = tuneIn('nostalgia-cartoons', {
    cache: new Map(),
    fetchImpl: async () => ({ ok: false, status: 503, json: async () => TUNE_UNAVAILABLE }),
  });
  const tune = await p;
  assert.equal(tune.available, false);
  assert.equal(tune.detail, TUNE_UNAVAILABLE.detail);
});

test('a cold channel is retried until its Archive catalogue is ready', async () => {
  let calls = 0;
  const waited = [];
  const tune = await tuneIn('nostalgia-cartoons', {
    cache: new Map(),
    warmRetries: 3,
    wait: async (ms) => { waited.push(ms); },
    fetchImpl: async () => {
      calls++;
      const body = calls < 3 ? NOW_WARMING : TUNE_OK;
      return { ok: calls >= 3, status: calls >= 3 ? 200 : 503, json: async () => body };
    },
  });
  assert.equal(tune.available, true);
  assert.equal(calls, 3);
  assert.deepEqual(waited, [2000, 2000]);
});

test('a hover and the click after it are one request, not two', async () => {
  const cache = new Map();
  let calls = 0;
  const fetchImpl = async () => { calls++; return ok(TUNE_OK); };
  let t = 0;
  const clock = () => t;

  await tuneIn('c', { cache, fetchImpl, clock });        // hover
  await tuneIn('c', { cache, fetchImpl, clock });        // click, a moment later
  assert.equal(calls, 1);

  // ...but an answer left sitting is asked again, because by then the channel
  // may be on a different programme entirely and the cached URL would be the
  // wrong FILE rather than merely a stale number.
  t = 60_000;
  await tuneIn('c', { cache, fetchImpl, clock });
  assert.equal(calls, 2);
});

test('a refused tune-in is not remembered', async () => {
  const cache = new Map();
  let calls = 0;
  const fetchImpl = async () => {
    calls++;
    if (calls === 1) throw new Error('gateway timeout');
    return ok(TUNE_OK);
  };
  await assert.rejects(() => tuneIn('c', { cache, fetchImpl, clock: () => 0 }), PlaybackError);
  const tune = await tuneIn('c', { cache, fetchImpl, clock: () => 0 });
  assert.equal(tune.available, true);
  assert.equal(calls, 2);
});

// ---------------------------------------------------------------- resolver --

test('the resolver claims channels and nothing else', () => {
  assert.equal(linearResolver.canHandle(linearURI('nostalgia-classic-tv')), true);
  assert.equal(linearURI('x'), `${LINEAR_URI_PREFIX}x`);
  for (const other of [
    'magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567',
    'https://archive.org/download/Some_Item/file.mp4',
    'https://example.com/stream.m3u8',
    '',
    null,
  ]) {
    assert.equal(linearResolver.canHandle(other), false, String(other));
  }
});

test('resolving a channel produces a positioned Playable', async () => {
  const asked = [];
  const out = await linearResolver.resolve(
    { kind: 'auto', uri: linearURI('nostalgia-classic-tv') },
    {
      fetchImpl: async (url) => { asked.push(url); return ok(TUNE_OK); },
      clock: () => Date.now() + Math.random() * 1e6, // defeat the module cache
    },
  );
  assert.equal(asked[0], '/api/v1/linear/stream?channel=nostalgia-classic-tv');
  assert.equal(out.render, 'video');
  assert.ok(out.src.endsWith('#t=1098'));
});

// ---------------------------------------------------------------- rendering --
//
// No jsdom (project constraint). These fakes cover only the surface the strip
// touches. What is being pinned is what a visitor can and cannot press.

function fakeEl(tag) {
  const node = {
    tagName: tag,
    className: '',
    textContent: '',
    type: '',
    title: '',
    alt: '',
    src: '',
    loading: '',
    hidden: false,
    disabled: false,
    style: {},
    attrs: {},
    events: {},
    children: [],
    parent: null,
    append(...kids) { for (const k of kids) { k.parent = node; node.children.push(k); } },
    replaceChildren(...kids) { node.children = []; node.append(...kids); },
    remove() {
      if (!node.parent) return;
      node.parent.children = node.parent.children.filter((c) => c !== node);
      node.parent = null;
    },
    setAttribute(k, v) { node.attrs[k] = v; },
    addEventListener(name, fn) { (node.events[name] ||= []).push(fn); },
    fire(name) { for (const fn of node.events[name] || []) fn(); },
    focus() {},
  };
  return node;
}

async function withFakeDom(fn) {
  const original = globalThis.document;
  globalThis.document = { createElement: fakeEl, addEventListener() {}, removeEventListener() {} };
  try {
    return await fn();
  } finally {
    globalThis.document = original;
  }
}

/** Every text node under an element, flattened, so a card can be read. */
function textOf(node) {
  const out = [];
  const walk = (n) => {
    if (n.hidden) return;
    if (n.textContent) out.push(n.textContent);
    for (const c of n.children) walk(c);
  };
  walk(node);
  return out.join(' | ');
}

test('a live card is pressable and says what is on', async () => {
  await withFakeDom(() => {
    const pressed = [];
    const card = createCard({ onWatch: (m) => pressed.push(m.title) });
    card.update(describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR, { receivedAt: 0, at: 0 }));

    assert.equal(card.node.disabled, false);
    const text = textOf(card.node);
    assert.ok(text.includes('On now'));
    assert.ok(text.includes('Beverly Hillbillies Ep01 The Clampetts Strike Oil'));
    assert.ok(text.includes('11 min left'));
    assert.ok(text.includes('The Big Frank'));

    card.node.fire('click');
    assert.deepEqual(pressed, ['Beverly Hillbillies Ep01 The Clampetts Strike Oil']);
  });
});

// The rule the rest of this site now follows: never show a control that cannot
// work. A warming channel is drawn, explained, and refuses the press.
test('a warming card explains itself and refuses to be pressed', async () => {
  await withFakeDom(() => {
    const pressed = [];
    const card = createCard({ onWatch: (m) => pressed.push(m) });
    card.update(describeChannel(chan('nostalgia-cartoons'), NOW_WARMING, { receivedAt: 0, at: 0 }));

    assert.equal(card.node.disabled, true);
    assert.equal(card.node.attrs['aria-disabled'], 'true');
    const text = textOf(card.node);
    assert.ok(text.includes('Coming on air'));
    assert.ok(text.includes('still loading its programmes'));

    card.node.fire('click');
    assert.deepEqual(pressed, [], 'a disabled channel must not start a player');
  });
});

test('hovering a live channel warms it; hovering a dead one asks for nothing', async () => {
  await withFakeDom(() => {
    const intents = [];
    const card = createCard({ onIntent: (m) => intents.push(m.id) });

    card.update(describeChannel(chan('nostalgia-cartoons'), NOW_WARMING, { receivedAt: 0, at: 0 }));
    card.node.fire('pointerenter');
    assert.deepEqual(intents, []);

    card.update(describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR, { receivedAt: 0, at: 0 }));
    card.node.fire('pointerenter');
    assert.deepEqual(intents, ['nostalgia-classic-tv']);
  });
});

test('a channel that goes off air drops the last programme still', async () => {
  await withFakeDom(() => {
    const card = createCard({});
    card.update(describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR, { receivedAt: 0, at: 0 }));
    const art = card.node.children[0];
    assert.ok(art.children.some((c) => c.tagName === 'img' && c.src), 'the programme still is up');

    card.update(describeChannel(chan('nostalgia-classic-tv'), NOW_WARMING, { receivedAt: 0, at: 0 }));
    assert.ok(!art.children.some((c) => c.tagName === 'img'),
      'a card that says nothing is on must not keep a picture of the last thing that was');
  });
});

test('the same card is updated rather than rebuilt, so focus survives a poll', async () => {
  await withFakeDom(() => {
    const card = createCard({});
    const first = card.node.children;
    card.update(describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR, { receivedAt: 0, at: 0 }));
    card.update(describeChannel(chan('nostalgia-classic-tv'), NOW_ON_AIR, { receivedAt: 0, at: 60_000 }));
    assert.equal(card.node.children, first);
  });
});

// --------------------------------------------------------------- the strip --

function stubTimers() {
  const timeouts = new Map();
  const intervals = new Map();
  let id = 0;
  return {
    timeouts,
    intervals,
    setTimeout(fn, ms) { const t = ++id; timeouts.set(t, { fn, ms }); return t; },
    clearTimeout(t) { timeouts.delete(t); },
    setInterval(fn, ms) { const t = ++id; intervals.set(t, { fn, ms }); return t; },
    clearInterval(t) { intervals.delete(t); },
  };
}

function stubServer({ channels = CHANNELS_BODY, now = {} } = {}) {
  const calls = [];
  return {
    calls,
    async fetchImpl(url) {
      calls.push(url);
      if (url.includes('/channels')) return ok(channels);
      if (url.includes('/now')) {
        const id = decodeURIComponent(url.split('channel=')[1]);
        const body = now[id];
        return body ? ok(body) : { ok: false, status: 404, json: async () => ({}) };
      }
      return { ok: false, status: 404, json: async () => ({}) };
    },
  };
}

test('the strip draws both channels and stays hidden until it has them', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('section');
    host.hidden = true;
    const server = stubServer({
      now: { 'nostalgia-classic-tv': NOW_ON_AIR, 'nostalgia-cartoons': NOW_WARMING },
    });
    const strip = mountLinear(host, {
      fetchImpl: server.fetchImpl, timers: stubTimers(), clock: () => 0,
    });
    // Hidden on the synchronous path, before a byte has come back.
    assert.equal(host.hidden, true);

    await strip.ready;
    assert.equal(host.hidden, false);
    const rail = host.children.find((c) => c.className === 'tv-rail');
    assert.equal(rail.children.length, 2);
    strip.stop();
  });
});

test('a channel service that answers nothing leaves the strip away entirely', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('section');
    host.hidden = true;
    const strip = mountLinear(host, {
      fetchImpl: () => Promise.reject(new Error('down')),
      timers: stubTimers(),
    });
    await strip.ready;
    assert.equal(host.hidden, true, 'no channels means no strip, not an empty box');
    assert.equal(host.children.length, 0);
    strip.stop();
  });
});

test('a poll that fails leaves the last known programme on screen', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('section');
    const timers = stubTimers();
    let answer = true;
    const strip = mountLinear(host, {
      timers,
      clock: () => 0,
      fetchImpl: async (url) => {
        if (url.includes('/channels')) return ok(CHANNELS_BODY);
        if (!answer) throw new Error('a poll timed out');
        return ok(url.includes('classic-tv') ? NOW_ON_AIR : NOW_WARMING);
      },
    });
    await strip.ready;
    await Promise.resolve();
    const before = strip.models().find((m) => m.id === 'nostalgia-classic-tv');
    assert.equal(before.live, true);

    answer = false;
    // Run whichever poll the strip scheduled next.
    const [firstTimer] = [...timers.timeouts.values()];
    if (firstTimer) await firstTimer.fn();
    const after = strip.models().find((m) => m.id === 'nostalgia-classic-tv');
    assert.equal(after.live, true, 'a lost poll is a network fact, not a channel fact');
    strip.stop();
  });
});

test('hiding the strip for a search stops every timer it owns', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('section');
    const timers = stubTimers();
    const server = stubServer({
      now: { 'nostalgia-classic-tv': NOW_ON_AIR, 'nostalgia-cartoons': NOW_ON_AIR },
    });
    const strip = mountLinear(host, { fetchImpl: server.fetchImpl, timers, clock: () => 0 });
    await strip.ready;
    await Promise.resolve();
    assert.ok(timers.intervals.size > 0, 'a live strip ticks');

    strip.hide();
    assert.equal(host.hidden, true);
    assert.equal(timers.intervals.size, 0);
    assert.equal(timers.timeouts.size, 0);

    strip.show();
    assert.equal(host.hidden, false);
    assert.ok(timers.intervals.size > 0);
    strip.stop();
  });
});

test('pressing a channel hands the caller the channel and what is on it', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('section');
    const watched = [];
    const server = stubServer({ now: { 'nostalgia-classic-tv': NOW_ON_AIR } });
    const strip = mountLinear(host, {
      fetchImpl: server.fetchImpl,
      timers: stubTimers(),
      clock: () => 0,
      onWatch: (channel, model) => watched.push([channel.id, model.title]),
    });
    await strip.ready;
    await Promise.resolve();

    const rail = host.children.find((c) => c.className === 'tv-rail');
    for (const node of rail.children) node.fire('click');
    assert.deepEqual(watched, [['nostalgia-classic-tv', NOW_ON_AIR.program.title]]);
    strip.stop();
  });
});
