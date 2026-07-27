import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createMemoryStore } from './store.js';

test('put then get round-trips a record', async () => {
  const s = createMemoryStore();
  await s.put({ id: 'a', title: 'UK channels', sources: [] });
  assert.equal((await s.get('a')).title, 'UK channels');
});

test('put with the same id replaces rather than duplicating', async () => {
  const s = createMemoryStore();
  await s.put({ id: 'a', title: 'one' });
  await s.put({ id: 'a', title: 'two' });
  const all = await s.list();
  assert.equal(all.length, 1);
  assert.equal(all[0].title, 'two');
});

test('remove deletes and get returns null for a missing id', async () => {
  const s = createMemoryStore();
  await s.put({ id: 'a', title: 'one' });
  await s.remove('a');
  assert.equal(await s.get('a'), null);
  assert.deepEqual(await s.list(), []);
});

test('put rejects a record with no id', async () => {
  const s = createMemoryStore();
  await assert.rejects(() => s.put({ title: 'no id' }), /id/);
});

test('mutating the record after put does not change what get returns', async () => {
  const s = createMemoryStore();
  const rec = { id: 'a', title: 'one' };
  await s.put(rec);
  rec.title = 'mutated';
  assert.equal((await s.get('a')).title, 'one');
});

test('mutating the object returned by get does not change a later get', async () => {
  const s = createMemoryStore();
  await s.put({ id: 'a', title: 'one' });
  const got = await s.get('a');
  got.title = 'mutated';
  assert.equal((await s.get('a')).title, 'one');
});

test('mutating an object returned inside list() does not change the store', async () => {
  const s = createMemoryStore();
  await s.put({ id: 'a', title: 'one' });
  const all = await s.list();
  all[0].title = 'mutated';
  assert.equal((await s.get('a')).title, 'one');
  assert.equal((await s.list())[0].title, 'one');
});

test('a record with a nested array round-trips with contents intact', async () => {
  const s = createMemoryStore();
  await s.put({ id: 'a', sources: [{ uri: 'x' }] });
  const got = await s.get('a');
  assert.deepEqual(got.sources, [{ uri: 'x' }]);
});
