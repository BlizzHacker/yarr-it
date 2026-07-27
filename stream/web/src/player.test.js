import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderPlayable, detachAll } from './player.js';
import { makePlayable, RENDER } from './source.js';

function fakeElements() {
  const make = () => ({
    src: '',
    hidden: true,
    pauseCalls: 0,
    loadCalls: 0,
    removeAttribute(k) { this[k] = ''; },
    pause() { this.pauseCalls += 1; },
    load() { this.loadCalls += 1; },
  });
  return { video: make(), audio: make(), image: make(), embed: make(), canvas: make() };
}

test('each render kind attaches its own element and hides the others', () => {
  for (const kind of [RENDER.VIDEO, RENDER.AUDIO, RENDER.IMAGE, RENDER.EMBED]) {
    const els = fakeElements();
    const el = renderPlayable(
      makePlayable({ render: kind, src: 'http://a/b', mime: 'video/mp4' }), els,
    );
    assert.equal(el, els[kind], `${kind} attached the wrong element`);
    assert.equal(el.hidden, false);
    assert.equal(el.src, 'http://a/b');
    for (const [name, other] of Object.entries(els)) {
      if (name !== kind) assert.equal(other.hidden, true, `${name} should stay hidden`);
    }
  }
});

test('detachAll clears every source so nothing keeps streaming', () => {
  const els = fakeElements();
  renderPlayable(makePlayable({ render: RENDER.VIDEO, src: 'http://a/b', mime: 'v' }), els);
  detachAll(els);
  assert.equal(els.video.hidden, true);
  assert.equal(els.video.src, '');
});

test('switching from one render kind to another releases the outgoing element', () => {
  const els = fakeElements();

  renderPlayable(makePlayable({ render: RENDER.VIDEO, src: 'http://a/video', mime: 'video/mp4' }), els);
  renderPlayable(makePlayable({ render: RENDER.AUDIO, src: 'http://a/audio', mime: 'audio/mp3' }), els);

  assert.equal(els.video.hidden, true);
  assert.equal(els.video.src, '');
  assert.ok(els.video.pauseCalls >= 1, 'pause() should have been called on the outgoing video element');
  assert.ok(els.video.loadCalls >= 1, 'load() should have been called on the outgoing video element');

  assert.equal(els.audio.hidden, false);
  assert.equal(els.audio.src, 'http://a/audio');
});

// The current fakeElements() gives every element both pause() and load(),
// which is exactly why the iframe bug (finding I1) was invisible to the
// existing suite: a real <iframe> embed (YouTube/Vimeo) has neither. This
// fake deliberately has neither, and tracks every value assigned to `src` so
// the test can prove about:blank was actually set, not just that the final
// (post-removeAttribute) value happens to be empty.
function fakeIframeElement() {
  const el = {
    hidden: false,
    srcHistory: [],
    removeAttribute(k) { this[k] = ''; },
  };
  Object.defineProperty(el, 'src', {
    get() { return el._src; },
    set(v) { el._src = v; el.srcHistory.push(v); },
  });
  return el;
}

test('detachAll points an element with no pause() at about:blank before clearing its src, so an iframe embed actually stops', () => {
  const iframe = fakeIframeElement();
  const els = { video: null, audio: null, image: null, embed: iframe, canvas: null };

  detachAll(els);

  assert.equal(iframe.hidden, true);
  assert.equal(iframe.srcHistory[0], 'about:blank', 'src should have been set to about:blank first');
  assert.equal(iframe.src, '', 'removeAttribute should still clear it afterward');
});

test('an unknown element for a valid render kind throws rather than silently doing nothing', () => {
  const els = fakeElements();
  delete els.embed;
  assert.throws(
    () => renderPlayable(makePlayable({ render: RENDER.EMBED, src: 'x', mime: 'text/html' }), els),
    /no element/,
  );
});
