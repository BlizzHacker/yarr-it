import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  ROUTE, REASON, normalise, normaliseBios, toPlayable, BIOS_SOURCE, BIOS_PATH,
} from './play.js';

/**
 * Firmware from the household's own library.
 *
 * THE CHANGE THIS FILE IS ABOUT: three machines -- ColecoVision, PlayStation,
 * Amiga -- are refused because console firmware is not this project's to ship.
 * Somebody whose own library server has held that firmware for years was still
 * being shown a screen asking them to go and find it.
 *
 * The server now says WHOSE firmware a game is running with, and where to fetch
 * the library's copy from. That last part is the only field in a verdict that
 * turns into a NETWORK DESTINATION, which is why these tests are hardest on it:
 * everything else in a verdict costs a button if it is wrong, and that one field
 * would cost a browser aimed somewhere on somebody else's say-so.
 */

// --- fixtures ----------------------------------------------------------------

// A verdict for a machine the library can unblock: the shape play_archive.go
// returns once the firmware source has found the file. The names and sizes are
// what a real RomM answered on 2026-08-08 -- the ColecoVision BIOS really is
// filed as `coleco.rom`, not `colecovision.rom`, and gearcoleco really does
// accept both.
const libraryBios = {
  id: 'dkong_coleco',
  title: 'Donkey Kong (ColecoVision)',
  domain: 'game',
  type: 'release',
  emulator: 'coleco',
  platform: 'colecovision',
  system: 'ColecoVision',
  playable: true,
  route: 'emulatorjs',
  core: 'coleco',
  coreFile: 'gearcoleco',
  rom: {
    name: 'dkong.col',
    url: '/bridge/iptv?u=https%3A%2F%2Farchive.org%2Fdownload%2Fdkong_coleco%2Fdkong.col',
    direct: 'https://archive.org/download/dkong_coleco/dkong.col',
    sizeBytes: 16384,
  },
  embed: 'https://archive.org/embed/dkong_coleco',
  touch: true,
  biosNeeded: {
    system: 'coleco',
    label: 'ColecoVision BIOS',
    files: ['colecovision.rom', 'coleco.rom'],
    detail: 'gearcoleco marks colecovision.rom as required firmware.',
    source: 'library',
    file: 'coleco.rom',
    url: '/api/play/bios/library/coleco/coleco.rom',
    sizeBytes: 8192,
  },
};

// The refusal, exactly as it arrives from a server with no library to ask.
const noFirmwareAnywhere = {
  id: 'dkong_coleco',
  emulator: 'coleco',
  platform: 'colecovision',
  system: 'ColecoVision',
  playable: true,
  route: 'archive',
  embed: 'https://archive.org/embed/dkong_coleco',
  touch: false,
  reasons: [{
    code: 'needs_bios',
    detail: 'ColecoVision games need the console BIOS, which is not ours to ship.',
  }],
  biosNeeded: {
    system: 'coleco',
    label: 'ColecoVision BIOS',
    files: ['colecovision.rom', 'coleco.rom'],
    detail: 'Supply your own copy and ColecoVision games play here.',
  },
};

const nesNoFirmware = {
  id: 'pacman_nes_2',
  emulator: 'nes',
  platform: 'nes',
  system: 'NES',
  playable: true,
  route: 'emulatorjs',
  core: 'nes',
  coreFile: 'fceumm',
  rom: { name: 'pacman.nes', url: '/bridge/iptv?u=x', direct: 'x', sizeBytes: 24592 },
  embed: 'https://archive.org/embed/pacman_nes_2',
  touch: true,
};

// --- just enough DOM to boot the emulator ------------------------------------

