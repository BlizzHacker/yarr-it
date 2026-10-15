/**
 * A tile must not promise something a click cannot deliver.
 *
 * Same rule as offsite.test.js, pointed at the case that arrives with the
 * Minerva Archive: a result that IS verified and IS fetchable, and that is not
 * a thing at all — it is a container of thousands of things.
 *
 * The specific word at issue here is any VERB. "Play" would offer a game that
 * is one of 3,647 in an 8.4 GB torrent; "Read" would offer a manual that is one
 * of a 1.64 TB mirror. Both are the same defect the TMDB tiles had on
 * 2026-08-08 — a word describing an intention rather than an outcome — and
 * both are worse here, because the outcome is measured in terabytes.
 *
 * So a set tile says what it is and what it weighs, and the size is not
 * decoration: it is the decision.
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { tileAction, itemFromCard, itemFromDiscover, sourceBadge } from './home.js';
import { filtersFromSearchURL, paramsForSearch } from './search-route.js';

const here = dirname(fileURLToPath(import.meta.url));
const read = (p) => readFileSync(join(here, p), 'utf8');

/**
 * The same file with its comments removed.
 *
 * Needed by the wording tests below, which assert that certain phrases never
 * reach a person. Those phrases are quoted in the comments that explain WHY
 * they are banned, so scanning the raw file finds the explanation and calls it
 * the offence.
 */
const code = (p) => read(p)
  // CRLF first. These files are checked out with Windows line endings, and a
  // `$`-anchored line match silently never fires against a trailing \r — which
  // fails open, leaving comments in and the assertion passing for the wrong
  // reason.
  .replace(/\r\n/g, '\n')
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/^[ \t]*\/\/.*$/gm, '');

const SNES_SET = {
  name: 'Nintendo - Super Nintendo Entertainment System',
  collection: 'No-Intro',
  path: 'No-Intro/Nintendo - Super Nintendo Entertainment System',
  sizeBytes: 8_400_000_000,
  sizeHuman: '7.8 GiB',
  files: 3647,
  peersKnown: false,
  measuredAt: '2026-04-14T00:11:35Z',
};

const setCard = (over = {}) => ({
  key: 'minerva:0bcb99da',
  title: 'Nintendo - Super Nintendo Entertainment System',
  kind: 'game',
  instant: false,
  external: null,
  system: 'snes',
  origin: 'Minerva Archive',
  best: 0,
  seeders: 0,
  set: { ...SNES_SET },
  sources: [
    {
      title: 'Nintendo - Super Nintendo Entertainment System',
      indexer: 'Minerva Archive',
      magnet: 'magnet:?xt=urn:btih:0bcb99da&dn=No-Intro+-+Nintendo&tr=udp%3A%2F%2Ft.example',
      action: 'download',
      source: 'No-Intro',
      webSafe: false,
      size: 8_400_000_000,
      sizeHuman: '7.8 GiB',
    },
  ],
  ...over,
});

// ------------------------------------------------------------- the verb --

test('a set tile never takes a domain verb', () => {
  const action = tileAction(itemFromCard(setCard(), 'game'));
  assert.equal(action.kind, 'set');
  assert.doesNotMatch(action.label, /^Play$/,
    'a set of 3,647 games was offered as though it were one game');
  assert.doesNotMatch(action.label, /Open|Find|Watch|Listen/,
    'a set took a verb from the domain vocabulary');
});

test('a documentation set is never labelled Read', () => {
  // bitsavers is filed under literature so it is browsable on the right shelf.
  // That must not give it that shelf's verb: the smallest thing anybody can
  // fetch from it is 1.64 TB.
  const docs = setCard({
    kind: 'literature',
    title: 'bitsavers',
    set: { ...SNES_SET, name: 'bitsavers', collection: 'bitsavers', sizeHuman: '1.5 TiB' },
  });
  const action = tileAction(itemFromCard(docs, 'literature'));
  assert.equal(action.kind, 'set');
  assert.doesNotMatch(action.label, /Read/,
    'a 1.64 TB documentation mirror was offered with the word Read');
  assert.match(action.label, /1\.5 TiB/, 'the size did not reach the label');
});

