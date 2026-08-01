import test from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// Nobody may hard-code the relay's scheme.
//
// This is a guard rather than a unit test because the bug is a copy-paste one
// and it has already happened three times: bridge-peer.js, tracker-udp.js and
// dht.js each grew their own `wss://${location.host}/bridge/socket`. Fixing the
// three does nothing to stop a fourth, and the failure is invisible in every
// way that normally catches a regression -- it only shows up on a plain-http
// install, where the browser refuses the secure socket and the torrent simply
// finds no peers. Search and direct playback keep working, so the app looks
// healthy. There is one correct way to build this URL, and it lives in
// server.js; this asserts nothing else builds its own.

const SRC = dirname(fileURLToPath(import.meta.url));

// A literal scheme glued to the bridge path. Deliberately narrow: engine.js
// legitimately holds `wss://tracker.openwebtorrent.com`, which is a real
// external tracker and not this at all.
const HARDCODED = /\bwss?:\/\/[^`'"\s]*\/bridge\/socket/;

test('no module builds the relay URL with a hard-coded scheme', () => {
  const offenders = [];

  for (const name of readdirSync(SRC, { recursive: true })) {
    if (!name.endsWith('.js') || name.endsWith('.test.js')) continue;
    const body = readFileSync(join(SRC, name), 'utf8');
    for (const [i, line] of body.split('\n').entries()) {
      if (HARDCODED.test(line)) offenders.push(`${name}:${i + 1}: ${line.trim()}`);
    }
  }

  assert.deepEqual(
    offenders,
    [],
    `Build the relay URL with bridgeSocketURL() from server.js, which derives the\n` +
      `scheme from the page and honours a configured server. Offending lines:\n` +
      offenders.join('\n'),
  );
});

test('the guard would actually catch the bug it is guarding against', () => {
  // A guard that cannot fail is worse than no guard, because it reads like
  // coverage. These are the three real regressions, verbatim.
  assert.ok(HARDCODED.test('const bridgeURL = () => `wss://${location.host}/bridge/socket`;'));
  assert.ok(HARDCODED.test('const BRIDGE_URL = `wss://${location.host}/bridge/socket`;'));
  assert.ok(HARDCODED.test("const u = 'ws://192.168.1.50:8802/bridge/socket';"));

  // ...and does not fire on the correct form, or on an unrelated wss:// URL.
  assert.ok(!HARDCODED.test('return `${scheme}//${location.host}/bridge/socket`;'));
  assert.ok(!HARDCODED.test("return base.replace(/^http/, 'ws') + '/bridge/socket';"));
  assert.ok(!HARDCODED.test("  'wss://tracker.openwebtorrent.com',"));
});
