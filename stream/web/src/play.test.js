import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  ROUTE, REASON, emptyVerdict, normalise, canPlay, playLabel, toPlayable, fetchVerdict, play,
} from './play.js';
import { RENDER } from './source.js';

/**
 * Every test below is about the same rule from a different side: a Play button
 * that does not play is worse than no Play button. So the assertions come in
 * pairs -- what is offered, and what is REFUSED -- because a file that only
 * tested the happy path would pass just as well if it always said yes.
 */

// A verdict from the server, with the shape play_archive.go returns.
const nesVerdict = {
  id: 'pacman_nes_2',
  title: 'Pac-Man (NES)',
  domain: 'game',
  type: 'release',
  emulator: 'nes',
  platform: 'nes',
  system: 'NES',
  playable: true,
  route: 'emulatorjs',
  core: 'nes',
  coreFile: 'fceumm',
  rom: {
    name: 'pacman.nes',
    url: '/bridge/iptv?u=https%3A%2F%2Farchive.org%2Fdownload%2Fpacman_nes_2%2Fpacman.nes',
    direct: 'https://archive.org/download/pacman_nes_2/pacman.nes',
    sizeBytes: 24592,
  },
  embed: 'https://archive.org/embed/pacman_nes_2',
  touch: true,
};

const streamOnlyVerdict = {
  id: 'davidrobinsonssupremecourtprototype',
  title: "David Robinson's Supreme Court (prototype)",
  emulator: 'gamegear',
  platform: 'gamegear',
  system: 'Game Gear',
  playable: true,
  route: 'archive',
  embed: 'https://archive.org/embed/davidrobinsonssupremecourtprototype',
  touch: false,
  reasons: [{
    code: 'stream_only',
    detail: 'the Internet Archive permits playing this in a browser but not downloading it, '
      + 'so it plays in their player rather than ours.',
  }],
};

const biosVerdict = {
  id: 'coleco_game',
  emulator: 'coleco',
  platform: 'colecovision',
  system: 'ColecoVision',
  playable: true,
  route: 'archive',
  embed: 'https://archive.org/embed/coleco_game',
  touch: false,
  reasons: [{
    code: 'needs_bios',
    detail: 'ColecoVision games need the console’s BIOS, which is not ours to ship.',
  }],
};

const notEmulatedVerdict = {
  id: 'a_book',
  playable: false,
  route: 'none',
  touch: false,
  reasons: [{ code: 'not_emulated', detail: 'this item is not an emulated one.' }],
};

const okResponse = (body) => ({ ok: true, status: 200, json: async () => body });

// --- what may be pressed ----------------------------------------------------

test('a game with a core and a ROM is offered our own player', () => {
  const verdict = normalise(nesVerdict);
  assert.equal(canPlay(verdict), true);
  assert.equal(verdict.route, ROUTE.EMULATORJS);
  assert.equal(verdict.core, 'nes');
  assert.equal(verdict.touch, true);

  const { label, caveat, blocked } = playLabel(verdict);
  assert.equal(blocked, false);
  assert.equal(label, 'Play');
  assert.equal(caveat, '', 'our player has a virtual gamepad; there is nothing to warn about');
});

// The Archive's own player really does run the game. Calling that unplayable
// would be its own dishonesty -- 96% of Game Gear items reach the site this way.
test('a stream-only item still gets a button, pointed at their player', () => {
  const verdict = normalise(streamOnlyVerdict);
  assert.equal(canPlay(verdict), true);
  assert.equal(verdict.route, ROUTE.ARCHIVE);

  const { label, hint, blocked } = playLabel(verdict);
  assert.equal(blocked, false);
  assert.match(label, /Internet Archive/);
  assert.match(hint, /permits playing this in a browser but not downloading/);
});

// The device fact this module exists to add. On a phone their player is a game
// you can watch and not play, and that is said out loud rather than discovered.
test('on a touch device the archive route carries its missing-controls caveat', () => {
  const verdict = normalise(streamOnlyVerdict);

  assert.equal(playLabel(verdict, { touchOnly: false }).caveat, '');
  assert.match(playLabel(verdict, { touchOnly: true }).caveat, /keyboard/);
});

// --- what may not ------------------------------------------------------------

test('an item nothing can run gets no button at all', () => {
  const verdict = normalise(notEmulatedVerdict);
  assert.equal(canPlay(verdict), false);

  const { label, hint, blocked } = playLabel(verdict);
  assert.equal(blocked, true);
  assert.equal(label, '', 'a label with nothing behind it is the dead button');
  assert.match(hint, /not an emulated one/);
});

// The reason a person is shown must be the server's own sentence, not a generic
// one written here -- otherwise the explanation drifts from the decision.
test('the refusal shown is the server’s own words', () => {
  assert.match(playLabel(normalise(biosVerdict)).hint, /BIOS/);
  assert.match(playLabel(normalise(notEmulatedVerdict)).hint, /not an emulated one/);
});

