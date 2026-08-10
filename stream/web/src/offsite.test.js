/**
 * A tile must not promise something a click cannot deliver.
 *
 * This is the same rule the discover tiles were corrected to on 2026-08-08 --
 * a tile carries a verified target or says plainly that it does not -- pointed
 * at the case that arrived with the Vimm's Lair catalogue: a result that IS
 * verified and IS openable, and is on somebody else's website.
 *
 * "Play" is the specific word at issue. It is what the games domain's verb
 * resolves to, it is what every other game card on the page says, and over a
 * link to vimm.net it would be false three times over: this site does not host
 * it, cannot stream it, and cannot boot it in our player.
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { tileAction, itemFromCard, sourceBadge } from './home.js';

const here = dirname(fileURLToPath(import.meta.url));
const read = (p) => readFileSync(join(here, p), 'utf8');

const VIMM = {
  name: "Vimm's Lair",
  short: 'Vimm',
  host: 'vimm.net',
  page: 'https://vimm.net/vault/3',
};

const vimmCard = (over = {}) => ({
  key: 'vimm:nes:10 yard fight',
  title: '10-Yard Fight',
  kind: 'game',
  instant: false,
  platform: 'Nintendo',
  system: 'nes',
  external: VIMM,
  best: 0,
  seeders: 0,
  sources: [
    {
      title: '10-Yard Fight (USA, Europe).nes',
      indexer: "Vimm's Lair",
      magnet: 'https://vimm.net/vault/?p=play&mediaId=3',
      action: 'play',
      offsite: true,
      webSafe: false,
      size: 16384,
      sizeHuman: '16 KB',
    },
    {
      title: '10-Yard Fight (USA, Europe).nes',
      indexer: "Vimm's Lair",
      magnet: 'https://dl3.vimm.net/?mediaId=3',
      action: 'download',
      offsite: true,
      webSafe: false,
      size: 16384,
      sizeHuman: '16 KB',
    },
  ],
  ...over,
});

test('a tile for another site never says Play', () => {
  const action = tileAction(itemFromCard(vimmCard(), 'game'));
  assert.notEqual(action.label, 'Play',
    'the tile promises our player over a link to somebody else\'s website');
  assert.match(action.label, /Vimm/,
    'the tile does not say where the click goes');
  assert.match(action.label, /↗/,
    'nothing marks this as an outbound link');
});

test('the site name reaches the tile from the server, not from a hostname guess', () => {
  const item = itemFromCard(vimmCard(), 'game');
  assert.deepEqual(item.external, VIMM,
    'itemFromCard drops the external block, so the tile has to guess');
});

test('an ordinary game card is completely unaffected', () => {
  // The archive.org shape: hosted, playable in our own player.
  const ia = {
    key: 'ia:contra_nes',
    title: 'Contra',
    kind: 'game',
    instant: true,
    best: 0,
    sources: [{ title: 'Contra', indexer: 'EmulatorJS', magnet: 'https://archive.org/details/contra_nes#ejs' }],
  };
  assert.equal(tileAction(itemFromCard(ia, 'game')).label, 'Play');

  // And a torrent, which is found but not launchable from here.
  const torrent = {
    key: 'abc',
    title: 'Some ROM Set',
    kind: 'game',
    instant: false,
    best: 0,
    sources: [{ title: 'Some ROM Set', indexer: 'x', magnet: 'magnet:?xt=urn:btih:abc' }],
  };
  assert.equal(tileAction(itemFromCard(torrent, 'game')).label, 'Open');
});

test('a card with no external block keeps the old behaviour exactly', () => {
  const c = vimmCard({ external: null });
  assert.equal(tileAction(itemFromCard(c, 'game')).label, 'Open',
    'without an external block this must fall through to the unchanged path');
});

// ------------------------------------------------------------- the labels --
//
// "Label things on where they're from so it's easy to see." Every class of
// result has to answer it, not just the new one — a grid where an archive.org
// game, a torrent and a Vimm link are indistinguishable is the same defect as a
// tile that says Play over a link, one step earlier.

test('every class of result says where it came from', () => {
  const ia = sourceBadge({
    origin: 'archive.org', instant: true, best: 0,
    sources: [{ indexer: 'EmulatorJS' }],
  });
  assert.equal(ia.text, 'archive.org',
    'a hosted game must name the place, not the emulator that runs it');
  assert.match(ia.cls, /instant/, 'the hosted styling was lost');

  const vimm = sourceBadge({
    origin: "Vimm's Lair", external: { name: "Vimm's Lair", short: 'Vimm', host: 'vimm.net' },
    best: 0, sources: [{ indexer: "Vimm's Lair" }],
  });
  assert.equal(vimm.text, "Vimm's Lair");
  assert.match(vimm.cls, /offsite/);
  assert.doesNotMatch(vimm.cls, /instant/,
    'an off-site result must not wear the colour that means "this will just work"');

  const torrent = sourceBadge({
    seeders: 812, best: 0,
    sources: [{ indexer: 'YTS', seeders: 812 }],
  });
  assert.match(torrent.text, /YTS/, 'a torrent does not name its indexer');
  assert.match(torrent.text, /812/,
    'the seeder count left the badge; for a torrent, who and will-it-work are one fact');
  assert.doesNotMatch(torrent.cls, /dead/);

  const dead = sourceBadge({ seeders: 0, best: 0, sources: [{ indexer: '1337x', seeders: 0 }] });
  assert.match(dead.cls, /dead/, 'an unseeded torrent lost its warning colour');
  assert.match(dead.text, /1337x/);
});

test('a music result is labelled by its own source row, with no second rule', () => {
  // music.go builds cards whose source indexer is already the place, so the
  // label must fall through to it rather than needing a case of its own.
  const music = sourceBadge({
    origin: 'archive.org', instant: true, best: 0,
    sources: [{ indexer: 'Archive.org' }],
  });
  assert.equal(music.text, 'archive.org');
});

test('a card the server said nothing about still labels itself', () => {
  // No origin, no sources: the badge must degrade to something true rather
  // than printing "undefined" over the artwork.
  const bare = sourceBadge({ seeders: 0 });
  assert.equal(bare.text, '0▲');
  assert.doesNotMatch(bare.text, /undefined/);

  const hosted = sourceBadge({ instant: true });
  assert.equal(hosted.text, 'hosted');
});

test('every badge carries a hint that explains what the place means', () => {
  for (const c of [
    { origin: 'archive.org', instant: true, best: 0, sources: [{ indexer: 'EmulatorJS' }] },
    { origin: "Vimm's Lair", external: VIMM, best: 0, sources: [{ indexer: "Vimm's Lair" }] },
    { seeders: 5, best: 0, sources: [{ indexer: 'YTS' }] },
  ]) {
    const b = sourceBadge(c);
    assert.ok(b.hint && b.hint.length > 10, `no hint for ${JSON.stringify(c.origin || c.seeders)}`);
    assert.doesNotMatch(b.hint, /undefined/);
  }
});

// ---------------------------------------------------------------- the rows --
//
// These assert on source text, for the reason regression.test.js gives: the
// behaviour is a DOM construction inside a 3,000-line module with no seam to
// exercise, and the cost of getting it wrong is a click that silently does
// something other than what the row said.

test('an off-site source row is a link, so the click cannot be routed anywhere else', () => {
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function offsiteSourceRow'), main.indexOf('function closeDetail'));
  assert.ok(fn.length > 0, 'offsiteSourceRow not found');

  assert.match(fn, /el\('a',/, 'the row is not an anchor');
  assert.match(fn, /row\.target = '_blank'/, 'the link does not open in a new tab');
  assert.match(fn, /noopener/, 'a new tab without noopener hands the opened page this window');
  assert.doesNotMatch(fn, /play\(card/,
    'an off-site row calls play(); that routes another site into our player');

  // The row must state which of the two things it is, because the catalogue
  // publishes them separately and they are not the same offer.
  assert.match(fn, /PLAY THERE/, 'a playable off-site row does not say where it plays');
  assert.match(fn, /DOWNLOAD/, 'a downloadable off-site row does not say it downloads');
  assert.match(fn, /leaves this site/, 'nothing on the row says it leaves');
});

test('sourceRow hands every off-site source to the link path', () => {
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function sourceRow('), main.indexOf('function offsiteSourceRow'));
  assert.ok(fn.length > 0, 'sourceRow not found');
  assert.match(fn, /if \(s\.offsite\) return offsiteSourceRow\(/,
    'an off-site source can still reach the button path, where a click calls play()');
});

test('every tile draws its source badge from the one shared rule', () => {
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function tile(card)'), main.indexOf('function openCard'));
  assert.ok(fn.length > 0, 'tile() not found');
  assert.match(fn, /sourceBadge\(card\)/,
    'tile() builds its badge by hand; that is how the three classes drift apart');
  assert.doesNotMatch(fn, /'INSTANT'/,
    'a hard-coded INSTANT means that tile says how confident to be and never says who from');
});

test('the detail sheet says where the links go before any of them is clicked', () => {
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function openDetail'), main.indexOf('function renderSaveButton'));
  assert.ok(fn.length > 0, 'openDetail not found');
  assert.match(fn, /opens \$\{card\.external\.host\} in a new tab/,
    'the source list header does not say the links leave the site');
  assert.match(fn, /Catalogued on \$\{card\.external\.name\}/,
    'the subtitle does not say who actually holds this');
});

test('an anchor in the source list is styled to sit in it', () => {
  const html = read('../index.html');
  assert.match(html, /a\.source\s*\{[^}]*text-decoration:\s*none/,
    'a.source has no rule, so the off-site rows render underlined and misaligned');
  assert.match(html, /\.badge\.offsite\s*\{/, '.badge.offsite has no rule');
});
