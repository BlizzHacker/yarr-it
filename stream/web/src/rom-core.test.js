import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  detectCore, coreFromHeader, coreFromExtension, coreFromContext, coreFromSize,
  isRomName, extensionOf, AMBIGUOUS,
} from './rom-core.js';

/** A buffer with bytes written at given offsets, for building fake ROMs. */
function rom(size, patches = {}) {
  const b = new Uint8Array(size);
  for (const [offset, value] of Object.entries(patches)) {
    const at = Number(offset);
    const data = typeof value === 'string'
      ? [...value].map((c) => c.charCodeAt(0))
      : value;
    b.set(data, at);
  }
  return b;
}

test('unambiguous extensions are read directly', () => {
  assert.equal(coreFromExtension('Zelda.nes'), 'nes');
  assert.equal(coreFromExtension('Chrono Trigger.SFC'), 'snes');
  assert.equal(coreFromExtension('Pokemon.gbc'), 'gb');
  assert.equal(coreFromExtension('Mario 64.z64'), 'n64');
  assert.equal(extensionOf('a/b/Game (USA).v64'), 'v64');
  assert.equal(coreFromExtension('readme.txt'), null);
});

// The whole reason this module exists.
test('.bin is not treated as any particular system', () => {
  assert.ok(AMBIGUOUS.has('bin'));
  assert.equal(coreFromExtension('dk.bin'), null);
  assert.equal(coreFromExtension('game.rom'), null);
  assert.equal(isRomName('dk.bin'), true, 'still a rom, just an unknown one');
});

// --- headers --------------------------------------------------------------

test('identifies an NES rom by its magic number', () => {
  assert.equal(coreFromHeader(rom(1024, { 0: 'NES\x1a' })), 'nes');
  assert.equal(coreFromHeader(rom(1024, { 0: 'FDS\x1a' })), 'nes');
});

test('identifies each of the three Nintendo 64 byte orders', () => {
  assert.equal(coreFromHeader(rom(1024, { 0: [0x80, 0x37, 0x12, 0x40] })), 'n64');
  assert.equal(coreFromHeader(rom(1024, { 0: [0x37, 0x80, 0x40, 0x12] })), 'n64');
  assert.equal(coreFromHeader(rom(1024, { 0: [0x40, 0x12, 0x37, 0x80] })), 'n64');
});

test('Sega cartridges name themselves at 0x100', () => {
  assert.equal(coreFromHeader(rom(0x400, { 0x100: 'SEGA MEGA DRIVE ' })), 'segaMD');
  assert.equal(coreFromHeader(rom(0x400, { 0x100: 'SEGA GENESIS    ' })), 'segaMD');
  assert.equal(coreFromHeader(rom(0x400, { 0x100: 'SEGA 32X        ' })), 'sega32x');
});

// Both write "TMR SEGA"; only the region nibble tells them apart.
test('Master System and Game Gear are separated by their region nibble', () => {
  const sms = rom(0x2100, { 0x1ff0: 'TMR SEGA', 0x1fff: [0x40] });
  const gg = rom(0x2100, { 0x1ff0: 'TMR SEGA', 0x1fff: [0x60] });
  assert.equal(coreFromHeader(sms), 'segaMS');
  assert.equal(coreFromHeader(gg), 'segaGG');
});

test('handhelds are identified by the boot logo the hardware checks', () => {
  const gb = rom(0x400, { 0x104: [0xce, 0xed, 0x66, 0x66, 0xcc, 0x0d, 0x00, 0x0b] });
  assert.equal(coreFromHeader(gb), 'gb');
  const gba = rom(0x400, { 0x04: [0x24, 0xff, 0xae, 0x51, 0x69, 0x9a, 0xa2, 0x21] });
  assert.equal(coreFromHeader(gba), 'gba');
});

test('a Colecovision cart is identified by its boot signature', () => {
  assert.equal(coreFromHeader(rom(0x2000, { 0: [0xaa, 0x55] })), 'coleco');
  assert.equal(coreFromHeader(rom(0x2000, { 0: [0x55, 0xaa] })), 'coleco');
});

// A SNES cart has no magic number at all; the checksum pair is the tell.
test('a Super Nintendo rom is found by its checksum complement', () => {
  const snes = rom(0x8000);
  snes.set([0x34, 0x12], 0x7fc0 + 0x1c); // complement 0x1234
  snes.set([0xcb, 0xed], 0x7fc0 + 0x1e); // checksum   0xEDCB, XOR = 0xFFFF
  assert.equal(coreFromHeader(snes), 'snes');
});

test('a Super Nintendo rom is still found behind a 512-byte copier header', () => {
  const snes = rom(0x8000 + 512);
  snes.set([0x34, 0x12], 512 + 0x7fc0 + 0x1c);
  snes.set([0xcb, 0xed], 512 + 0x7fc0 + 0x1e);
  assert.equal(coreFromHeader(snes), 'snes');
});