test('a set tile states its size inside the label itself', () => {
  // A tile shows no source rows, so if the size is not in the label it is
  // nowhere at all — and that is how somebody queues 6.75 TB by accident.
  const action = tileAction(itemFromCard(setCard(), 'game'));
  assert.match(action.label, /7\.8 GiB/, 'the size is not in the tile label');
  assert.match(action.label, /set/i, 'the label does not say this is a whole set');
});

test('a set with no size still says so rather than showing nothing', () => {
  const c = setCard({ set: { ...SNES_SET, sizeHuman: '' } });
  const action = tileAction(itemFromCard(c, 'game'));
  assert.match(action.label, /unknown/i,
    'a size-less set rendered a bare label with no hint that the number is missing');
});

test('the set rung runs before the reader rung', () => {
  // Belt and braces rather than a live fix: archiveIdFrom answers '' for a
  // magnet, so today a set could not reach the reader anyway. It is one line to
  // keep it that way if either of those changes.
  const c = setCard({
    kind: 'literature',
    sources: [{ ...setCard().sources[0], magnet: 'https://archive.org/details/somebook' }],
  });
  assert.equal(tileAction(itemFromCard(c, 'literature')).kind, 'set',
    'a set whose source happens to look like an archive.org item became a reader tile');
});

// ------------------------------------------------- both normalisers carry it --

test('both normalisers carry the set through', () => {
  // This is the exact shape of the bug b71f7ad found, when itemFromDiscover
  // dropped `external` and every Vimm browse tile fell through to canPlay.
  // tileAction reads ONE property and must not have to know which endpoint
  // built the item.
  const fromCard = itemFromCard(setCard(), 'game');
  assert.ok(fromCard.set, 'itemFromCard dropped the set');

  const fromDiscover = itemFromDiscover({
    title: 'Nintendo - Super Nintendo Entertainment System',
    mediaType: 'game',
    play: 'magnet:?xt=urn:btih:0bcb99da',
    source: 'Minerva Archive',
    set: { ...SNES_SET },
  }, 'game');
  assert.ok(fromDiscover.set, 'itemFromDiscover dropped the set');

  assert.equal(tileAction(fromCard).kind, tileAction(fromDiscover).kind,
    'the same set behaves differently depending on which endpoint built it');
  assert.equal(tileAction(fromDiscover).label, tileAction(fromCard).label);
});

test('an item that is not a set is untouched', () => {
  const plain = itemFromDiscover({ title: 'Super Mario World', mediaType: 'game',
    play: 'https://archive.org/details/smw' }, 'game');
  assert.equal(plain.set, null,
    'a nil set is the positive statement that this tile is one work');
  assert.notEqual(tileAction(plain).kind, 'set');
});

// ---------------------------------------------------------------- the badge --

test('a set badge never prints a number and never wears the dead colour', () => {
  const badge = sourceBadge(setCard());
  assert.match(badge.cls, /set/, 'the set badge lost its own class');
  assert.doesNotMatch(badge.cls, /dead/,
    'an unmeasured swarm wore the colour that means "this will not work"');
  assert.doesNotMatch(badge.cls, /instant/);
  assert.doesNotMatch(badge.text, /\d/,
    'the badge printed a number; nobody has counted these peers since April 2026');
  assert.doesNotMatch(badge.text, /▲/, 'the seeder glyph reached a set badge');
  assert.match(badge.text, /Minerva/, 'the badge does not say where it came from');
  // The absence has to be explained, or it is its own kind of confusing.
  assert.match(badge.hint, /peers/i);
});

test('the badge is ready for the day somebody measures the swarm', () => {
  const measured = sourceBadge(setCard({ set: { ...SNES_SET, peersKnown: true } }));
  assert.doesNotMatch(measured.hint, /Nobody has counted/,
    'a measured swarm still apologised for having no count');
});

// ----------------------------------------------------------- the source rows --
//
// Read out of main.js as text, the way offsite.test.js reads it: these are
// wiring facts about a module that needs a DOM to run.

