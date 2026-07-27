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

test('an unknown element for a valid render kind throws rather than silently doing nothing', () => {
  const els = fakeElements();
  delete els.embed;
  assert.throws(
    () => renderPlayable(makePlayable({ render: RENDER.EMBED, src: 'x', mime: 'text/html' }), els),
    /no element/,
  );
});
