import { test } from 'node:test';
import assert from 'node:assert/strict';
import { groupByCategory } from './library.js';
import { makeCollection, makeSource } from './source.js';

const src = (title, group) =>
  makeSource({ kind: 'url', uri: `http://s/${title}`, meta: { title, group } });

test('sources are grouped by their playlist group', () => {
  const c = makeCollection({
    title: 'IPTV',
    sources: [src('BBC', 'UK'), src('ITV', 'UK'), src('CNN', 'US')],
  });
  const groups = groupByCategory(c);
  assert.deepEqual([...groups.keys()], ['UK', 'US']);
  assert.equal(groups.get('UK').length, 2);
});

test('ungrouped channels collect under Ungrouped rather than an empty label', () => {
  const c = makeCollection({ title: 'IPTV', sources: [src('X', '')] });
  const groups = groupByCategory(c);
  assert.deepEqual([...groups.keys()], ['Ungrouped']);
});
