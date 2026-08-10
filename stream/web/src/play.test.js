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
test('the core the server chose is the one EmulatorJS is configured with', async () => {
  const doc = fakeDocument();
  const playable = toPlayable(normalise(nesVerdict), {
    doc, fetchImpl: romFetch(nesVerdict.rom.sizeBytes),
  });
  const el = fakeElement();
  playable.mount(el);

  // The boot hangs off a real click, so drive it the way a person would -- and
  // wait, because the ROM is fetched before there is an emulator to hand it to.
  await el.children[0].listeners.click[0]();

  assert.equal(globalThis.EJS_core, 'nes');
  assert.equal(globalThis.EJS_gameName, 'pacman.nes');
  // The URL EmulatorJS is given is a blob: one, and that is the fix rather than
  // an implementation detail. EmulatorJS names the file it writes into the
  // core's filesystem after the URL it downloaded; through the relay that name
  // was `iptv`, with no extension, for every ROM on the site. A blob: URL makes
  // it use EJS_gameName instead, which is the name the server chose.
  assert.ok(
    String(globalThis.EJS_gameUrl).startsWith('blob:'),
    `EJS_gameUrl is ${globalThis.EJS_gameUrl}; a URL-derived name is what broke the Game Gear`,
  );
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

/**
 * The ROM the player now fetches for itself before booting.
 *
 * The byte COUNT matters: a length that disagrees with the verdict is treated
 * as a failed fetch and the next source is tried. That is what catches an
 * archive.org node answering 5xx with a short HTML body, which a WASM emulator
 * would otherwise boot on and run nothing.
 */
function romFetch(bytes) {
  return async () => ({
    ok: true,
    status: 200,
    arrayBuffer: async () => new ArrayBuffer(bytes),
  });
}

// --- the player switch -------------------------------------------------------

import {
  playerOptions, canSwitchPlayer, choosePlayer, solePlayerSentence,
  readPlayerPreference, writePlayerPreference, verdictQuery, PLAYER_PREF_KEY,
} from './play.js';

/**
 * The headline change: EmulatorJS is the default everywhere it can run, and the
 * viewer can move to the Internet Archive's player and be remembered.
 *
 * Two failure modes are being defended against, and they pull in opposite
 * directions. A switch that hides its other half tells a viewer there is no
 * choice; a switch that offers a dead option is the dead button this whole file
 * exists to remove. Both drawn, one disabled, and the disabled one carrying the
 * server's own sentence is the only version that is neither.
 */

const fakeStorage = (initial = {}) => {
  const map = new Map(Object.entries(initial));
  return {
    getItem: (k) => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => map.set(k, String(v)),
    _map: map,
  };
};

test('our own player is the default wherever it can run', () => {
  const verdict = normalise(nesVerdict);
  assert.equal(choosePlayer(verdict, ''), ROUTE.EMULATORJS);
  assert.equal(canSwitchPlayer(verdict), true, 'an item with an embed can always go the other way');
});

test('both players are always offered, and the one that cannot run says why', () => {
  const [ours, theirs] = playerOptions(normalise(streamOnlyVerdict));

  assert.equal(ours.route, ROUTE.EMULATORJS);
  assert.equal(ours.available, false);
  assert.match(ours.why, /permits playing this in a browser but not downloading/,
    "the refusal must be the server's own sentence, not one invented here");

  assert.equal(theirs.route, ROUTE.ARCHIVE);
  assert.equal(theirs.available, true);
  assert.match(theirs.note, /Internet Archive's own player/);
});

test('a remembered choice wins, but only where that player can run the item', () => {
  const both = normalise(nesVerdict);
  assert.equal(choosePlayer(both, ROUTE.ARCHIVE), ROUTE.ARCHIVE);
  assert.equal(choosePlayer(both, ROUTE.EMULATORJS), ROUTE.EMULATORJS);

  // A preference for a player that cannot run THIS item is not a reason to show
  // somebody nothing.
  const theirsOnly = normalise(streamOnlyVerdict);
  assert.equal(choosePlayer(theirsOnly, ROUTE.EMULATORJS), ROUTE.ARCHIVE);

  // And nothing at all is still nothing.
  assert.equal(choosePlayer(normalise(notEmulatedVerdict), ROUTE.ARCHIVE), ROUTE.NONE);
});

test('the choice is remembered as a taste, not as a route for one item', () => {
  const storage = fakeStorage();
  assert.equal(readPlayerPreference(storage), '');

  writePlayerPreference(ROUTE.ARCHIVE, storage);
  assert.equal(storage._map.get(PLAYER_PREF_KEY), ROUTE.ARCHIVE);
  assert.equal(readPlayerPreference(storage), ROUTE.ARCHIVE);

  // Anything that is not one of the two players is ignored in both directions,
  // so a corrupted value can never select a player that does not exist.
  writePlayerPreference('something-else', storage);
  assert.equal(readPlayerPreference(storage), ROUTE.ARCHIVE);
  assert.equal(readPlayerPreference(fakeStorage({ [PLAYER_PREF_KEY]: 'nonsense' })), '');
});

test('storage that throws costs the memory, never the player', () => {
  const angry = {
    getItem() { throw new Error('blocked'); },
    setItem() { throw new Error('blocked'); },
  };
  assert.equal(readPlayerPreference(angry), '');
  assert.doesNotThrow(() => writePlayerPreference(ROUTE.ARCHIVE, angry));
});

test('when only one player is possible, one plain sentence says which and why', () => {
  const sentence = solePlayerSentence(normalise(streamOnlyVerdict));
  assert.match(sentence, /^Internet Archive player only —/);
  assert.match(sentence, /permits playing this in a browser but not downloading/);

  // With a real choice there is nothing to explain; the switch speaks for itself.
  assert.equal(solePlayerSentence(normalise(nesVerdict)), '');
});

test('the switch can only ever select a player that actually works', () => {
  const theirsOnly = normalise(streamOnlyVerdict);
  assert.throws(
    () => toPlayable(theirsOnly, { route: ROUTE.EMULATORJS }),
    /permits playing this in a browser but not downloading/,
    'the switch was able to hand-build the dead button',
  );

  // And the legitimate direction really does produce the other player.
  const both = normalise(nesVerdict);
  assert.equal(toPlayable(both, { route: ROUTE.ARCHIVE, doc: fakeDocument() }).render, RENDER.EMBED);
  assert.equal(toPlayable(both, { route: ROUTE.EMULATORJS, doc: fakeDocument() }).render, RENDER.CANVAS);
});

// --- what this browser declares ---------------------------------------------

test('capabilities are declared on the query string, and never the files themselves', () => {
  assert.equal(verdictQuery('x', [], false), 'id=x');
  assert.equal(verdictQuery('x', ['coleco'], false), 'id=x&bios=coleco');
  // Sorted and de-duplicated, so the same browser always produces the same URL.
  assert.equal(verdictQuery('x', ['psx', 'coleco', 'psx'], false), 'id=x&bios=coleco%2Cpsx');
  assert.equal(verdictQuery('x', ['', null, ' amiga '], false), 'id=x&bios=amiga');
  assert.equal(verdictQuery('x', [], true), 'id=x&isolated=1');
  assert.match(verdictQuery('a b/c', [], false), /^id=a\+b%2Fc$/);
});

test('the declaration reaches the server', async () => {
  const asked = [];
  await fetchVerdict('coleco_game', {
    bios: ['coleco'],
    isolated: true,
    fetchImpl: async (url) => { asked.push(url); return okResponse(nesVerdict); },
  });
  assert.equal(asked.length, 1);
  assert.match(asked[0], /[?&]bios=coleco(&|$)/);
  assert.match(asked[0], /[?&]isolated=1(&|$)/);
});

// --- the guide comes through -------------------------------------------------

test('the guide and the BIOS offer survive normalisation', () => {
  const verdict = normalise({
    ...biosVerdict,
    guide: { description: 'A game.', controls: [{ button: 'A', key: 'Z' }] },
    biosNeeded: { system: 'coleco', label: 'ColecoVision BIOS', files: ['colecovision.rom'] },
  });
  assert.equal(verdict.guide.description, 'A game.');
  assert.equal(verdict.biosNeeded.system, 'coleco');

  // And anything malformed becomes absent rather than half-present: an offer
  // with no machine name is a file input pointed at nothing.
  for (const bad of [null, 'text', [], {}, { label: 'x' }]) {
    assert.equal(normalise({ ...biosVerdict, biosNeeded: bad }).biosNeeded, null);
  }
  for (const bad of [null, 'text', []]) {
    assert.equal(normalise({ ...biosVerdict, guide: bad }).guide, null);
  }
});

// A WASM emulator needs a real user gesture before the browser will let it run,
// so mountEmulator hangs the boot off a click. Press it the way a person would.
async function boot(playable) {
  const host = fakeElement('DIV');
  playable.mount(host);
  const [button] = host.children;
  // Awaited: the click handler fetches the ROM before there is an emulator.
  for (const fn of button.listeners.click ?? []) await fn();
  return host;
}

test('a BIOS URL is handed to the emulator and nothing else is', async () => {
  const doc = fakeDocument();
  const rom = romFetch(nesVerdict.rom.sizeBytes);
  await boot(toPlayable(normalise(nesVerdict), {
    doc, route: ROUTE.EMULATORJS, biosUrl: 'blob:stored-locally', fetchImpl: rom,
  }));
  assert.equal(globalThis.EJS_biosUrl, 'blob:stored-locally');

  // Globals persist between games, so a BIOS left set from the last one would
  // be handed to the next: a Kickstart ROM fed to an NES core is a black screen
  // with no error anywhere.
  await boot(toPlayable(normalise(nesVerdict), {
    doc, route: ROUTE.EMULATORJS, fetchImpl: rom,
  }));
  assert.equal(globalThis.EJS_biosUrl, '');
});

// SharedArrayBuffer only exists on a cross-origin-isolated page, and asking
// EmulatorJS for a threaded core without it loads a core that throws on
// construction. Reading the platform's own answer means this is right whether or
// not the isolation headers are ever deployed.
test('threading is claimed only when the platform says the page is isolated', async () => {
  const doc = fakeDocument();
  const rom = romFetch(nesVerdict.rom.sizeBytes);
  const before = globalThis.crossOriginIsolated;
  try {
    globalThis.crossOriginIsolated = false;
    await boot(toPlayable(normalise(nesVerdict), {
      doc, route: ROUTE.EMULATORJS, fetchImpl: rom,
    }));
    assert.equal(globalThis.EJS_threads, false);

    globalThis.crossOriginIsolated = true;
    await boot(toPlayable(normalise(nesVerdict), {
      doc, route: ROUTE.EMULATORJS, fetchImpl: rom,
    }));
    assert.equal(globalThis.EJS_threads, true);
  } finally {
    globalThis.crossOriginIsolated = before;
  }
});

// --- where the ROM comes from, and what it is called on arrival --------------

import { fetchRom } from './resolvers/game.js';

// A Game Gear verdict of the shape the server now returns. `name` is what the
// Archive calls the file; `file` is what genesis_plus_gx has to see, and the
// two differ for exactly the machines whose core cannot tell which console it
// is looking at from the bytes.
const gameGearVerdict = {
  id: 'gg_Pac-Man_1990Namco',
  title: 'Pac-Man',
  emulator: 'gamegear',
  platform: 'gamegear',
  system: 'Game Gear',
  playable: true,
  route: 'emulatorjs',
  core: 'segaGG',
  coreFile: 'genesis_plus_gx',
  rom: {
    name: 'Pac-Man_1990Namco.bin',
    file: 'Pac-Man_1990Namco.gg',
    fetch: 'https://archive.org/cors/gg_Pac-Man_1990Namco/Pac-Man_1990Namco.bin',
    url: '/bridge/iptv?u=https%3A%2F%2Farchive.org%2Fdownload%2Fgg_Pac-Man_1990Namco%2FPac-Man_1990Namco.bin',
    sizeBytes: 131072,
  },
  embed: 'https://archive.org/embed/gg_Pac-Man_1990Namco',
  touch: true,
  streamOnly: true,
};

const bytes = (n) => ({ ok: true, status: 200, arrayBuffer: async () => new ArrayBuffer(n) });

// THE BUG THIS WHOLE PATH IS ABOUT. EmulatorJS names the file it writes into the
// core's filesystem after the URL it downloaded, and genesis_plus_gx reads that
// extension to pick between the five machines it emulates. Measured: the same
// Game Gear ROM called `.gg` runs at 160x144 and plays; called `.bin`, or called
// `iptv` because it came through our relay, it runs at 256x192 as a Master
// System and draws a black screen while reporting itself started.
test('the core is told the name the machine needs, not the one the Archive uses', async () => {
  const doc = fakeDocument();
  await boot(toPlayable(normalise(gameGearVerdict), {
    doc, route: ROUTE.EMULATORJS, fetchImpl: async () => bytes(131072),
  }));
  assert.equal(globalThis.EJS_gameName, 'Pac-Man_1990Namco.gg');
  assert.equal(globalThis.EJS_core, 'segaGG');
});

// The button is for a person, so it says the name the item actually has.
test('the button shows the Archive’s name even when the core is told another', () => {
  const playable = toPlayable(normalise(gameGearVerdict), { doc: fakeDocument() });
  const el = fakeElement();
  playable.mount(el);
  assert.match(el.children[0].textContent, /Pac-Man_1990Namco\.bin/);
});

// An older server sends neither `file` nor `fetch`. That must keep working and
// must not start renaming things on its own -- a second copy of the machine
// table here is the thing the server holds one for.
test('a verdict with no file field falls back to the Archive’s own name', async () => {
  const doc = fakeDocument();
  await boot(toPlayable(normalise(nesVerdict), {
    doc, route: ROUTE.EMULATORJS, fetchImpl: async () => bytes(24592),
  }));
  assert.equal(globalThis.EJS_gameName, 'pacman.nes');
});

test('the Archive’s own CORS endpoint is tried before our relay', async () => {
  const asked = [];
  await fetchRom(
    [gameGearVerdict.rom.fetch, gameGearVerdict.rom.url],
    { expectBytes: 131072, fetchImpl: async (u) => { asked.push(u); return bytes(131072); } },
  );
  assert.deepEqual(asked, [gameGearVerdict.rom.fetch]);
  assert.ok(asked[0].startsWith('https://archive.org/cors/'), 'the free path should be first');
});

// Measured: archive.org's storage nodes answer 5xx for roughly one request in a
// hundred, never twice for the same item. Falling back turns that from a dead
// game into a slower one.
test('a 5xx from the Archive falls through to the relay rather than failing', async () => {
  const asked = [];
  const buf = await fetchRom(
    [gameGearVerdict.rom.fetch, gameGearVerdict.rom.url],
    {
      expectBytes: 131072,
      fetchImpl: async (u) => {
        asked.push(u);
        return u.startsWith('https://') ? { ok: false, status: 503 } : bytes(131072);
      },
    },
  );
  assert.equal(buf.byteLength, 131072);
  assert.equal(asked.length, 2);
  assert.ok(asked[1].startsWith('/bridge/iptv'));
});

// THE SILENT ONE. A relay that passes an upstream error through returns a short
// HTML body with a 200-shaped read; a WASM emulator handed 170 bytes of HTML
// boots, reports itself started, and runs nothing. A length that disagrees with
// the verdict is therefore a failed fetch, not a ROM.
test('a body of the wrong length is refused instead of booted', async () => {
  const asked = [];
  const buf = await fetchRom(
    [gameGearVerdict.rom.fetch, gameGearVerdict.rom.url],
    {
      expectBytes: 131072,
      fetchImpl: async (u) => {
        asked.push(u);
        return u.startsWith('https://') ? bytes(170) : bytes(131072);
      },
    },
  );
  assert.equal(buf.byteLength, 131072);
  assert.equal(asked.length, 2, 'the short body should not have been accepted');
});

test('an empty body is refused too', async () => {
  await assert.rejects(
    fetchRom(['/only'], { expectBytes: 0, fetchImpl: async () => bytes(0) }),
    /could not be fetched/,
  );
});

// When everything fails the message has to say what was tried, because "it did
// not work" is not something anybody can act on.
test('when no source yields the ROM the failure names what it tried', async () => {
  await assert.rejects(
    fetchRom(
      ['https://archive.org/cors/x/y.bin', '/bridge/iptv?u=z'],
      { expectBytes: 100, fetchImpl: async () => ({ ok: false, status: 503 }) },
    ),
    (error) => {
      assert.equal(error.name, 'PlaybackError');
      assert.match(error.message, /archive\.org/);
      assert.match(error.message, /bridge/);
      assert.match(error.message, /503/);
      return true;
    },
  );
});

// A fetch that throws (offline, blocked, CORS refused) is a source that did not
// work, not an exception that escapes the loop and kills the fallback.
test('a throwing source is just another source that failed', async () => {
  const buf = await fetchRom(
    ['https://archive.org/cors/x/y.bin', '/bridge/iptv?u=z'],
    {
      expectBytes: 8,
      fetchImpl: async (u) => {
        if (u.startsWith('https://')) throw new TypeError('Failed to fetch');
        return bytes(8);
      },
    },
  );
  assert.equal(buf.byteLength, 8);
});

// A failed fetch must SAY so. Booting an emulator with no ROM produces a black
// screen that reports itself as running, which is the exact failure mode this
// whole path exists to remove.
test('a ROM that cannot be fetched leaves a message, not an empty emulator', async () => {
  const doc = fakeDocument();
  const before = globalThis.EJS_gameUrl;
  const host = await boot(toPlayable(normalise(gameGearVerdict), {
    doc, route: ROUTE.EMULATORJS, fetchImpl: async () => ({ ok: false, status: 503 }),
  }));
  assert.match(host.children[0].textContent, /could not be started/);
  assert.equal(globalThis.EJS_gameUrl, before, 'no emulator should have been configured');
});

// --- what THIS device can hold ------------------------------------------------

import {
  deviceMemoryGiB, romBudgetBytes, romFitsDevice, bytesLabel,
  RAM_MULTIPLE_PER_ROM, LOW_END_DEVICE_GIB,
} from './play.js';

/**
 * The third device fact, alongside touch and cross-origin isolation.
 *
 * play_archive.go stops at 512 MiB because that is what SOME browser might hold.
 * Whether THIS browser holds it is a fact about the handset, and the same 426 MiB
 * PlayStation disc is a good game on a laptop and a killed tab on a phone. The
 * rule below is measured rather than guessed -- the ROM is resident three times
 * over while it plays, so a device needs roughly sixteen times the file in RAM.
 *
 * The assertions come in pairs for the reason the top of this file gives: one
 * that says yes and one that says no, because a check that only ever allowed
 * would pass every "does it play" test and none of the ones that matter.
 */

/** A 426 MiB PlayStation disc, the size this whole change is about. */
const discVerdict = {
  ...nesVerdict,
  id: 'psx_kasparov',
  emulator: 'psx',
  platform: 'psx',
  system: 'PlayStation',
  core: 'psx',
  coreFile: 'pcsx_rearmed',
  rom: {
    name: 'playstationdisc.chd',
    file: 'playstationdisc.chd',
    fetch: 'https://archive.org/cors/psx_kasparov/playstationdisc.chd',
    url: '',
    sizeBytes: 446954699,
  },
  embed: 'https://archive.org/embed/psx_kasparov',
};

test('a device that did not say is treated as unknown, never as small', () => {
  assert.equal(deviceMemoryGiB({}), 0);
  assert.equal(deviceMemoryGiB({ deviceMemory: 0 }), 0);
  assert.equal(deviceMemoryGiB(undefined), 0);
  assert.equal(romBudgetBytes(0), Infinity,
    'refusing on a missing field would take Play off every desktop Safari');
  assert.equal(romFitsDevice(discVerdict, 0), true);
});

test('the budget is the measured multiple of what the device reports', () => {
  assert.equal(romBudgetBytes(8), Math.floor((8 * 1024 ** 3) / RAM_MULTIPLE_PER_ROM));
  assert.equal(romBudgetBytes(LOW_END_DEVICE_GIB), 256 * 1024 * 1024,
    'a 4 GiB phone holds every cartridge ever made and no disc at all');
});

test('a 426 MiB disc plays on a laptop and is refused on a phone', () => {
  assert.equal(romFitsDevice(discVerdict, 8), true, '8 GiB holds a PlayStation disc');
  assert.equal(romFitsDevice(discVerdict, 4), false, '4 GiB does not');

  const [oursBig] = playerOptions(normalise(discVerdict), { memoryGiB: 8 });
  assert.equal(oursBig.available, true);

  const [oursSmall, theirsSmall] = playerOptions(normalise(discVerdict), { memoryGiB: 4 });
  assert.equal(oursSmall.available, false);
  assert.match(oursSmall.why, /426\.2 MiB/, 'the refusal names the size');
  assert.match(oursSmall.why, /this device/, 'and blames the device, not the game');
  assert.equal(theirsSmall.available, true,
    'the Archive streams it, which is a real fallback and not a consolation prize');
});

test('a phone is sent to the Archive rather than shown a button that dies', () => {
  const verdict = normalise(discVerdict);
  assert.equal(choosePlayer(verdict, '', { memoryGiB: 4 }), ROUTE.ARCHIVE);
  assert.equal(choosePlayer(verdict, '', { memoryGiB: 8 }), ROUTE.EMULATORJS);

  // And the remembered preference cannot override a device fact: a viewer who
  // once chose our player does not thereby gain memory.
  assert.equal(choosePlayer(verdict, ROUTE.EMULATORJS, { memoryGiB: 4 }), ROUTE.ARCHIVE);
  assert.equal(canSwitchPlayer(verdict, { memoryGiB: 4 }), false);
  assert.match(solePlayerSentence(verdict, { memoryGiB: 4 }), /Internet Archive player only/);
});

test('the label follows the switch, and warns where nothing can be known', () => {
  const verdict = normalise(discVerdict);

  const onPhone = playLabel(verdict, { memoryGiB: 4 });
  assert.equal(onPhone.label, 'Play at the Internet Archive',
    'a button that cannot deliver must not say "Play"');

  const onLaptop = playLabel(verdict, { memoryGiB: 8 });
  assert.equal(onLaptop.label, 'Play');
  assert.equal(onLaptop.caveat, '', 'a device that answered needs no warning');

  // OFFER IT BUT WARN: Safari and Firefox report nothing, so there is no fact to
  // refuse on and the size is said out loud instead.
  const unknown = playLabel(verdict, { memoryGiB: 0 });
  assert.equal(unknown.label, 'Play');
  assert.match(unknown.caveat, /426\.2 MiB/);
  assert.match(unknown.caveat, /phone/);

  // A cartridge is never warned about on any device.
  assert.equal(playLabel(normalise(nesVerdict), { memoryGiB: 0 }).caveat, '');
});

test('toPlayable refuses the route the device cannot run', () => {
  const verdict = normalise(discVerdict);
  assert.throws(
    () => toPlayable(verdict, { route: ROUTE.EMULATORJS, memoryGiB: 4 }),
    /more than this device/,
    'the switch may not be used to hand-build the dead button',
  );
  const ok = toPlayable(verdict, { route: ROUTE.ARCHIVE, memoryGiB: 4 });
  assert.equal(ok.render, RENDER.EMBED);
});

test('sizes read like a sentence, the way the server writes them', () => {
  assert.equal(bytesLabel(512), '512 B');
  assert.equal(bytesLabel(24592), '24.0 KiB');
  assert.equal(bytesLabel(50331648), '48.0 MiB');
  assert.equal(bytesLabel(446954699), '426.2 MiB');
  assert.equal(bytesLabel(536870912), '512.0 MiB');
});

// --- the relay is a fallback now, not the path --------------------------------

/**
 * The server withholds `rom.url` for anything past what it can afford to relay.
 * A verdict with only `fetch` is therefore NORMAL for a disc, and treating it as
 * broken would refuse every large game the change exists to allow.
 */
test('a ROM with only the direct path is playable, with one source', async () => {
  const verdict = normalise(discVerdict);
  assert.ok(verdict.rom, 'a fetch-only ROM survives normalise');
  assert.equal(verdict.route, ROUTE.EMULATORJS);

  const asked = [];
  const doc = fakeDocument();
  await boot(toPlayable(verdict, {
    doc,
    route: ROUTE.EMULATORJS,
    memoryGiB: 8,
    fetchImpl: async (u) => {
      asked.push(u);
      return { ok: true, status: 200, arrayBuffer: async () => new ArrayBuffer(446954699) };
    },
  }));
  assert.deepEqual(asked, [discVerdict.rom.fetch],
    'no relay is tried, because none was offered');
});

test('a device refusal is explained, not silently rerouted', () => {
  const { label, hint } = playLabel(normalise(discVerdict), { memoryGiB: 4 });
  assert.equal(label, 'Play at the Internet Archive');
  assert.match(hint, /426\.2 MiB/,
    'the server said nothing here, so the device sentence is the only true one');
});
