import { test } from 'node:test';
import assert from 'node:assert/strict';
import { archiveResolver, archiveFileResolver, identifierFrom, viaRelay } from './archive.js';
import { isCollection, RENDER } from '../source.js';

test('recognises archive.org item URLs and nothing else', () => {
  assert.equal(identifierFrom('https://archive.org/details/dk_coleco'), 'dk_coleco');
  assert.equal(identifierFrom('https://archive.org/download/dk_coleco/dk.bin'), 'dk_coleco');
  assert.equal(identifierFrom('https://evil.com/archive.org/details/x'), null);
  assert.equal(identifierFrom('not a url'), null);
  assert.equal(archiveResolver.canHandle('https://example.com/a.mp4'), false);
});

// archive.org sends no Access-Control-Allow-Origin, and an emulator fetches the
// ROM bytes itself, so that fetch would be blocked without the relay.
test('rom URLs are routed through the relay that adds CORS', () => {
  const out = viaRelay('https://archive.org/download/dk_coleco/dk.bin');
  assert.ok(out.startsWith('/bridge/iptv?u='));
  assert.ok(out.includes(encodeURIComponent('https://archive.org/download/dk_coleco/dk.bin')));
});

function metaFetch(files, title = 'Donkey Kong', emulator = 'coleco') {
  return async () => ({ ok: true, json: async () => ({ metadata: { title, emulator }, files }) });
}

// The real item: dk_coleco.zip is a 404, the ROM is dk.bin, and the rest is
// screenshots, XML and a .torrent.
test('picks the rom out of a real archive.org file listing', async () => {
  const out = await archiveResolver.resolve(
    { uri: 'https://archive.org/details/dk_coleco' },
    { fetchImpl: metaFetch([
      { name: '00_coverscreenshot.png', size: 40000 },
      { name: '__ia_thumb.jpg', size: 9000 },
      { name: 'dk.bin', size: 16384 },
      { name: 'dk_coleco_archive.torrent', size: 2000 },
      { name: 'dk_coleco_meta.xml', size: 3000 },
    ]) },
  );
  assert.equal(isCollection(out), false);
  assert.equal(out.render, RENDER.CANVAS);
  assert.ok(out.src.startsWith('/bridge/iptv?u='), 'must be proxied for CORS');
  assert.ok(out.src.includes('dk.bin'));
});

test('an item with several roms asks rather than guessing', async () => {
  const out = await archiveResolver.resolve(
    { uri: 'https://archive.org/details/romset' },
    { fetchImpl: metaFetch([
      { name: 'meta.xml', size: 100 },
      { name: 'Zelda (USA).nes', size: 131072 },
      { name: 'Mario (USA).nes', size: 40960 },
    ]) },
  );
  assert.equal(isCollection(out), true);
  assert.equal(out.sources.length, 2);
});

test('a video item plays direct, costing the relay nothing', async () => {
  const out = await archiveResolver.resolve(
    { uri: 'https://archive.org/details/film' },
    { fetchImpl: metaFetch([{ name: 'movie.mp4', size: 900000000 }]) },
  );
  assert.equal(out.render, RENDER.VIDEO);
  assert.ok(out.src.startsWith('https://archive.org/download/'),
    'media elements ignore CORS, so no proxy needed');
});

test('an item with nothing playable reports rather than guessing', async () => {
  await assert.rejects(
    () => archiveResolver.resolve(
      { uri: 'https://archive.org/details/x' },
      { fetchImpl: metaFetch([{ name: 'meta.xml', size: 10 }]) },
    ),
    /nothing playable/,
  );
});

test('a dead item is a typed failure, not a crash', async () => {
  await assert.rejects(
    () => archiveResolver.resolve(
      { uri: 'https://archive.org/details/gone' },
      { fetchImpl: async () => { throw new Error('network'); } },
    ),
    /did not respond/,
  );
});

test('the file resolver claims a file URL but not a bare item', () => {
  assert.equal(archiveFileResolver.canHandle('https://archive.org/download/x/rom.nes'), true);
  assert.equal(archiveFileResolver.canHandle('https://archive.org/details/x'), false);
});

// dk.bin is a COLECOVISION rom. `.bin` is also Atari 2600, Mega Drive and
// several others, so guessing from the extension picks the wrong machine --
// which boots EmulatorJS successfully and then runs nothing.
test('the core comes from archive.org metadata, not the file extension', async () => {
  const { coreFromMetadata } = await import('./archive.js');
  assert.equal(coreFromMetadata({ metadata: { emulator: 'coleco' } }), 'coleco');
  assert.equal(coreFromMetadata({ metadata: { emulator: 'Genesis' } }), 'segaMD');
  assert.equal(coreFromMetadata({ metadata: {} }), null);
});

test('a colecovision item is given the coleco core, not nes', async () => {
  const out = await archiveResolver.resolve(
    { uri: 'https://archive.org/details/dk_coleco' },
    { fetchImpl: metaFetch([{ name: 'dk.bin', size: 16384 }, { name: 'meta.xml', size: 10 }]) },
  );
  // The proxied URL is the ROM, and the playable is a canvas render whose
  // mount will boot the declared core.
  assert.equal(out.render, RENDER.CANVAS);
  assert.ok(out.src.includes('dk.bin'));
  assert.ok(out.src.startsWith('/bridge/iptv?u='));
});

test('an unsupported system is refused rather than defaulted to nes', async () => {
  await assert.rejects(
    () => archiveResolver.resolve(
      { uri: 'https://archive.org/details/weird' },
      { fetchImpl: async () => ({
        ok: true,
        json: async () => ({
          metadata: { title: 'Odd', emulator: 'some_unsupported_machine' },
          files: [{ name: 'game.rom99', size: 1024 }, { name: 'x.bin', size: 2048 }],
        }),
      }) },
    ),
    /no emulator core/,
  );
});
