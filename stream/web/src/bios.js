/**
 * Bring your own BIOS.
 *
 * THREE MACHINES ARE REFUSED HERE FOR A REASON THAT IS NOT ABOUT THE MACHINE.
 * ColecoVision, PlayStation and Amiga need console firmware, and that firmware
 * is not this project's to ship. It is, however, the console owner's to use.
 * Somebody with a ColecoVision in a cupboard has every right to the BIOS in it,
 * and refusing to let them supply it turns a licensing fact into a capability we
 * simply do not have. RomM has let operators drop firmware in for years; this is
 * the same thing for a visitor who installs nothing.
 *
 * WHERE THE BYTES GO, AND WHERE THEY DO NOT.
 *
 * Into this browser's IndexedDB and nowhere else. There is no upload in this
 * file and no URL for one to go to: the only way a stored file leaves this
 * module is as a `blob:` URL handed to the emulator running in the same tab.
 * That is not a policy that could be relaxed later without rewriting the module,
 * which is the point -- the legal position that makes this legitimate is
 * precisely that the file never leaves the machine it was already on.
 *
 * WHY THE STORAGE IS INJECTED.
 *
 * `backend` is a four-method interface rather than IndexedDB directly, because
 * the part worth testing is the policy -- which systems may be stored, how big a
 * BIOS can be, what happens to a file that is obviously not one -- and none of
 * that needs a database. `indexedDBBackend()` is the real one and is thin enough
 * to read in one sitting; `memoryBackend()` is what the tests use.
 */

/**
 * A BIOS is small. Every file in libretro's core-info lists for the three
 * machines here is between 8 KB (colecovision.rom) and 512 KB (a Kickstart ROM
 * or a PlayStation BIOS). Two megabytes is generous by a factor of four and
 * still rejects the thing people actually do by mistake, which is picking the
 * game instead of the firmware.
 */
export const MAX_BIOS_BYTES = 2 << 20;

/** Storage names, kept in one place so the real and fake backends agree. */
export const DB_NAME = 'yarrit-bios';
export const STORE_NAME = 'bios';

/**
 * The refusals, as codes. A UI branches on the code; the sentence is written to
 * be read and may be reworded.
 */
export const BIOS_REFUSAL = {
  UNKNOWN_SYSTEM: 'unknown_system',
  TOO_LARGE: 'too_large',
  EMPTY: 'empty',
  UNAVAILABLE: 'unavailable',
};

export class BiosError extends Error {
  constructor(code, message) {
    super(message);
    this.name = 'BiosError';
    this.code = code;
  }
}

/**
 * An in-memory backend. Used by the tests, and also the honest fallback when a
 * browser has no IndexedDB at all (private mode in some webviews): the BIOS then
 * lasts for the session rather than not working, which is the better failure.
 */
export function memoryBackend() {
  const map = new Map();
  return {
    async get(key) { return map.get(key) ?? null; },
    async put(key, value) { map.set(key, value); },
    async remove(key) { map.delete(key); },
    async keys() { return [...map.keys()]; },
  };
}

/**
 * The real backend.
 *
 * Deliberately untested by the suite: it is a thin adapter over an API that node
 * does not have, and a fake IndexedDB would be testing the fake. Everything that
 * makes a decision lives above this line and is tested there.
 */
