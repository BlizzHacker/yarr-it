import test from 'node:test';
import assert from 'node:assert/strict';
import { webmulatorEmbedURL, webmulatorResolver } from './webmulator.js';

const contra = 'https://www.webmulator.com/games/nintendo/contra/mobile';

test('accepts only the public Webmulator mobile player route', () => {
  assert.equal(webmulatorEmbedURL(contra), contra);
  for (const bad of [
    'https://evil.example/webmulator.com/games/nintendo/contra/mobile',
    'https://webmulator.com.evil.example/games/nintendo/contra/mobile',
    'https://www.webmulator.com/games/nintendo/contra',
    'https://downloads.webmulator.com/play.php?rom_url=/roms/contra.zip',
    'http://www.webmulator.com/games/nintendo/contra/mobile',
  ]) assert.equal(webmulatorEmbedURL(bad), null, bad);
});

test('embeds the official page without fetching or extracting a ROM URL', async () => {
  const out = await webmulatorResolver.resolve({ uri: contra });
  assert.equal(out.render, 'embed');
  assert.equal(out.src, contra);
  assert.equal(out.mime, 'text/html');
  assert.ok(!out.src.includes('rom_url'));
  assert.ok(!out.src.includes('downloads.webmulator.com'));
});
