import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  createBiosStore, memoryBackend, MAX_BIOS_BYTES, BIOS_REFUSAL, BiosError,
} from './bios.js';

/**
 * Three machines are refused only because console firmware is not this
 * project's to ship. It IS the console owner's to use, so they may supply it --
 * and the whole legitimacy of that rests on one property: the file never leaves
 * their browser. These tests are about the policy that makes it safe (which
 * machines, how big, what happens to a mistake) and about that one property.
 */

const ALLOWED = ['amiga', 'coleco', 'psx'];
const bytes = (n) => new Uint8Array(n).fill(7);

const store = (opts = {}) => createBiosStore({ backend: memoryBackend(), allowed: ALLOWED, ...opts });

test('a BIOS is stored and comes back described, without its bytes', async () => {
  const s = store();
  const saved = await s.put('coleco', { name: 'colecovision.rom', bytes: bytes(8192) });

  assert.equal(saved.system, 'coleco');
  assert.equal(saved.name, 'colecovision.rom');
  assert.equal(saved.size, 8192);
  assert.equal(saved.bytes, undefined, 'a listing must never carry the firmware itself');

  assert.deepEqual(await s.declared(), ['coleco']);
  assert.equal((await s.info('coleco')).size, 8192);
});

// The only list that decides this comes from the server -- /api/play/systems
// publishes `bios[].system`. A local table here would be a fourth copy of a map
// that already exists in Go, and the copies would drift.
test('only a machine the server says is unlockable may be stored', async () => {
  const s = store();
  for (const wrong of ['nes', 'colecovision', 'dos', '', null, undefined]) {
    await assert.rejects(
      () => s.put(wrong, { bytes: bytes(1024) }),
      (err) => {
        assert.ok(err instanceof BiosError);
        assert.equal(err.code, BIOS_REFUSAL.UNKNOWN_SYSTEM);
        return true;
      },
      `${wrong} was accepted`,
    );
  }
  assert.deepEqual(await s.declared(), []);
});

// The mistake people actually make is picking the game instead of the firmware,
// and the failure mode without this check is silent: a 3 MB ROM stored as a BIOS
// makes the emulator boot, draw its own error screen, and report itself started.
test('something far too large to be firmware is refused with a reason', async () => {
  const s = store();
  await assert.rejects(
    () => s.put('psx', { name: 'game.chd', bytes: bytes(MAX_BIOS_BYTES + 1) }),
    (err) => {
      assert.equal(err.code, BIOS_REFUSAL.TOO_LARGE);
      assert.match(err.message, /probably the game rather than the firmware/);
      return true;
    },
  );
  await assert.rejects(
    () => s.put('psx', { name: 'empty.bin', bytes: new Uint8Array(0) }),
    (err) => {
      assert.equal(err.code, BIOS_REFUSAL.EMPTY);
      return true;
    },
  );
});

test('a real PlayStation BIOS is comfortably inside the ceiling', async () => {
  // scph5501.bin is 512 KB. The ceiling exists to catch a game, not to be tight.
  const s = store();
  const saved = await s.put('psx', { name: 'scph5501.bin', bytes: bytes(512 * 1024) });
  assert.equal(saved.size, 512 * 1024);
});

test('storing again replaces rather than accumulating', async () => {
  const s = store();
  await s.put('amiga', { name: 'kick34005.A500', bytes: bytes(256 * 1024) });
  await s.put('amiga', { name: 'kick40068.A1200', bytes: bytes(512 * 1024) });

  const all = await s.list();
  assert.equal(all.length, 1);
  assert.equal(all[0].name, 'kick40068.A1200');
});

test('removing one leaves the others alone', async () => {
  const s = store();
  await s.put('coleco', { bytes: bytes(8192) });
  await s.put('psx', { bytes: bytes(1024) });
  await s.remove('coleco');

  assert.deepEqual(await s.declared(), ['psx']);
  assert.equal(await s.info('coleco'), null);
});

// The declaration is what goes on the query string, so it has to be stable:
// an unsorted list would produce a different URL for the same browser and defeat
// any caching in front of the play service.
test('the declaration is sorted and free of duplicates', async () => {
  const s = store();
  await s.put('psx', { bytes: bytes(16) });
  await s.put('amiga', { bytes: bytes(16) });
  await s.put('coleco', { bytes: bytes(16) });
  assert.deepEqual(await s.declared(), ['amiga', 'coleco', 'psx']);
});

