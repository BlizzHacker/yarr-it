import { test } from 'node:test';
import assert from 'node:assert/strict';
import { groupByCategory, renderLibrary } from './library.js';
import { makeCollection, makeSource, makePlayable, RENDER } from './source.js';

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

test('groupByCategory rejects a Playable with a clear error instead of throwing mid-loop', () => {
  const playable = makePlayable({ render: RENDER.VIDEO, src: 'http://a/b', mime: 'video/mp4' });
  assert.throws(() => groupByCategory(playable), /expects a Collection/);
});

test('groupByCategory rejects undefined rather than a confusing TypeError', () => {
  assert.throws(() => groupByCategory(undefined), /expects a Collection/);
});

test('a source with no meta key at all groups under Ungrouped', () => {
  const c = makeCollection({
    title: 'IPTV',
    sources: [{ kind: 'url', uri: 'http://s/no-meta' }],
  });
  const groups = groupByCategory(c);
  assert.deepEqual([...groups.keys()], ['Ungrouped']);
});

// --- renderLibrary: delegated click listener -------------------------------
//
// No jsdom (project constraint). These are plain-object fakes covering only
// the DOM surface renderLibrary actually touches: element creation,
// className/textContent/dataset, append, and addEventListener/
// removeEventListener/closest on the mount and its children. `document`
// itself is a global in real code, so it is faked for the duration of the
// test and restored afterward.

function fakeChildElement(tag) {
  return {
    tagName: tag,
    className: '',
    textContent: '',
    type: '',
    dataset: {},
    children: [],
    append(...nodes) { this.children.push(...nodes); },
    closest(selector) {
      return selector === `.${this.className}` ? this : null;
    },
  };
}

function fakeMount() {
  const listeners = {};
  const el = {
    tagName: 'DIV',
    hidden: true,
    children: [],
    append(...nodes) { this.children.push(...nodes); },
    addEventListener(type, fn) { (listeners[type] ??= []).push(fn); },
    removeEventListener(type, fn) {
      listeners[type] = (listeners[type] || []).filter((f) => f !== fn);
    },
    dispatch(type, event) {
      for (const fn of listeners[type] || []) fn(event);
    },
    listenerCount(type) { return (listeners[type] || []).length; },
  };
  // Mirrors the real DOM behaviour renderLibrary relies on: assigning
  // textContent wipes existing children.
  Object.defineProperty(el, 'textContent', {
    set() { this.children = []; },
    get() { return ''; },
  });
  return el;
}

test('re-rendering the same mount twice then clicking once calls onPick exactly once', () => {
  const originalDocument = globalThis.document;
  globalThis.document = { createElement: (tag) => fakeChildElement(tag) };
  try {
    const mount = fakeMount();
    const picks = [];
    const onPick = (source) => picks.push(source);
    const collection = makeCollection({ title: 'IPTV', sources: [src('BBC', 'UK')] });

    renderLibrary(collection, { mount, onPick });
    renderLibrary(collection, { mount, onPick });

    assert.equal(
      mount.listenerCount('click'), 1,
      'renderLibrary must reuse the delegated listener across re-renders, not stack a new one',
    );

    const row = mount.children.find((child) => child.className === 'lib-row');
    const btn = row.children[0];
    mount.dispatch('click', { target: btn });

    assert.equal(picks.length, 1, 'onPick should fire exactly once per click, even after two renders');
    assert.equal(picks[0], collection.sources[0]);
  } finally {
    globalThis.document = originalDocument;
  }
});
