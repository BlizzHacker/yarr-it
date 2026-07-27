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
  return {
    async list() { return [...rows.values()]; },
    async get(id) { return rows.get(id) ?? null; },
    async put(record) {
      if (!record?.id) throw new Error('record needs an id');
      rows.set(record.id, record);
      return record;
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
    async list() { return (await run('readonly', (s) => s.getAll())) ?? []; },
    async get(id) { return (await run('readonly', (s) => s.get(id))) ?? null; },
    async put(record) {
      if (!record?.id) throw new Error('record needs an id');
      await run('readwrite', (s) => s.put(record));
      return record;
    },
    async remove(id) { await run('readwrite', (s) => s.delete(id)); },
  };
}
