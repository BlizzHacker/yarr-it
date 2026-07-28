import { test } from 'node:test';
import assert from 'node:assert/strict';
import { kindOf, playableFiles, bestFile, needsChoice } from './pickfile.js';

const f = (name, length = 1024) => ({ name, length });

test('classifies by extension, and knows what is never wanted', () => {
  assert.equal(kindOf('Sintel.mp4'), 'video');
  assert.equal(kindOf('track.flac'), 'audio');
  assert.equal(kindOf('Zelda.smc'), 'rom');
  assert.equal(kindOf('game.SWF'), 'flash');
  assert.equal(kindOf('cover.png'), 'image');
  assert.equal(kindOf('roms.zip'), 'archive');
  for (const junk of ['readme.nfo', 'file_id.diz', 'item_meta.xml', 'a.torrent', 'setup.exe']) {
    assert.equal(kindOf(junk), 'junk', junk);
  }
});

// The old logic took the single largest file. In a 200-ROM collection the
// biggest file is not "the game" -- it is whichever game happened to be
// biggest, which is nonsense as a default.
test('a rom collection offers every rom, not just the biggest', () => {
  const files = [
    f('readme.nfo', 900),
    f('Chrono Trigger (USA).smc', 4_000_000),
    f('Zelda (USA).smc', 1_000_000),
    f('Mario (Japan).smc', 512_000),
  ];
  const out = playableFiles(files);
  assert.equal(out.length, 3, 'all three roms should be offered');
  assert.ok(out.every((x) => x.kind === 'rom'));
  assert.equal(needsChoice(files), true);
});

test('roms are ordered by region, not by size', () => {
  const out = playableFiles([
    f('Game (Japan).smc', 9_000_000),
    f('Game (USA).smc', 1_000),
    f('Game (Europe).smc', 5_000_000),
  ]);
  assert.match(out[0].name, /USA/);
  assert.match(out[1].name, /Europe/);
  assert.match(out[2].name, /Japan/);
});

test('video still picks the biggest encode', () => {
  const files = [f('sample.mp4', 5_000_000), f('Movie.1080p.mkv', 8_000_000_000)];
  assert.equal(bestFile(files).name, 'Movie.1080p.mkv');
  assert.equal(needsChoice(files), false, 'two encodes of one film is not a real choice');
});

// A film torrent that happens to ship a Flash extra should still play the film.
test('real media outranks a rom in a mixed torrent', () => {
  const out = bestFile([f('extra.swf', 200_000), f('Movie.mkv', 3_000_000_000)]);
  assert.equal(out.name, 'Movie.mkv');
});

test('an archive.org item picks the rom out of the metadata clutter', () => {
  // Exactly what archive.org/metadata/dk_coleco returns.
  const files = [
    f('00_coverscreenshot.png', 40_000),
    f('__ia_thumb.jpg', 9_000),
    f('dk.bin', 16_384),
    f('dk_coleco_archive.torrent', 2_000),
    f('dk_coleco_files.xml', 3_000),
    f('dk_coleco_meta.sqlite', 20_000),
  ];
  const best = bestFile(files);
  assert.equal(best.name, 'dk.bin', 'the ROM, not the screenshot or the torrent');
  assert.equal(best.kind, 'rom');
});

test('a zip is only offered when nothing is directly playable', () => {
  assert.equal(bestFile([f('roms.zip', 900_000)]).name, 'roms.zip');
  // With a real ROM present the zip must not win.
  const out = bestFile([f('roms.zip', 900_000_000), f('Game (USA).nes', 40_000)]);
  assert.equal(out.name, 'Game (USA).nes');
});

test('a set of only junk yields nothing rather than a guess', () => {
  assert.equal(bestFile([f('readme.nfo'), f('meta.xml')]), null);
  assert.deepEqual(playableFiles([]), []);
  assert.equal(needsChoice([]), false);
});

test('handles missing lengths and odd names without throwing', () => {
  assert.doesNotThrow(() => playableFiles([{ name: 'Game.smc' }, { name: null }, {}]));
  assert.equal(bestFile([{ name: 'Game.smc' }]).name, 'Game.smc');
});

// Nearly every archive.org item ships cover art and a thumbnail. Treating those
// as playable turns a one-ROM item into a three-way question about whether you
// would rather look at the box.
test('cover art is not a play option when a rom is present', () => {
  const out = playableFiles([
    f('00_coverscreenshot.png', 40_000),
    f('__ia_thumb.jpg', 9_000),
    f('dk.bin', 16_384),
  ]);
  assert.equal(out.length, 1);
  assert.equal(out[0].name, 'dk.bin');
});

test('an image-only item still plays its image', () => {
  const out = playableFiles([f('scan.jpg', 900_000), f('meta.xml', 100)]);
  assert.equal(out.length, 1);
  assert.equal(out[0].kind, 'image');
});