export function indexedDBBackend(factory = globalThis.indexedDB) {
  if (!factory) return null;

  const open = () => new Promise((resolve, reject) => {
    const request = factory.open(DB_NAME, 1);
    request.onupgradeneeded = () => {
      if (!request.result.objectStoreNames.contains(STORE_NAME)) {
        request.result.createObjectStore(STORE_NAME);
      }
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });

  const run = (mode, fn) => open().then((db) => new Promise((resolve, reject) => {
    const tx = db.transaction(STORE_NAME, mode);
    const request = fn(tx.objectStore(STORE_NAME));
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
    tx.oncomplete = () => db.close();
  }));

  return {
    get: (key) => run('readonly', (store) => store.get(key)),
    put: (key, value) => run('readwrite', (store) => store.put(value, key)),
    remove: (key) => run('readwrite', (store) => store.delete(key)),
    keys: () => run('readonly', (store) => store.getAllKeys()),
  };
}

/** Whichever backend this environment can actually provide. */
export function defaultBackend() {
  return indexedDBBackend() ?? memoryBackend();
}

/**
 * Create the store.
 *
 * `allowed` is the set of EmulatorJS system names the SERVER says can be
 * unblocked by firmware -- `/api/play/systems` publishes it as `bios[].system`.
 * Passing it in rather than hard-coding a list here is what stops this file
 * becoming a fourth copy of a table that already exists in Go: if the server
 * learns a fourth machine, this learns it the same day, and if somebody types
 * `colecovision` where the server said `coleco` it is refused rather than stored
 * under a name nothing will ever look up.
 */
export function createBiosStore({ backend = defaultBackend(), allowed = [] } = {}) {
  const permitted = new Set(allowed.filter(Boolean));

  // An EMPTY allow-list permits nothing, which is deliberate and is the
  // safe direction. Empty means the server could not be asked, and firmware
  // filed under a name the server does not use is firmware nothing will ever
  // look up -- a silent no-op that looks like it worked.
  const guard = (system) => {
    const name = String(system ?? '').trim();
    if (!name || !permitted.has(name)) {
      throw new BiosError(
        BIOS_REFUSAL.UNKNOWN_SYSTEM,
        `${name || 'That machine'} is not one this player can unlock with a BIOS file.`,
      );
    }
    return name;
  };

  const store = {
    /** Which machines can be unlocked at all. */
    systems() {
      return [...permitted];
    },

    /**
     * Store one file. `bytes` is an ArrayBuffer or a typed array -- whatever
     * `file.arrayBuffer()` gave the caller.
     */
    async put(system, { name = '', bytes } = {}) {
      const key = guard(system);
      const size = bytes?.byteLength ?? bytes?.length ?? 0;
      if (!size) {
        throw new BiosError(BIOS_REFUSAL.EMPTY, 'that file is empty.');
      }
      if (size > MAX_BIOS_BYTES) {
        throw new BiosError(
          BIOS_REFUSAL.TOO_LARGE,
          `that file is ${Math.round(size / 1024)} KB. A console BIOS is a few `
          + 'hundred kilobytes at most, so this is probably the game rather than the firmware.',
        );
      }
      const record = {
        system: key,
        name: String(name || 'bios.bin'),
        size,
        // A copy, not the caller's buffer: a File's ArrayBuffer is detached by
        // some transfer paths and a stored reference to a detached buffer reads
        // as an empty BIOS, which boots and shows the emulator's error screen.
        bytes: new Uint8Array(bytes.buffer ?? bytes).slice(),
        addedAt: Date.now(),
      };
      await backend.put(key, record);
      return summarise(record);
    },

    /** What is stored for one machine, without the bytes. */
    async info(system) {
      const record = await backend.get(String(system ?? '').trim()).catch(() => null);
      return record ? summarise(record) : null;
    },

    /** Everything stored, without the bytes. Sorted so a list does not reorder. */
    async list() {
      let keys = [];
      try {
        keys = (await backend.keys()) ?? [];
      } catch {
        return [];
      }
      const out = [];
      for (const key of keys) {
        const record = await backend.get(key).catch(() => null);
        if (record) out.push(summarise(record));
      }
      return out.sort((a, b) => a.system.localeCompare(b.system));
    },

    /** The names of every machine with firmware stored -- what the server wants. */
    async declared() {
      return (await store.list()).map((r) => r.system);
    },

    async remove(system) {
      await backend.remove(String(system ?? '').trim());
    },

    /**
     * A URL the emulator can load. The caller owns it and must revoke it: a blob
     * URL holds its bytes alive until it is revoked, and a player that swaps
     * games ten times would otherwise hold ten copies of a Kickstart ROM.
     */
    async objectURL(system, { createObjectURL = globalThis.URL?.createObjectURL, Blob: BlobImpl = globalThis.Blob } = {}) {
      if (!createObjectURL || !BlobImpl) return null;
      const record = await backend.get(String(system ?? '').trim()).catch(() => null);
      if (!record?.bytes) return null;
      return createObjectURL(new BlobImpl([record.bytes], { type: 'application/octet-stream' }));
    },
  };
  return store;
}

function summarise({ system, name, size, addedAt }) {
  return { system, name, size, addedAt };
}
