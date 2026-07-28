import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  archiveResolver, identifierFrom, fileFrom, embedUrl, itemInfo, ejsCoreFor, MAX_RELAY_ROM,
} from './archive.js';
import { RENDER } from '../source.js';

test('recognises archive.org item URLs and nothing else', () => {
  assert.equal(identifierFrom('https://archive.org/details/dk_coleco'), 'dk_coleco');
  assert.equal(identifierFrom('https://archive.org/download/dk_coleco/dk.bin'), 'dk_coleco');
  assert.equal(identifierFrom('https://archive.org/embed/dk_coleco'), 'dk_coleco');
  assert.equal(identifierFrom('https://evil.com/archive.org/details/x'), null);
  assert.equal(identifierFrom('not a url'), null);
  assert.equal(archiveResolver.canHandle('https://example.com/a.mp4'), false);
});

test('reads the file out of a URL that names one', () => {
  assert.equal(fileFrom('https://archive.org/download/x/rom.nes'), 'rom.nes');
  assert.equal(fileFrom('https://archive.org/download/x/sub/dir/rom.nes'), 'sub/dir/rom.nes');
  assert.equal(fileFrom('https://archive.org/details/x'), null);
});

// The whole point of the rewrite: archive.org runs the emulator, so we do not
// proxy ROM bytes and we do not have to know which machine the game is for.
test('an emulated item hands off to archive.org, costing us no bandwidth', async () => {
  const out = await archiveResolver.resolve({ uri: 'https://archive.org/details/dk_coleco' });
  assert.equal(out.render, RENDER.EMBED);
  assert.equal(out.src, 'https://archive.org/embed/dk_coleco');
  assert.ok(!out.src.includes('/bridge/'), 'no byte should cross our relay');
});

// dk.bin is a COLECOVISION rom. `.bin` is equally Atari 2600 and Mega Drive, so
// guessing from the extension picks the wrong machine -- which boots an
// emulator successfully and then runs nothing. Their player already knows.
test('no emulator core is ever guessed from a file extension', async () => {
  const out = await archiveResolver.resolve(
    { uri: 'https://archive.org/download/dk_coleco/dk.bin' },
  );
  assert.equal(out.render, RENDER.EMBED);
  assert.equal(out.src, 'https://archive.org/embed/dk_coleco?start=dk.bin');
});

test('a named rom in a multi-rom item is started specifically', () => {
  assert.equal(embedUrl('romset', 'Zelda (USA).nes'),
    'https://archive.org/embed/romset?start=Zelda%20(USA).nes');
  assert.equal(embedUrl('romset'), 'https://archive.org/embed/romset');
});

// An iframe cannot be seeked or fullscreened the way a video element can, and
// media elements ignore CORS, so plain media is worth playing ourselves.
test('a direct video link plays natively rather than in their iframe', async () => {
  const out = await archiveResolver.resolve(
    { uri: 'https://archive.org/download/film/movie.mp4' },
  );
  assert.equal(out.render, RENDER.VIDEO);
  assert.equal(out.src, 'https://archive.org/download/film/movie.mp4');
});

test('direct audio and image links get their own render modes', async () => {
  const audio = await archiveResolver.resolve(
    { uri: 'https://archive.org/download/lp/track.flac' },
  );
  assert.equal(audio.render, RENDER.AUDIO);
  const image = await archiveResolver.resolve(
    { uri: 'https://archive.org/download/scan/page.jpg' },
  );
  assert.equal(image.render, RENDER.IMAGE);
});

// Playback never fetches metadata now, so an item page resolves without any
// network call at all -- a resolver that needed fetch would throw here.
test('playback resolves with no network call', async () => {
  const out = await archiveResolver.resolve({ uri: 'https://archive.org/details/anything' });
  assert.equal(out.render, RENDER.EMBED);
});