test('a checksum that does not complement is not called a SNES rom', () => {
  const notSnes = rom(0x8000);
  notSnes.set([0x34, 0x12], 0x7fc0 + 0x1c);
  notSnes.set([0x99, 0x99], 0x7fc0 + 0x1e);
  assert.equal(coreFromHeader(notSnes), null);
});

test('random bytes and short files yield nothing rather than a guess', () => {
  assert.equal(coreFromHeader(rom(4096, { 0: [1, 2, 3, 4, 5, 6] })), null);
  assert.equal(coreFromHeader(new Uint8Array(4)), null);
  assert.equal(coreFromHeader(null), null);
});

// --- combining evidence ---------------------------------------------------

// The exact failure that started this: a Colecovision game called dk.bin was
// booted as an NES, which starts the emulator and then runs nothing.
test('dk.bin is identified as Colecovision, not defaulted to NES', () => {
  const dk = rom(16384, { 0: [0xaa, 0x55] });
  const out = detectCore({ name: 'dk.bin', bytes: dk });
  assert.equal(out.core, 'coleco');
  assert.equal(out.via, 'header');
});

test('the header beats a misleading extension', () => {
  // A Mega Drive rom that somebody renamed .nes.
  const md = rom(0x400, { 0x100: 'SEGA MEGA DRIVE ' });
  assert.equal(detectCore({ name: 'game.nes', bytes: md }).core, 'segaMD');
});

test('the extension is used when there are no bytes to read yet', () => {
  const out = detectCore({ name: 'Chrono Trigger (USA).sfc' });
  assert.equal(out.core, 'snes');
  assert.equal(out.via, 'extension');
});

// Release and folder names are how a torrent says what a .bin actually is.
test('a torrent folder name resolves an ambiguous rom', () => {
  const out = detectCore({
    name: 'Chrono Trigger (USA).bin',
    context: 'Super Nintendo Full ROM Set (No-Intro)/',
    bytes: rom(64, { 0: [9, 9, 9, 9] }),
  });
  assert.equal(out.core, 'snes');
  assert.equal(out.via, 'context');
});

test('context matches the longest platform name first', () => {
  assert.equal(coreFromContext('Mario Kart [Game Boy Advance]'), 'gba');
  assert.equal(coreFromContext('Tetris [Game Boy]'), 'gb');
  assert.equal(coreFromContext('Sonic [Sega 32X]'), 'sega32x');
  assert.equal(coreFromContext('Sonic [Mega Drive]'), 'segaMD');
});

// A stray platform word in a folder name must not make a readme playable.
test('context alone cannot turn a non-rom into a game', () => {
  const out = detectCore({ name: 'readme.txt', context: 'SNES Collection' });
  assert.equal(out.core, null);
  assert.equal(out.via, 'unknown');
});

// Atari 2600 carts are raw code with no header at all.
test('a headerless cart falls back to its cartridge size', () => {
  assert.equal(coreFromSize(4096), 'atari2600');
  assert.equal(coreFromSize(4097), null);
  const out = detectCore({ name: 'Combat.bin', bytes: rom(4096, { 0: [0x78, 0xd8] }) });
  assert.equal(out.core, 'atari2600');
  assert.equal(out.via, 'size');
});

// The size rule is weak, so anything with real evidence must outrank it.
test('size never overrides a header', () => {
  const nes = rom(8192, { 0: 'NES\x1a' });
  const out = detectCore({ name: 'game.bin', bytes: nes });
  assert.equal(out.core, 'nes');
  assert.equal(out.via, 'header');
});

test('an unidentifiable rom refuses rather than defaulting', () => {
  const out = detectCore({ name: 'mystery.rom', bytes: rom(999, { 0: [1, 2, 3] }) });
  assert.equal(out.core, null);
  assert.equal(out.via, 'unknown');
});

test('detectCore survives being called with nothing', () => {
  assert.doesNotThrow(() => detectCore());
  assert.equal(detectCore().core, null);
});

// Against a real ROM rather than a synthetic one: nestest.nes is the NES test
// cartridge shipped with this app as the test torrent's payload.
test('a real rom is identified correctly however it is named', async () => {
  const { readFileSync } = await import('node:fs');
  const bytes = new Uint8Array(readFileSync(new URL('../dist/roms/nestest.nes', import.meta.url)));

  assert.equal(detectCore({ name: 'nestest.nes', bytes }).core, 'nes');
  // Renamed to the ambiguous extension that caused the original bug.
  assert.equal(detectCore({ name: 'nestest.bin', bytes }).core, 'nes');
  // And renamed to a confidently WRONG extension: the header still wins, which
  // is the whole point -- dumps get renamed, headers do not.
  const misnamed = detectCore({ name: 'nestest.smc', bytes });
  assert.equal(misnamed.core, 'nes');
  assert.equal(misnamed.via, 'header');
});
