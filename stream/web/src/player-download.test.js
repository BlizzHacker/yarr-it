import test from 'node:test';
import assert from 'node:assert/strict';
import { playerDownloadURL } from './player-download.js';

test('the EmulatorJS toolbar accepts public Archive and Vimm downloads', () => {
  assert.equal(
    playerDownloadURL('https://archive.org/download/contra/contra.nes'),
    'https://archive.org/download/contra/contra.nes',
  );
  assert.equal(
    playerDownloadURL('https://vimm.net/vault/265?download=1'),
    'https://vimm.net/vault/265?download=1',
  );
});

test('hidden Webmulator ROM paths and lookalike hosts never become downloads', () => {
  for (const input of [
    'https://downloads.webmulator.com/play.php?rom_url=/roms/contra.zip',
    'https://archive.org.evil.example/download/x/y.nes',
    'http://archive.org/download/x/y.nes',
    'javascript:alert(1)',
  ]) assert.equal(playerDownloadURL(input), '', input);
});