test('metadata is still available for search results that want a file list', async () => {
  const info = await itemInfo('dk_coleco', {
    fetchImpl: async () => ({
      ok: true,
      json: async () => ({
        metadata: { title: 'Donkey Kong', emulator: 'coleco' },
        files: [{ name: 'dk.bin', size: '16384' }],
      }),
    }),
  });
  assert.equal(info.title, 'Donkey Kong');
  assert.equal(info.emulator, 'coleco');
  assert.deepEqual(info.files, [{ name: 'dk.bin', length: 16384 }]);
});

test('a dead metadata lookup reports the identifier it failed on', async () => {
  await assert.rejects(
    () => itemInfo('gone', { fetchImpl: async () => ({ ok: false, status: 404 }) }),
    /gone.*404/,
  );
});

// --- playing here vs playing at archive.org --------------------------------

function metaFetch({ emulator = 'nes', files = [{ name: 'game.nes', size: 40960 }] } = {}) {
  return async () => ({
    ok: true,
    json: async () => ({ metadata: { title: 'A Game', emulator }, files }),
  });
}

// The archive's own player has no on-screen controls, so on a phone a console
// game there is something you can watch and not play. `#ejs` asks for ours.
test('#ejs plays with our own emulator instead of their iframe', async () => {
  const out = await archiveResolver.resolve(
    { uri: 'https://archive.org/details/some_game#ejs' },
    { fetchImpl: metaFetch() },
  );
  assert.equal(out.render, RENDER.CANVAS);
  assert.ok(out.src.startsWith('/bridge/iptv?u='), 'archive.org sends no CORS header');
  assert.ok(decodeURIComponent(out.src.split('u=')[1]).endsWith('game.nes'));
});

test('without the fragment it is still their player, costing nothing', async () => {
  const out = await archiveResolver.resolve({ uri: 'https://archive.org/details/some_game' });
  assert.equal(out.render, RENDER.EMBED);
  assert.ok(!out.src.includes('/bridge/'));
});

// The core is translated from what archive.org declares, never guessed from a
// file extension -- `.bin` alone would pick the wrong machine.
test('the core comes from the declared emulator, not the file name', () => {
  assert.equal(ejsCoreFor('coleco'), 'coleco');
  assert.equal(ejsCoreFor('megadriv'), 'segaMD');
  assert.equal(ejsCoreFor('gbcolor'), 'gb');
  assert.equal(ejsCoreFor('some_unsupported_machine'), null);
  assert.equal(ejsCoreFor(''), null);
});

test('a system with no core here says so instead of booting the wrong one', async () => {
  await assert.rejects(
    () => archiveResolver.resolve(
      { uri: 'https://archive.org/details/x#ejs' },
      { fetchImpl: metaFetch({ emulator: 'apple2gs' }) },
    ),
    /no core for/,
  );
});

// Every relayed byte is paid for out of an allowance shared with a mail server.
test('a rom too large to relay is refused with the other option named', async () => {
  await assert.rejects(
    () => archiveResolver.resolve(
      { uri: 'https://archive.org/details/x#ejs' },
      { fetchImpl: metaFetch({ files: [{ name: 'disc.bin', size: MAX_RELAY_ROM + 1 }] }) },
    ),
    /too large to play here.*archive\.org/s,
  );
});

test('a named file in a multi-rom item is the one that gets played', async () => {
  const out = await archiveResolver.resolve(
    { uri: 'https://archive.org/download/pack/Zelda (USA).nes#ejs' },
    { fetchImpl: metaFetch({ files: [
      { name: 'Mario (USA).nes', size: 40960 },
      { name: 'Zelda (USA).nes', size: 131072 },
    ] }) },
  );
  // The relay's u= parameter is itself encoded, so read it back out rather
  // than matching against the raw string.
  const target = decodeURIComponent(out.src.split('u=')[1]);
  assert.ok(target.endsWith(encodeURIComponent('Zelda (USA).nes')), target);
});
