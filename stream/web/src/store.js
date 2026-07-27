/**
 * SourceStore: saved playlists and sources.
 *
 * Two implementations behind one interface. LocalSourceStore ships now;
 * SyncedSourceStore will implement the same four methods in the sync slice, so
 * nothing outside this file needs to know which is in use.
 */

const STORE = 'sources';

export function createMemoryStore() {
  const rows = new Map();
  // structuredClone on every write and every read so this fallback matches
  // IndexedDB's structured-clone semantics (caller mutations after put(), or
  // mutations of a returned record, must never reach back into storage).
  // Do not "optimise" this away by storing/returning live references.
  return {
    async list() { return [...rows.values()].map((r) => structuredClone(r)); },
    async get(id) {
      const r = rows.get(id);
      return r ? structuredClone(r) : null;
    },
    async put(record) {
      if (!record?.id) throw new Error('record needs an id');
      rows.set(record.id, structuredClone(record));
      return structuredClone(record);
    },
    async remove(id) { rows.delete(id); },
  };
}

export function createLocalStore(idbFactory = globalThis.indexedDB) {
  // Private browsing and some embedded webviews expose no IndexedDB at all.
  // Falling back keeps the app usable rather than throwing on first paint.
  if (!idbFactory) return createMemoryStore();

  const ready = new Promise((resolve, reject) => {
    const req = idbFactory.open('yarrit', 1);
    req.onupgradeneeded = () => {
      req.result.createObjectStore(STORE, { keyPath: 'id' });
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
    // If another tab holds the connection open during a version upgrade,
    // neither onsuccess nor onerror fires and this promise would otherwise
    // hang forever, along with every list/get/put/remove call awaiting it.
    req.onblocked = () => reject(new Error('IndexedDB open blocked by another open connection (e.g. another tab); close other tabs and retry'));
  });

  const run = async (mode, fn) => {
    const db = await ready;
    return new Promise((resolve, reject) => {
      const tx = db.transaction(STORE, mode);
      const req = fn(tx.objectStore(STORE));
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  };

  return {
    async list() { return await run('readonly', (s) => s.getAll()); },
    async get(id) { return (await run('readonly', (s) => s.get(id))) ?? null; },
    async put(record) {
      if (!record?.id) throw new Error('record needs an id');
      await run('readwrite', (s) => s.put(record));
      return record;
    },
    async remove(id) { await run('readwrite', (s) => s.delete(id)); },
  };
}