test('pressing anyway refuses with the same sentence it was labelled with', () => {
  const verdict = normalise(notEmulatedVerdict);
  assert.throws(() => toPlayable(verdict), (error) => {
    assert.equal(error.name, 'PlaybackError');
    assert.match(error.message, /not an emulated one/);
    return true;
  });
});

// --- distrust of everything between the server and here ----------------------

// The direction of the defence is the whole point: anything unrecognised must
// cost a button, never produce one.
test('an unknown route is treated as unplayable, not as playable', () => {
  const verdict = normalise({ ...nesVerdict, route: 'something-new' });
  assert.equal(verdict.route, ROUTE.NONE);
  assert.equal(canPlay(verdict), false);
});

test('a missing body is unplayable rather than optimistically empty', () => {
  for (const bad of [null, undefined, 'a string', 42, []]) {
    const verdict = normalise(bad, 'x');
    assert.equal(canPlay(verdict), false, `normalise(${JSON.stringify(bad)}) offered a button`);
    assert.equal(verdict.reasons[0].code, REASON.UPSTREAM);
  }
});

// The invariant the server promises, re-checked. If it ever arrives broken, the
// answer is the archive route -- which still plays -- rather than a canvas with
// nothing to put in it.
test('an emulatorjs route with no ROM falls back instead of booting an empty core', () => {
  const verdict = normalise({ ...nesVerdict, rom: null });
  assert.equal(verdict.route, ROUTE.ARCHIVE);
  assert.equal(canPlay(verdict), true);
  assert.ok(verdict.reasons.some((r) => r.code === REASON.NO_PAYLOAD));
});

test('an emulatorjs route with no core falls back the same way', () => {
  const verdict = normalise({ ...nesVerdict, core: '' });
  assert.equal(verdict.route, ROUTE.ARCHIVE);
});

// A route with nowhere to go is not a route.
test('an archive route with no embed is unplayable', () => {
  const verdict = normalise({ ...streamOnlyVerdict, embed: '' });
  assert.equal(verdict.route, ROUTE.NONE);
  assert.equal(canPlay(verdict), false);
});

test('playable:true with route none is not honoured', () => {
  const verdict = normalise({ ...notEmulatedVerdict, playable: true });
  assert.equal(canPlay(verdict), false);
});

// A ROM with no URL is not a ROM.
test('a ROM entry with no url does not count as one', () => {
  const verdict = normalise({ ...nesVerdict, rom: { name: 'x.nes', sizeBytes: 10 } });
  assert.equal(verdict.route, ROUTE.ARCHIVE);
});

// --- the playables -----------------------------------------------------------

test('our player is a canvas that boots on a real click, not on load', () => {
  const playable = toPlayable(normalise(nesVerdict), { doc: fakeDocument() });
  assert.equal(playable.render, RENDER.CANVAS);
  assert.equal(typeof playable.mount, 'function');

  // A WASM emulator opens an AudioContext, and autoplay policy holds the run
  // loop until the page has been interacted with -- so a boot on load leaves
  // the core loaded, `started` true, and the frame counter pinned at 0. What
  // must be in the container after mount is therefore a BUTTON, not a game.
  const el = fakeElement();
  playable.mount(el);
  assert.equal(el.children.length, 1);
  assert.equal(el.children[0].tagName, 'BUTTON');
  assert.match(el.children[0].textContent, /pacman\.nes/);
});

// The core reaches EmulatorJS as EJS_core, and that is the value the whole
// server-side table exists to get right. This is where it is finally spent.
test('the core the server chose is the one EmulatorJS is configured with', () => {
  const doc = fakeDocument();
  const playable = toPlayable(normalise(nesVerdict), { doc });
  const el = fakeElement();
  playable.mount(el);

  // The boot hangs off a real click, so drive it the way a person would.
  el.children[0].listeners.click[0]();

  assert.equal(globalThis.EJS_core, 'nes');
  assert.equal(globalThis.EJS_gameUrl, nesVerdict.rom.url);
  assert.equal(globalThis.EJS_gameName, 'pacman.nes');
});

test('the ROM is loaded through the relay, because archive.org sends no CORS header', () => {
  const playable = toPlayable(normalise(nesVerdict));
  assert.ok(playable.src.startsWith('/bridge/iptv?u='), playable.src);
});

// Rebuilding the relay URL here would be a second place for the path to be
// wrong, and the two would disagree exactly when one of them was fixed.
test('the relay URL is the server’s, not rebuilt from the direct one', () => {
  const playable = toPlayable(normalise(nesVerdict));
  assert.equal(playable.src, nesVerdict.rom.url);
});

test('their player is an iframe and costs us no relay bytes', () => {
  const playable = toPlayable(normalise(streamOnlyVerdict));
  assert.equal(playable.render, RENDER.EMBED);
  assert.equal(playable.src, streamOnlyVerdict.embed);
  assert.ok(!playable.src.includes('/bridge/'), 'no byte should cross our relay');
});

