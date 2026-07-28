import { test } from 'node:test';
import assert from 'node:assert/strict';
import { flashResolver, isSwf } from './flash.js';
import { gameResolver, coreFor, isRom, bootEmulator } from './game.js';
import { RENDER } from '../source.js';

// --- claiming -------------------------------------------------------------

test('flash claims .swf on the path only, never via the query string', () => {
  assert.equal(flashResolver.canHandle('https://x/game.swf'), true);
  assert.equal(flashResolver.canHandle('https://x/game.SWF?v=2'), true);
  assert.equal(flashResolver.canHandle('https://x/movie.mp4?next=game.swf'), false);
  assert.equal(flashResolver.canHandle('https://x/a.mp4'), false);
});

test('canHandle never throws on junk input', () => {
  for (const bad of [null, 42, '', 'not a url', undefined]) {
    assert.doesNotThrow(() => flashResolver.canHandle(bad));
    assert.doesNotThrow(() => gameResolver.canHandle(bad));
    assert.equal(flashResolver.canHandle(bad), false);
    assert.equal(gameResolver.canHandle(bad), false);
  }
});

test('rom extensions map to the right emulator core', () => {
  assert.equal(coreFor('Zelda.nes'), 'nes');
  assert.equal(coreFor('Mario.SMC'), 'snes');
  assert.equal(coreFor('pokemon.gbc'), 'gb');
  assert.equal(coreFor('mario64.z64'), 'n64');
  assert.equal(coreFor('sonic.md'), 'segaMD');
  assert.equal(coreFor('notagame.mp4'), null);
});

test('game claims rom paths but not ordinary media', () => {
  assert.equal(gameResolver.canHandle('https://x/roms/Zelda.nes'), true);
  assert.equal(gameResolver.canHandle('https://x/a.mp4'), false);
  assert.equal(isRom('https://x/Mario.smc'), true);
});

// A ROM URL is also an http(s) URL, so registration order decides who wins.
// These resolvers must sit ahead of the catch-all url resolver.
test('a rom url is claimed by game, and a swf by flash, not by chance', () => {
  assert.equal(gameResolver.canHandle('https://x/Zelda.nes'), true);
  assert.equal(flashResolver.canHandle('https://x/Zelda.nes'), false);
  assert.equal(gameResolver.canHandle('https://x/game.swf'), false);
});

// --- resolving ------------------------------------------------------------

test('flash resolves to a canvas playable carrying a mount function', async () => {
  const p = await flashResolver.resolve({ uri: 'https://x/game.swf' });
  assert.equal(p.render, RENDER.CANVAS);
  assert.equal(typeof p.mount, 'function');
  assert.equal(p.mime, 'application/x-shockwave-flash');
});

test('game resolves to a canvas playable carrying a mount function', async () => {
  const p = await gameResolver.resolve({ uri: 'https://x/roms/Zelda.nes' });
  assert.equal(p.render, RENDER.CANVAS);
  assert.equal(typeof p.mount, 'function');
});

test('a canvas playable without a mount is rejected at construction', async () => {
  const { makePlayable } = await import('../source.js');
  assert.throws(
    () => makePlayable({ render: RENDER.CANVAS, src: 'x', mime: '' }),
    /mount/,
  );
});

// --- emulator boot --------------------------------------------------------

test('bootEmulator sets every global the loader reads BEFORE injecting it', () => {
  const appended = [];
  const el = {};  // no id: bootEmulator must assign one
  const fakeDoc = {
    createElement: () => ({ set src(v) { this._src = v; }, get src() { return this._src; } }),
    body: {
      append(tag) {
        // The loader reads these at execute time; if they are not already set
        // when the tag lands, it boots with no game.
        appended.push({
          src: tag.src,
          core: globalThis.EJS_core,
          gameUrl: globalThis.EJS_gameUrl,
          player: globalThis.EJS_player,
          playerIsSelector: typeof globalThis.EJS_player === 'string',
        });
      },
    },
  };
  bootEmulator(el, { gameUrl: 'https://x/Z.nes', core: 'nes', name: 'Z', doc: fakeDoc });

  assert.equal(appended.length, 1);
  assert.match(appended[0].src, /loader\.js$/);
  assert.equal(appended[0].core, 'nes');
  assert.equal(appended[0].gameUrl, 'https://x/Z.nes');
  // EJS_player must be a CSS selector string; an element makes the loader
  // run and then never construct the emulator, with no error raised.
  assert.equal(appended[0].playerIsSelector, true);
  assert.equal(appended[0].player, `#${el.id}`);
  assert.ok(el.id, 'bootEmulator must give the host an id to select');
});