// THE property the whole feature rests on. The only way bytes leave this module
// is as a blob URL for the emulator in this same tab: there is no method that
// takes a destination, and none that returns the bytes themselves.
test('nothing in the public surface can send firmware anywhere', async () => {
  const s = store();
  await s.put('coleco', { bytes: bytes(8192) });

  const surface = Object.keys(s).sort();
  assert.deepEqual(surface, ['declared', 'info', 'list', 'objectURL', 'put', 'remove', 'systems']);

  for (const [name, value] of Object.entries(s)) {
    if (typeof value !== 'function') continue;
    const source = value.toString();
    assert.doesNotMatch(source, /fetch|XMLHttpRequest|sendBeacon|WebSocket|https?:/i,
      `${name}() references a network primitive`);
  }
  // And the two read paths return descriptions, never the firmware.
  assert.equal((await s.info('coleco')).bytes, undefined);
  assert.equal((await s.list())[0].bytes, undefined);
});

test('the emulator gets a blob URL made from the stored bytes', async () => {
  const s = store();
  await s.put('coleco', { name: 'colecovision.rom', bytes: bytes(8192) });

  const seen = [];
  const url = await s.objectURL('coleco', {
    createObjectURL: (blob) => { seen.push(blob); return 'blob:fake-1'; },
    Blob: class { constructor(parts, opts) { this.parts = parts; this.type = opts?.type; } },
  });

  assert.equal(url, 'blob:fake-1');
  assert.equal(seen.length, 1);
  assert.equal(seen[0].type, 'application/octet-stream');
  assert.equal(seen[0].parts[0].byteLength, 8192);

  assert.equal(await s.objectURL('psx', { createObjectURL: () => 'x', Blob: class {} }), null,
    'a machine with nothing stored must not produce a URL for empty firmware');
});

// Storage that is unavailable or broken must cost the feature, never the page.
test('a backend that throws leaves an empty list rather than an exception', async () => {
  const angry = {
    async get() { throw new Error('nope'); },
    async put() { throw new Error('nope'); },
    async remove() { throw new Error('nope'); },
    async keys() { throw new Error('nope'); },
  };
  const s = createBiosStore({ backend: angry, allowed: ALLOWED });

  assert.deepEqual(await s.list(), []);
  assert.deepEqual(await s.declared(), []);
  assert.equal(await s.info('coleco'), null);
  assert.equal(await s.objectURL('coleco', { createObjectURL: () => 'x', Blob: class {} }), null);
});

// A File's ArrayBuffer can be detached by some transfer paths, and a stored
// reference to a detached buffer reads as an empty BIOS -- which boots and shows
// the emulator's own error screen rather than an error of ours.
test('the stored copy survives the caller reusing its buffer', async () => {
  const s = store();
  const buffer = new Uint8Array([1, 2, 3, 4]);
  await s.put('coleco', { bytes: buffer });
  buffer.fill(0);

  const seen = [];
  await s.objectURL('coleco', {
    createObjectURL: (blob) => { seen.push(blob.parts[0]); return 'blob:x'; },
    Blob: class { constructor(parts) { this.parts = parts; } },
  });
  assert.deepEqual([...seen[0]], [1, 2, 3, 4]);
});

// An empty list means the server could not be asked. Accepting anything then
// would file firmware under a name nothing will ever look up -- a silent no-op
// that looks exactly like success, which is the failure this whole area of the
// codebase keeps being bitten by.
test('with no allow-list the store accepts nothing rather than everything', async () => {
  const s = createBiosStore({ backend: memoryBackend(), allowed: [] });
  assert.deepEqual(s.systems(), []);
  for (const name of ['coleco', 'psx', 'amiga', 'anything']) {
    await assert.rejects(() => s.put(name, { bytes: bytes(16) }), (err) => {
      assert.equal(err.code, BIOS_REFUSAL.UNKNOWN_SYSTEM);
      return true;
    }, `${name} was stored with no server list to check it against`);
  }
});