test('a magnet is rendered as an anchor and never as a button', () => {
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function setSourceRow'));
  const body = fn.slice(0, fn.indexOf('\nfunction '));

  assert.match(body, /el\('a'/,
    'a magnet row must be an anchor: an anchor says where it goes and cannot be '
    + 'routed into the player, because there is no handler to route');
  assert.doesNotMatch(body, /el\('button'/, 'a magnet ended up in a button');
  assert.doesNotMatch(body, /addEventListener\(\s*'click'/,
    'a click handler on a magnet row is one refactor from calling play()');
});

test('a set row carries no torrent vocabulary', () => {
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function setSourceRow'));
  const body = fn.slice(0, fn.indexOf('\nfunction '));

  for (const wrong of ['s.quality', 's.codec', 's.webSafe', 's.seeders', '▲']) {
    assert.ok(!body.includes(wrong),
      `a set row printed ${wrong}, which renders as "unknown" or "0" and makes `
      + 'the most reliable row on the page read as the most broken');
  }
  assert.match(body, /sizeHuman/, 'the size left the set row; it is the decision');
});

test('a set row does not claim to leave the site', () => {
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function setSourceRow'));
  const body = fn.slice(0, fn.indexOf('\nfunction '));

  assert.ok(!body.includes('leaves this site'),
    'a magnet is handed to a program on your own machine — nothing navigates, '
    + 'and no page of anybody else\'s website opens');
  assert.ok(!body.includes("target = '_blank'") && !body.includes('noopener'),
    'a magnet row was given new-tab semantics it has no use for');
});

test('sourceRow hands every set source to the set path, before anything else', () => {
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function sourceRow('));
  const body = fn.slice(0, fn.indexOf('\n}'));

  const set = body.indexOf('card.set');
  const onSite = body.indexOf('s.onSite');
  const offsite = body.indexOf('s.offsite');
  assert.ok(set >= 0, 'sourceRow has no set branch, so a magnet reaches the torrent path');
  assert.ok(set < onSite && set < offsite,
    'the set branch must come first: the torrent path at the bottom calls play()');
});

// ------------------------------------------------------------- the sentence --

test('the detail sheet gets a fourth sentence, not one of the other three', () => {
  const main = read('main.js');
  const sheet = main.slice(main.indexOf('function openDetail'));
  const body = sheet.slice(0, sheet.indexOf('\n}'));

  assert.match(body, /if \(card\.set\)/,
    'the detail sheet has no branch for a set, so it uses a sentence written about '
    + 'something else');
  const setBranch = body.indexOf('if (card.set)');
  const externalBranch = body.indexOf('card.external');
  assert.ok(setBranch < externalBranch,
    'the set branch must be tested before external, or a set would take the '
    + '"opens in a new tab" sentence');
});

test('an unknown file list renders nothing at all, not a hedge', () => {
  assert.match(read('main.js'), /card\.set\.contains\?\.file/,
    'the contains line is not guarded, so a set with no file list would render a '
    + 'sentence about contents nobody has read');
  // And specifically, no wording anywhere in the code that could be read as a
  // weak yes. Comments are stripped first: the ban is explained in one, and the
  // explanation must not be mistaken for the offence.
  const main = code('main.js');
  for (const hedge of ['may contain', 'contents unknown', 'possibly contains']) {
    assert.ok(!main.includes(hedge),
      `"${hedge}" reads as a weak yes; the honest state is to say nothing`);
  }
});

test('a file count of zero is never printed as "0 files"', () => {
  const main = read('main.js');
  assert.match(main, /card\.set\.files > 0/,
    '"0 files" describes an empty torrent, which is a different and false claim');
});

// ---------------------------------------------------------------- the band --

test('the browse band renders anchors and no seeder count', () => {
  const browse = read('browse.js');
  const fn = browse.slice(browse.indexOf('function paintSets'));
  const body = fn.slice(0, fn.indexOf('\n    }'));

  assert.match(body, /el\('a', 'bset'\)/, 'the band is not built from anchors');
  assert.ok(!body.includes('▲') && !body.includes('seeders'),
    'the band printed a seeder count for a swarm nobody has measured');
  assert.match(body, /sizeHuman/, 'a band item does not state its size');
  assert.match(body, /setsNote/, 'the band lost the sentence that carries the size range');
});

test('the band reads sets from its own field, never from items', () => {
  const browse = read('browse.js');
  assert.match(browse, /row\?\.sets/,
    'the band must read the sibling field: a set inside items renders on the Xbox, '
    + 'Roku and Cartridge clients as a playable game');
  assert.ok(!browse.includes('items.push(...row.sets') && !browse.includes('items.concat(row.sets'),
    'the band was merged into the item grid');
});

// --------------------------------------------------- the search-page band --
//
// Read out of main.js as text, the way the source-row tests above are: this is
// wiring in a module that needs a DOM to run.
//
// The defect these pin down is not a rendering bug, it is a DROPPED ANSWER.
// minervaStage ran on the synchronous first-paint path, respondSearch
// partitioned its cards into `sets`, and the filters, the de-dup and the device
// profile all applied to them -- and then progressive.js merged `body.cards`
// and nothing on the search page ever read the sibling key. Every one of those
// sets was computed and thrown away, and Minerva was visible on category pages
// and nowhere else.

test('the search page reads the sets key and renders it', () => {
  const main = read('main.js');
  assert.ok(main.includes('function paintSetBand('),
    'nothing on the search page reads body.sets, so every set the server '
    + 'computed is thrown away');
  const fn = main.slice(main.indexOf('function paintSetBand('));
  const body = fn.slice(0, fn.indexOf('\n}'));

  assert.match(body, /data\?\.sets/, 'the band does not read the sibling key');
  assert.match(body, /\$\('#sets'\)/, 'the band has no mount of its own');
});

test('the band is painted as results arrive, not only when the search finishes', () => {
  // A set lands in the FIRST response: minervaStage is a scan of memory on the
  // synchronous path, before any goroutine starts. Painting it only at the end
  // would hold the one source that is always ready behind the slowest ones.
  const main = read('main.js');
  const at = main.indexOf('onPaint: ({ cards, added, data: body })');
  assert.ok(at > 0, 'the progressive paint callback moved');
  const body = main.slice(at, main.indexOf('},', at));
  assert.match(body, /paintSetBand\(body\)/,
    'the band is not painted as results arrive');
});

test('a set never reaches the results grid', () => {
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function paintSetBand('));
  const body = fn.slice(0, fn.indexOf('\n}'));
  assert.ok(!body.includes("$('#grid')"),
    'the band wrote into the results grid: a 50 GB container laid among single '
    + 'titles looks exactly like one of them');

  // And the other direction: the merge that builds the grid must keep reading
  // `cards` and nothing else. Comments are stripped first, so the explanation
  // of the rule is not mistaken for a breach of it.
  const merge = code('progressive.js');
  assert.ok(!merge.includes('sets'),
    'the progressive merge learned about sets; the partition exists so that it '
    + 'never has to');
});

test('the band builds its tiles with the shared renderer', () => {
  // Not a second renderer. The badge comes from sourceBadge and the label from
  // tileAction, and both already know what a set is -- so a copy here would be
  // a second place for that to drift, and drifting means the word "Play" over
  // 8.4 GB.
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function paintSetBand('));
  const body = fn.slice(0, fn.indexOf('\n}'));
  assert.match(body, /tile\(c\)/, 'the band hand-rolls its own tile markup');
});

test('an empty grid with a full band is not reported as nothing found', () => {
  // The ordinary shape of ?source=sets: every surviving card IS a container, so
  // they all leave for the band and the grid is empty by construction. Telling
  // somebody to loosen their filters while a band of real answers sits under
  // the message is worse than saying nothing.
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function finishResults('));
  const body = fn.slice(0, fn.indexOf('\n}'));
  const band = body.indexOf('state.sets.length');
  const nothing = body.indexOf('Nothing matched');
  assert.ok(band >= 0, 'finishResults cannot see the band');
  assert.ok(nothing >= 0 && band < nothing,
    '"Nothing matched" is reachable with a band of real answers on screen');
});

test('the result bar counts the band apart from the grid', () => {
  // `total` and the headline count are promises about the GRID: the server
  // partitions sets out before it builds either one. So the band gets its own
  // number rather than being folded into one that does not describe it.
  const main = read('main.js');
  const fn = main.slice(main.indexOf('function updateResultBar('));
  const body = fn.slice(0, fn.indexOf('\n}'));
  assert.match(body, /state\.sets\.length/,
    '"0 results" can sit above a screen that visibly has something on it');
});

// -------------------------------------------------------------- the chip --
//
// filters.Source == "sets" has been understood server-side all along and
// nothing on this page could ask for it. Wiring a chip up naively would have
// produced an empty results page -- cards: [], total: 0, and the whole answer
// in a key nobody read -- so the chip and the band had to arrive together.

test('a link can carry the sets filter', () => {
  const f = filtersFromSearchURL(new URLSearchParams('q=nintendo&source=sets'));
  assert.equal(f.source, 'sets',
    '?source=sets was dropped by the client before it could reach the server');
  assert.equal(paramsForSearch('nintendo', f).get('source'), 'sets',
    'the filter did not survive being written back into the URL');
});

test('an unknown source value is still refused', () => {
  const f = filtersFromSearchURL(new URLSearchParams('q=nintendo&source=everything'));
  assert.equal(f.source, '', 'the vocabulary stopped being a vocabulary');
});

test('the sets chip exists and drives the filter', () => {
  const html = read('../index.html');
  assert.match(html, /id="f-sets"/, 'there is no control for the sets filter');
  assert.match(html, /<section id="sets" hidden>/,
    'there is no mount for the band below the grid');

  const main = read('main.js');
  assert.match(main, /\$\('#f-sets'\)\.addEventListener/, 'the chip is not wired');
  assert.match(main, /state\.filters\.source = on \? '' : 'sets'/,
    'the chip does not toggle the filter it is named after');
});

test('a filter that empties the grid can still be turned off again', () => {
  // Ticking "Sets" leaves every answer in the band and no cards at all. A guard
  // of `state.cards.length` would then make the next chip click do nothing --
  // INCLUDING THE CLICK THAT UNTICKS IT, stranding somebody inside the filter
  // they had just chosen.
  const main = code('main.js');
  assert.ok(!main.includes('if (state.cards.length) refilter()'),
    'a chip guard still tests the grid alone, so a filter that empties the grid '
    + 'cannot be turned off again');
  assert.match(main, /function hasResults\(\)/, 'the shared guard is missing');
});

// ---------------------------------------------------- the origin, and size --

test('the browse band says where a set came from', () => {
  // Every other result on this site names its origin. This band was the one
  // surface rendering Minerva at all, and it was the one that named nobody --
  // while the server has always sent `source` with every item in it.
  const browse = read('browse.js');
  const fn = browse.slice(browse.indexOf('function paintSets'));
  const body = fn.slice(0, fn.indexOf('\n    }'));
  assert.match(body, /it\.source/,
    'the category band drops the origin the server sent with every set');
});

test('the badge and the tile agree about a set with no size', () => {
  // Unreachable from this server -- minervaCard refuses a row whose size is not
  // positive -- but a catalogue is a file on disk and a client renders whatever
  // is in it. The two disagreed: the tile said "size unknown" and the tooltip
  // over it said "undefined".
  const { sizeHuman, ...noSize } = SNES_SET;
  assert.equal(sizeHuman, '7.8 GiB');
  const c = setCard({ set: noSize });

  const badge = sourceBadge(c);
  assert.doesNotMatch(badge.hint, /undefined/,
    'the hint printed the word "undefined", which is not about the thing at all');
  assert.match(badge.hint, /size unknown/i, 'the hint does not say the size is missing');

  const label = tileAction(itemFromCard(c, 'game')).label;
  assert.match(label, /size unknown/i);
  assert.ok(badge.hint.startsWith('size unknown'),
    'the tile and its tooltip use different words for the same missing fact');
});
