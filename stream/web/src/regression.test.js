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

// Eleven filter controls shipped and not one of them survived a reload. The
// controls are wired one by one in init(), so the way this comes back is
// somebody adding a twelfth and wiring it like the other eleven were.
test('every control on the filter bar writes what it changed to storage', () => {
  const main = read('main.js');
  const html = read('../index.html');

  const ids = [...html.matchAll(/id="(f-[a-z]+)"/g)].map((m) => m[1])
    .filter((id) => id !== 'f-groups' && id !== 'f-quality' && id !== 'f-codec'
                 && id !== 'f-lang-group' && id !== 'f-reset');
  assert.ok(ids.length >= 8, `expected the filter bar's controls, found ${ids.length}`);

  for (const id of ids) {
    // Most are wired one per line; the two source chips share a loop, so the
    // fallback is "wherever this id is mentioned in init".
    let at = main.indexOf(`$('#${id}').addEventListener`);
    if (at === -1) at = main.indexOf(`['#${id}'`);
    assert.notEqual(at, -1, `#${id} has no handler in main.js`);
    const body = main.slice(at, at + 700);
    assert.match(body, /persistFilters\(\)|setLanguage\(/,
      `#${id} changes a filter without saving it — that is the reload bug`);
  }

  // The two chip rows are drawn rather than declared, so they are checked where
  // they are built instead of by id.
  const groupRow = main.slice(main.indexOf('function groupRow'), main.indexOf('function chipRow'));
  assert.match(groupRow, /changeGroups\(/, 'category chips must go through changeGroups');
  const chipRow = main.slice(main.indexOf('function chipRow'), main.indexOf('function showSkeletons'));
  assert.match(chipRow, /persistFilters\(\)/, 'quality/codec chips must be saved');

  // A reset that a reload undoes is not a reset.
  const clear = main.slice(main.indexOf('function clearAllFilters'), main.indexOf('function fillLanguageMenu'));
  assert.match(clear, /prefs\.clearFilters\(\)/,
    'clearing the filters must clear the STORED filters too');
});

// `video { max-width:100% }` caps a picture and never lifts one, which is how
// 320x240 Nostalgia TV files played at 320x240 on a 1080p screen. The fix is a
// seam in main.js, so it comes back by someone adding a third render path that
// fits the embed and forgets the video.
test('every path that fits a player also fits the video in it', () => {
  const main = read('main.js');
  const embed = (main.match(/fitEmbedToStage\(out, el\)/g) || []).length;
  const video = (main.match(/fitVideoToStage\(out, el\)/g) || []).length;
  assert.ok(embed > 0, 'no player render path found');
  assert.equal(video, embed,
    'a render path fits the embed but not the video: small sources will play small');
});

// Both copies are published; if they drift, the one users curl is the stale one.
test('both copies of the installer are identical', () => {
  const published = read('../selfhost.sh');
  const source = read('../../deploy/selfhost.sh');
  assert.equal(published, source,
    'web/selfhost.sh and deploy/selfhost.sh have drifted; web/ is the one served');
});

test('saved titles have a dedicated, reachable library screen', () => {
  const main = read('main.js');
  const html = read('../index.html');

  assert.match(html, /id="library-open"[^>]+href="\/\?library=1"/,
    'the signed-in header has no link to the saved library');
  assert.match(html, /id="saved-library"/,
    'the saved library has no dedicated mount');
  const show = main.slice(main.indexOf('async function showSavedLibrary'),
    main.indexOf('async function browseDomain'));
  assert.match(show, /#saved-library/,
    'the saved-library route does not render its dedicated mount');
  assert.doesNotMatch(show, /const host = \$\('#library'\)/,
    'saved titles were mounted into the transient playlist browser');
});

// Video had autoplay while audio did not. Archive music and audiobook tracks
// resolved correctly, attached a valid MP3, then sat forever behind the word
// "Resolving" because nothing asked the audio element to start.
test('Archive audio starts under the same autoplay policy as video', () => {
  const main = read('main.js');
  const html = read('../index.html');

  assert.match(html, /<audio id="audio" controls autoplay hidden>/,
    'the audio element does not start a track selected by the user');
  assert.match(main, /\['#video', '#audio'\]/,
    'the saved autoplay setting is applied to video but not audio');
  assert.match(main, /const starting = el\.play\(\)/,
    'attaching an Archive MP3 never explicitly starts its audio element');
});