// --- fetching ----------------------------------------------------------------

test('the verdict is asked for by identifier', async () => {
  let asked = '';
  const verdict = await fetchVerdict('pacman_nes_2', {
    fetchImpl: async (url) => { asked = url; return okResponse(nesVerdict); },
  });
  assert.equal(asked, '/api/play/archive?id=pacman_nes_2');
  assert.equal(verdict.route, ROUTE.EMULATORJS);
});

// A card carries the URL, so a client should not have to take it apart. The
// server accepts either form; this only has to pass it through intact.
test('an archive.org URL is passed through encoded rather than parsed here', async () => {
  let asked = '';
  await fetchVerdict('https://archive.org/details/pacman_nes_2#ejs', {
    fetchImpl: async (url) => { asked = url; return okResponse(nesVerdict); },
  });
  assert.ok(asked.includes(encodeURIComponent('https://archive.org/details/pacman_nes_2#ejs')));
});

test('an empty id never reaches the network', async () => {
  let called = false;
  const verdict = await fetchVerdict('  ', {
    fetchImpl: async () => { called = true; return okResponse(nesVerdict); },
  });
  assert.equal(called, false);
  assert.equal(canPlay(verdict), false);
});

// "We could not check" must never render as "press Play". This is the failure
// mode that turns one flaky request into a button that does nothing.
test('a network failure is unknown, not playable', async () => {
  const verdict = await fetchVerdict('x', {
    fetchImpl: async () => { throw new Error('offline'); },
  });
  assert.equal(canPlay(verdict), false);
  assert.equal(verdict.reasons[0].code, REASON.UPSTREAM);
  assert.match(playLabel(verdict).hint, /offline/);
});

test('an HTTP error is unknown, not playable', async () => {
  const verdict = await fetchVerdict('x', {
    fetchImpl: async () => ({ ok: false, status: 502, json: async () => ({}) }),
  });
  assert.equal(canPlay(verdict), false);
  assert.match(playLabel(verdict).hint, /502/);
});

// Rate limiting and maintenance pages arrive as 200 + HTML.
test('a non-JSON body is unknown, not playable', async () => {
  const verdict = await fetchVerdict('x', {
    fetchImpl: async () => ({ ok: true, status: 200, json: async () => { throw new Error('bad json'); } }),
  });
  assert.equal(canPlay(verdict), false);
  assert.equal(verdict.reasons[0].code, REASON.UPSTREAM);
});

test('emptyVerdict is never accidentally playable', () => {
  const verdict = emptyVerdict('x');
  assert.equal(canPlay(verdict), false);
  assert.equal(verdict.route, ROUTE.NONE);
  assert.equal(verdict.touch, false);
});

// --- end to end --------------------------------------------------------------

test('play() resolves an item straight to a playable', async () => {
  const playable = await play('pacman_nes_2', {
    fetchImpl: async () => okResponse(nesVerdict),
  });
  assert.equal(playable.render, RENDER.CANVAS);
});

test('play() refuses an item the server said no about', async () => {
  await assert.rejects(
    () => play('a_book', { fetchImpl: async () => okResponse(notEmulatedVerdict) }),
    /not an emulated one/,
  );
});

// Every fixture in this file, checked against the one rule the module exists
// for: a canvas is only ever produced with a core and a ROM behind it.
test('no verdict ever produces a canvas without a core and a ROM', () => {
  for (const raw of [nesVerdict, streamOnlyVerdict, biosVerdict, notEmulatedVerdict]) {
    const verdict = normalise(raw);
    if (!canPlay(verdict)) continue;
    const playable = toPlayable(verdict, { doc: fakeDocument() });
    if (playable.render !== RENDER.CANVAS) continue;
    assert.ok(verdict.core, `${verdict.id} produced a canvas with no core`);
    assert.ok(verdict.rom?.url, `${verdict.id} produced a canvas with no ROM`);
  }
});

// --- a DOM small enough to reason about --------------------------------------

function fakeElement(tagName = 'DIV') {
  const el = {
    tagName,
    id: '',
    className: '',
    type: '',
    src: '',
    textContent: '',
    style: {},
    children: [],
    listeners: {},
    append(...kids) { this.children.push(...kids); },
    replaceChildren(...kids) { this.children = kids; },
    addEventListener(name, fn) { (this.listeners[name] ||= []).push(fn); },
    remove() {},
  };
  return el;
}

/**
 * Enough of a document for the EmulatorJS boot to run without a DOM.
 *
 * `body` matters: the loader is injected by appending a <script> to it, which
 * is the step that actually starts the emulator.
 */
function fakeDocument() {
  const body = fakeElement('BODY');
  return {
    body,
    createElement: (tag) => fakeElement(tag.toUpperCase()),
  };
}
