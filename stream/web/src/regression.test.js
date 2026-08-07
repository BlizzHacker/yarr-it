/**
 * Guards for defects that shipped and stayed invisible.
 *
 * These assert on source text, which is normally a poor kind of test. It earns
 * its place here because each defect was a single call or a missing line in a
 * generated shell script -- there is no behaviour to exercise in isolation, and
 * the cost of the regression was a user-visible failure that looked like
 * something else entirely.
 */

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const read = (p) => readFileSync(join(here, p), 'utf8');

// A prefilled field plus focus() means the first keystroke merges with the old
// value: typing an address over "yarrit.com" produced "192.168.0.yarrit.com".
// On a TV remote it was worse -- clearing it took 22 presses.
test('the settings field replaces its value rather than merging', () => {
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function openSettings'), main.indexOf('function closeSettings'));

  assert.ok(fn.length > 0, 'openSettings not found');
  assert.match(fn, /#set-server'\)\.select\(\)/,
    'openSettings must select() the existing value so typing replaces it');
  assert.doesNotMatch(fn, /#set-server'\)\.focus\(\)/,
    'focus() leaves a cursor in the old value; that is the merge bug');
});

// A hard-coded wss:// meant the peer relay could never connect from a
// plain-http self-host. Only torrents broke, so nothing looked wrong.
test('the relay scheme is never hard-coded', () => {
  const bridge = read('bridge-peer.js');
  assert.doesNotMatch(bridge, /`wss:\/\//,
    'bridge-peer must not build a wss:// URL itself');
  assert.match(bridge, /bridgeSocketURL/,
    'bridge-peer must derive its URL from the configured server');
});

// The installer built the gateway and never ran it, and nothing served the web
// app -- so the address it proudly printed was a 404.
test('the self-host installer starts every component it builds', () => {
  const sh = read('../selfhost.sh');

  const start = sh.slice(sh.indexOf('cat > "$HOME_DIR/start.sh"'), sh.indexOf('chmod +x "$HOME_DIR/start.sh"'));
  assert.ok(start.length > 0, 'generated start.sh not found in the installer');

  for (const svc of ['mw-bridge', 'mw-search', 'mw-gateway', 'serve-local.mjs']) {
    assert.ok(start.includes(svc), `start.sh never launches ${svc}`);
  }

  // Built but not shipped is the same failure one step earlier.
  for (const built of ['search', 'bridge', 'gateway']) {
    assert.match(sh, new RegExp(`for svc in[^\\n]*${built}`),
      `the installer does not build ${built}`);
  }

  // The gateway is reached directly by TVs; on loopback it is useless to them.
  assert.match(start, /mw-gateway["'\s-]+addr 0\.0\.0\.0/,
    'the gateway must bind the LAN, not loopback');
});

// Both copies are published; if they drift, the one users curl is the stale one.
test('both copies of the installer are identical', () => {
  const published = read('../selfhost.sh');
  const source = read('../../deploy/selfhost.sh');
  assert.equal(published, source,
    'web/selfhost.sh and deploy/selfhost.sh have drifted; web/ is the one served');
});