function fakeElement(tagName = 'DIV') {
  return {
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
}

function fakeDocument() {
  return { body: fakeElement('BODY'), createElement: (tag) => fakeElement(tag.toUpperCase()) };
}

// The player fetches the ROM itself before booting, so that the name the core
// sees is ours to set rather than whatever the URL happened to end in. A boot in
// a test therefore needs bytes to boot on, and the right NUMBER of them: a
// length that disagrees with the verdict is treated as a failed fetch, which is
// how a truncated body or an error page is caught.
function romFetch(bytes) {
  return async () => ({
    ok: true,
    status: 200,
    arrayBuffer: async () => new ArrayBuffer(bytes),
  });
}

// A WASM emulator needs a real user gesture before the browser will let it run,
// so mountEmulator hangs the boot off a click. Press it the way a person would,
// and wait: the click handler fetches the ROM before there is an emulator.
async function boot(playable) {
  const host = fakeElement('DIV');
  playable.mount(host);
  const [button] = host.children;
  for (const fn of button.listeners.click ?? []) await fn();
  return host;
}

// --- what the verdict says ---------------------------------------------------

test('the library firmware a verdict names survives normalisation', () => {
  const verdict = normalise(libraryBios);
  assert.equal(verdict.route, ROUTE.EMULATORJS);
  assert.equal(verdict.biosNeeded.source, BIOS_SOURCE.LIBRARY);
  assert.equal(verdict.biosNeeded.file, 'coleco.rom');
  assert.equal(verdict.biosNeeded.url, '/api/play/bios/library/coleco/coleco.rom');
  assert.equal(verdict.biosNeeded.sizeBytes, 8192);
  assert.deepEqual(verdict.biosNeeded.files, ['colecovision.rom', 'coleco.rom']);
});

// An invitation -- "you could supply this" -- must never look like firmware in
// use. A panel saying "running with your library's copy" over a game with no
// firmware would describe the exact silent failure this whole area keeps hitting.
test('an offer with no firmware in use names no source', () => {
  const verdict = normalise(noFirmwareAnywhere);
  assert.equal(verdict.biosNeeded.system, 'coleco');
  assert.equal(verdict.biosNeeded.source, '');
  assert.equal(verdict.biosNeeded.url, '');
  assert.equal(verdict.biosNeeded.file, '');
});

// THE ONE FIELD THAT BECOMES A NETWORK DESTINATION. The server has no reason to
// send anything but our own relay path. If anything between here and it ever
// did, the cost must be firmware that does not load -- never a browser aimed
// somewhere else, and never the library's own address, which needs a credential
// this browser must never have.
test('a firmware URL that is not our own relay is dropped, and takes its claim with it', () => {
  const hostile = [
    'https://evil.example/coleco.rom',
    '//evil.example/coleco.rom',
    'http://10.0.0.1:8080/api/firmware/635/content/coleco.rom',
    '/api/play/bios/../../etc/passwd',
    'javascript:alert(1)',
    '/bridge/iptv?u=https://evil.example/x',
    'data:application/octet-stream;base64,AAAA',
    '',
  ];
  for (const url of hostile) {
    const verdict = normalise({
      ...libraryBios,
      biosNeeded: { ...libraryBios.biosNeeded, url },
    });
    assert.equal(verdict.biosNeeded.url, '', `${url} was accepted as a firmware URL`);
    // And with nowhere to fetch it from it is no longer a library source: there
    // would be nothing to hand the emulator, and saying otherwise is the lie.
    assert.equal(verdict.biosNeeded.source, '', `${url} still claimed a library source`);
  }
});

test('an unrecognised source is treated as no source at all', () => {
  for (const source of ['operator', 'romm', 'ours', 1, null, {}]) {
    const verdict = normalise({
      ...libraryBios,
      biosNeeded: { ...libraryBios.biosNeeded, source },
    });
    assert.equal(verdict.biosNeeded.source, '');
  }
});

test('normaliseBios refuses anything that is not an offer', () => {
  for (const bad of [null, undefined, 'text', [], {}, { label: 'x' }]) {
    assert.equal(normaliseBios(bad), null);
  }
  assert.ok(BIOS_PATH.startsWith('/'), 'the relay path must be relative to this server');
});

// --- what the emulator is handed ---------------------------------------------

test('the emulator is pointed at the library copy when this browser holds nothing', async () => {
  const doc = fakeDocument();
  await boot(toPlayable(normalise(libraryBios), {
    doc, route: ROUTE.EMULATORJS, fetchImpl: romFetch(16384),
  }));
  assert.equal(globalThis.EJS_biosUrl, '/api/play/bios/library/coleco/coleco.rom');
});

// PRECEDENCE, and it is not a technical preference. Somebody who went and found
// a specific Kickstart revision because the game they want needs it has said
// something, and quietly running the library's copy instead would be overruling
// them about their own machine.
test('a file this browser holds beats the library copy', async () => {
  const doc = fakeDocument();
  await boot(toPlayable(normalise(libraryBios), {
    doc, route: ROUTE.EMULATORJS, biosUrl: 'blob:the-one-they-chose',
    fetchImpl: romFetch(16384),
  }));
  assert.equal(globalThis.EJS_biosUrl, 'blob:the-one-they-chose');
});

// EJS_biosUrl is a GLOBAL and outlives the game that set it. A library BIOS left
// behind would be handed to the next game, and a Kickstart ROM fed to an NES core
// is a black screen with no error anywhere.
test('the library firmware is cleared before the next game', async () => {
  const doc = fakeDocument();
  await boot(toPlayable(normalise(libraryBios), {
    doc, route: ROUTE.EMULATORJS, fetchImpl: romFetch(16384),
  }));
  assert.equal(globalThis.EJS_biosUrl, '/api/play/bios/library/coleco/coleco.rom');

  await boot(toPlayable(normalise(nesNoFirmware), {
    doc, route: ROUTE.EMULATORJS, fetchImpl: romFetch(24592),
  }));
  assert.equal(globalThis.EJS_biosUrl, '');
});

// The name at the end of that URL is not decoration, and this is the trap that
// cost the previous attempt at this feature its proof. EmulatorJS writes the
// firmware into the emulator's filesystem under the LAST PATH SEGMENT of the URL
// it fetched it from; gearcoleco then looks for `colecovision.rom` and falls back
// to `coleco.rom`. A URL ending in anything else -- including the UUID at the end
// of every blob: URL -- is a core that reports NO BIOS over a game that never
// booted, at a healthy frame rate, with nothing downstream able to tell.
test('the firmware URL ends in a name the core actually looks for', () => {
  const { biosNeeded } = normalise(libraryBios);
  const last = biosNeeded.url.split('/').pop();
  assert.equal(last, biosNeeded.file);
  assert.ok(biosNeeded.files.includes(last), `${last} is not a name gearcoleco asks for`);
});

// --- degradation --------------------------------------------------------------

// A server that has never heard of any of this -- an older build, a household
// with no library, a library that is down -- must produce exactly what it
// produced before, which is the offer to supply a file by hand.
test('with no library anywhere the answer is the refusal it always was', async () => {
  const verdict = normalise(noFirmwareAnywhere);
  assert.equal(verdict.route, ROUTE.ARCHIVE);
  assert.equal(verdict.playable, true);
  assert.equal(verdict.reasons[0].code, REASON.NEEDS_BIOS);
  assert.equal(verdict.biosNeeded.label, 'ColecoVision BIOS');
  assert.equal(verdict.biosNeeded.source, '');

  // And nothing is handed to the emulator, because there is nothing to hand it.
  const doc = fakeDocument();
  await boot(toPlayable(normalise(nesNoFirmware), {
    doc, route: ROUTE.EMULATORJS, fetchImpl: romFetch(24592),
  }));
  assert.equal(globalThis.EJS_biosUrl, '');
});
