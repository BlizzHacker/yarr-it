import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createTorrentResolver, renderForKind } from './torrent.js';
import { RENDER } from '../source.js';

// A torrent carrying a ROM or a Flash file must reach the canvas renderer, not
// be classified as video and handed to a <video> element that can never play it.
test('flash and rom kinds render on the canvas', () => {
  assert.equal(renderForKind('flash'), RENDER.CANVAS);
  assert.equal(renderForKind('rom'), RENDER.CANVAS);
  assert.equal(renderForKind('video'), RENDER.VIDEO);
});

/** A torrent file stub whose blob() resolves once, like WebTorrent's. */
function fakeFile(name, { blobFails = false } = {}) {
  return {
    name,
    length: 1024,
    progress: 0.5,
    streamURL: '/webtorrent/x/' + name,
    blob: async () => {
      if (blobFails) throw new Error('swarm died');
      return { size: 1024 };
    },
  };
}

function fakeEngine(file) {
  const torrent = { id: 't1' };
  return {
    torrent,
    destroyed: 0,
    _serverReady: Promise.resolve(true),
    add(_uri, { onReady }) { onReady(file, torrent); },
    destroyTorrent() { this.destroyed += 1; },
  };
}

test('a rom inside a torrent resolves to a canvas playable, not a video', async () => {
  const engine = fakeEngine(fakeFile('Zelda.nes'));
  const r = createTorrentResolver({ engine, classify: () => 'rom' });
  const p = await r.resolve({ uri: 'magnet:?xt=urn:btih:abc' });

  assert.equal(p.render, RENDER.CANVAS);
  assert.equal(typeof p.mount, 'function');
  // A canvas playable is driven by mount(), so src is deliberately not a URL.
  assert.equal(p.src, '');
});

test('a flash file inside a torrent resolves to a canvas playable', async () => {
  const engine = fakeEngine(fakeFile('game.swf'));
  const r = createTorrentResolver({ engine, classify: () => 'flash' });
  const p = await r.resolve({ uri: 'magnet:?xt=urn:btih:abc' });

  assert.equal(p.render, RENDER.CANVAS);
  assert.equal(typeof p.mount, 'function');
});

test('an ordinary video torrent is untouched by the canvas branch', async () => {
  const engine = fakeEngine(fakeFile('Sintel.mp4'));
  const r = createTorrentResolver({ engine, classify: () => 'video' });
  const p = await r.resolve({ uri: 'magnet:?xt=urn:btih:abc' });

  assert.equal(p.render, RENDER.VIDEO);
  assert.equal(p.src, '/webtorrent/x/Sintel.mp4');
  assert.equal(p.mount, null);
});

test('cleanup on a canvas playable is scoped to its own torrent', async () => {
  const engine = fakeEngine(fakeFile('Zelda.nes'));
  const r = createTorrentResolver({ engine, classify: () => 'rom' });
  const p = await r.resolve({ uri: 'magnet:?xt=urn:btih:abc' });

  engine.torrent = { id: 'someone-elses' };
  p.cleanup();
  assert.equal(engine.destroyed, 0, 'must not tear down a torrent it does not own');
});
