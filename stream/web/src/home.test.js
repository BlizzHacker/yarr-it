import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  domainInText, domainOfRow, groupRowsByDomain, homePlan,
  tileAction, canPlay, setPlayProbe, archiveIdFrom,
  itemFromCard, itemFromDiscover, domainSentence, renderHome,
  backendNotice,
} from './home.js';
import { allDomains, labelFor, verbFor, canonicalDomain } from './schema.js';

// The rows /api/discover actually returns today, taken from the live server.
// Pinned as data rather than described in prose, because the attribution rule
// is only worth anything if it survives the exact strings the server sends.
const LIVE_ROWS = [
  { key: 'trending', title: 'Trending this week', media: ['movie', 'tv'], want: 'video' },
  { key: 'popular-movies', title: 'Popular films', media: ['movie'], want: 'video' },
  { key: 'top-movies', title: 'Top rated films', media: ['movie'], want: 'video' },
  { key: 'popular-tv', title: 'Popular TV', media: ['tv'], want: 'video' },
  { key: 'now-playing', title: 'In cinemas now', media: ['movie'], want: 'video' },
  { key: 'games-top', title: 'Top rated games', media: ['game'], want: 'game' },
  { key: 'games-popular', title: 'Popular games right now', media: ['game'], want: 'game' },
  { key: 'games-classics', title: 'Retro classics', media: ['game'], want: 'game' },
  { key: 'ia-games', title: 'Games you can play right now', media: ['game'], want: 'game' },
  { key: 'ia-arcade', title: 'Arcade cabinets', media: ['game'], want: 'game' },
  { key: 'ia-dos', title: 'MS-DOS classics', media: ['game'], want: 'game' },
  { key: 'ia-flash', title: 'Flash, still playable', media: ['game'], want: 'game' },
  { key: 'ia-books', title: 'Books', media: ['text'], want: 'literature' },
  { key: 'ia-comics', title: 'Golden age comics', media: ['text'], want: 'comic' },
  { key: 'ia-audiobooks', title: 'Audiobooks', media: ['audio'], want: 'literature' },
];

// Every published item carries the target it opens. It used to carry `play: ''`
// and a click ran a search for the item's own display name, which is the defect
// these rows exist to describe -- on 2026-08-08, 171 of the 172 tiles built
// that way reached nothing on the live site.
const rowOf = (spec) => ({
  key: spec.key,
  title: spec.title,
  items: spec.media.map((mediaType, i) => ({
    title: `item ${i}`,
    mediaType,
    play: `https://archive.org/details/item_${spec.key}_${i}`,
  })),
});

// ------------------------------------------------------------- attribution --

test('every row the live server sends today lands on the right domain', () => {
  for (const spec of LIVE_ROWS) {
    assert.equal(domainOfRow(rowOf(spec)), spec.want, `row ${spec.key}`);
  }
});

test('mediaType alone cannot tell a book from a comic, which is why it is last', () => {
  // Both rows are `text`, so anything reading mediaType first files Books and
  // Comics identically -- and `text` resolves to no domain at all, so it files
  // them nowhere. This is the reason the key is consulted before it.
  assert.equal(canonicalDomain('text'), '');
  assert.equal(domainOfRow(rowOf(LIVE_ROWS.find((r) => r.key === 'ia-books'))), 'literature');
  assert.equal(domainOfRow(rowOf(LIVE_ROWS.find((r) => r.key === 'ia-comics'))), 'comic');
});

test('audiobooks go to literature, not to music, even though their mediaType is audio', () => {
  // schema.json files an audiobook under literature on purpose: it is an
  // edition of a book and shares its author. `audio` is a music alias, so
  // trusting mediaType would split one work across two domains.
  assert.equal(canonicalDomain('audio'), 'music');
  assert.equal(domainOfRow(rowOf(LIVE_ROWS.find((r) => r.key === 'ia-audiobooks'))), 'literature');
});

test('a domain the server states outright beats every guess below it', () => {
  const row = { key: 'ia-comics', title: 'Golden age comics', domain: 'literature', items: [{ mediaType: 'text' }] };
  assert.equal(domainOfRow(row), 'literature');
});

test('names are read right to left, so the specific word wins', () => {
  // "movie-songs" is music. Read left to right it would resolve on "movie"
  // and be filed as video -- the qualifier beating the noun.
  assert.equal(domainInText('movie-songs'), 'music');
  assert.equal(domainInText('popular-movies'), 'video');
  assert.equal(domainInText('ia-books'), 'literature');
});

test('a row nothing can be said about is left out rather than guessed at', () => {
  const row = { key: 'zzz', title: 'Staff picks', items: [{ mediaType: 'widget' }] };
  assert.equal(domainOfRow(row), '');
  assert.equal(groupRowsByDomain([row]).size, 0);
});

// ------------------------------------------------------------- dead tiles --
//
// The defect Wade reported: "the buttons need to go direct to a working search
// result - not a wrong search result... not just some appearance of working."
// On the live site every tile on the five TMDB rows and the three IGDB rows
// carried no target at all -- `ids=[]` -- and a click ran a text search for the
// tile's own display name. 171 of 172 reached zero results.
//
// The fix for that is NOT to hide them here. An earlier version of this file
// filtered out every targetless item, and applied during a Prowlarr outage that
// removed five whole shelves whose tiles work the moment the backend answers.
// Which tiles exist is the server's call; this side's job is to not oversell
// what arrives.

test('a tile with no verified target offers to look, and never promises', () => {
  setPlayProbe(null);
  for (const domain of ['video', 'game', 'literature', 'comic']) {
    const item = itemFromDiscover(
      { title: 'Spider-Man: Brand New Day', mediaType: '', state: 'unchecked' },
      domain,
    );
    const action = tileAction(item);
    assert.equal(action.kind, 'search', domain);
    // "Watch" over a click that runs a fuzzy search was the visible half of the
    // original defect. Every domain says the same honest word now.
    assert.equal(action.label, 'Find', domain);
  }
});

test('a row of unchecked tiles is rendered, not deleted', () => {
  const row = {
    key: 'trending',
    title: 'Trending this week',
    items: [
      { title: 'Spider-Man: Brand New Day', mediaType: 'movie', state: 'unchecked' },
      { title: 'Wicked: For Good', mediaType: 'movie', state: 'unchecked' },
    ],
  };
  const rows = groupRowsByDomain([row]).get('video');
  assert.equal(rows.length, 1);
  assert.equal(rows[0].items.length, 2, 'an outage must not delete a shelf');
});

test('a backend that is down is explained, not silently absorbed', async () => {
  await withFakeDom(() => {
    assert.equal(backendNotice(null), null);
    assert.equal(backendNotice({ configured: true, reachable: true }), null);
    // An instance with no indexer is smaller, not broken.
    assert.equal(backendNotice({ configured: false, reachable: false }), null);

    const notice = backendNotice({
      configured: true, reachable: false, detail: 'the torrent indexer is not answering',
    });
    assert.ok(notice, 'a wall of Find tiles with no explanation reads as the site getting worse');
    assert.match(notice.textContent, /not answering/);
  });
});

test('a resolved tile opens its item; it never goes looking for its own name', () => {
  setPlayProbe(null);
  const item = itemFromDiscover(
    { title: 'Super Mario World', mediaType: 'game', play: 'https://archive.org/details/smw-usa#ejs', source: 'archive.org' },
    'game',
  );
  assert.equal(item.uri, 'https://archive.org/details/smw-usa#ejs');
  assert.equal(item.source, 'archive.org');
  const action = tileAction(item);
  assert.notEqual(action.kind, 'search');
  assert.equal(action.kind, 'open');
  assert.equal(action.label, 'Play');
  assert.equal(archiveIdFrom(item.uri), 'smw-usa');
});

test('an empty row is not grouped, so it can never draw an empty shelf', () => {
  assert.equal(groupRowsByDomain([{ key: 'ia-comics', title: 'Comics', items: [] }]).size, 0);
});

// --------------------------------------------------------------------- plan --

test('the plan has exactly one section per domain in the schema, in schema order', () => {
  const plan = homePlan(LIVE_ROWS.map(rowOf));
  assert.deepEqual(plan.map((s) => s.domain), allDomains());
});

test('every section takes its name and its verb from the schema, never from here', () => {
  for (const section of homePlan([])) {
    assert.equal(section.label, labelFor(section.domain));
    assert.equal(section.verb, verbFor(section.domain));
  }
});

test('domains discover covered do not browse; the rest do', () => {
  const plan = homePlan(LIVE_ROWS.map(rowOf));
  const byDomain = Object.fromEntries(plan.map((s) => [s.domain, s]));
  assert.equal(byDomain.video.needsBrowse, false);
  assert.equal(byDomain.game.needsBrowse, false);
  assert.equal(byDomain.literature.needsBrowse, false);
  assert.equal(byDomain.comic.needsBrowse, false);
  // Nothing curated exists for these two, so they are the only ones that cost
  // a search on load.
  assert.equal(byDomain.music.needsBrowse, true);
  assert.equal(byDomain.image.needsBrowse, true);
});

test('with no discover rows at all every domain still gets a section to fill', () => {
  const plan = homePlan([]);
  assert.equal(plan.length, allDomains().length);
  assert.ok(plan.every((s) => s.needsBrowse));
});

// ------------------------------------------------------------- archive ids --

test('an archive identifier is recovered from every shape the API hands out', () => {
  assert.equal(archiveIdFrom('https://archive.org/details/SpaceOdyssey_819'), 'SpaceOdyssey_819');
  assert.equal(archiveIdFrom('https://archive.org/details/Prince_of_Persia#ejs'), 'Prince_of_Persia');
  assert.equal(archiveIdFrom('https://archive.org/details/foo#swf'), 'foo');
  assert.equal(archiveIdFrom('https://archive.org/download/foo/bar.cbz'), 'foo');
  assert.equal(archiveIdFrom('https://archive.org/embed/foo'), 'foo');
  assert.equal(archiveIdFrom('ia:i-have-no-mouth_202202'), 'i-have-no-mouth_202202');
});

test('anything that is not an archive item yields no id rather than a guess', () => {
  assert.equal(archiveIdFrom('magnet:?xt=urn:btih:abc'), '');
  assert.equal(archiveIdFrom('https://example.com/details/foo'), '');
  assert.equal(archiveIdFrom(''), '');
  assert.equal(archiveIdFrom(undefined), '');
});

// ------------------------------------------------------------------ actions --

test('a comic opens the reader, and says Read', () => {
  const item = itemFromDiscover(
    { title: 'Space Odyssey', play: 'https://archive.org/details/SpaceOdyssey_819' }, 'comic');
  const a = tileAction(item);
  assert.equal(a.kind, 'reader');
  assert.equal(a.label, 'Read');
  assert.equal(a.id, 'SpaceOdyssey_819');
  assert.equal(a.verb, 'read');
});

test('a book says Read, not Watch', () => {
  const item = itemFromDiscover(
    { title: 'Amusements', play: 'https://archive.org/details/amusementsinmath16713gut' }, 'literature');
  assert.equal(tileAction(item).label, 'Read');
  assert.equal(tileAction(item).kind, 'reader');
});

test('a picture set is viewed, and goes to the reader without asking for comic pages', () => {
  const item = itemFromDiscover({ title: 'map craft', play: 'https://archive.org/details/amonguscraft_202010' }, 'image');
  const a = tileAction(item);
  assert.equal(a.label, 'View');
  assert.equal(a.verb, 'view');
});

// This test used to require the label "Watch", and that requirement was the
// visible half of the defect: a film tile said "Watch" over a click that ran a
// fuzzy text search, and on the live site 171 of 172 such searches returned
// nothing. The click is unchanged -- it is still a search, and searching is a
// perfectly good thing for a catalogue tile to do -- but the word over it now
// describes what will happen rather than what is hoped for.
test('a film with no target of its own is a name to search for, and says so', () => {
  const item = itemFromDiscover({ title: 'Dune', year: 2021 }, 'video');
  const a = tileAction(item);
  assert.equal(a.kind, 'search');
  assert.equal(a.label, 'Find');
});

test('an audiobook in the Books section is offered as Listen, not as Read', () => {
  // schema.json files audiobooks under literature, whose verb is read. The
  // item itself is audio, and a page reader has no pages for a recording.
  const item = itemFromDiscover(
    { title: 'The Art of War', mediaType: 'audio', play: 'https://archive.org/details/art_of_war_librivox' },
    'literature');
  const a = tileAction(item);
  assert.equal(a.label, 'Listen');
  assert.notEqual(a.kind, 'reader');
});

test('a printed book in the same section stays Read, because its type says nothing', () => {
  const item = itemFromDiscover(
    { title: 'Amusements', mediaType: 'text', play: 'https://archive.org/details/amusementsinmath16713gut' },
    'literature');
  assert.equal(tileAction(item).label, 'Read');
  assert.equal(tileAction(item).kind, 'reader');
});

test('a game with nothing to launch offers to find one rather than a Play that lies', () => {
  // An IGDB row is catalogue metadata: there is no ROM behind it, only a name.
  const item = itemFromDiscover({ title: 'Super Metroid', year: 1994, mediaType: 'game' }, 'game');
  const a = tileAction(item);
  assert.equal(a.kind, 'search');
  assert.equal(a.label, 'Find');
});

test('music says Listen', () => {
  const card = { title: 'Album', kind: 'audio', best: 0, sources: [{ magnet: 'magnet:?xt=urn:btih:a' }] };
  assert.equal(tileAction(itemFromCard(card, 'music')).label, 'Listen');
});

// ---------------------------------------------------------------- play seam --

test('a game this client can actually boot says Play', () => {
  const item = itemFromDiscover(
    { title: 'Prince of Persia', play: 'https://archive.org/details/msdos_Prince_of_Persia_1990#ejs' }, 'game');
  assert.equal(canPlay(item), true);
  assert.equal(tileAction(item).label, 'Play');
});

test('a game that is only a torrent is offered as Open, never as a Play button that lies', () => {
  const card = {
    title: 'Nintendo 64 Full ROM Set', kind: 'game', best: 0,
    sources: [{ magnet: 'magnet:?xt=urn:btih:deadbeef' }],
  };
  const item = itemFromCard(card, 'game');
  assert.equal(canPlay(item), false);
  assert.equal(tileAction(item).label, 'Open');
});

test('a launcher backend can claim a card through setPlayProbe without touching this file', () => {
  try {
    setPlayProbe((item) => item.title === 'Halo');
    assert.equal(canPlay({ title: 'Halo', uri: 'magnet:?xt=urn:btih:x' }), true);
    assert.equal(tileAction({ domain: 'game', title: 'Halo', uri: 'magnet:?xt=urn:btih:x' }).label, 'Play');
    assert.equal(canPlay({ title: 'Doom', uri: 'magnet:?xt=urn:btih:x' }), false);
  } finally {
    setPlayProbe(null);
  }
});

test('a launcher backend that throws answers no, and does not take the row down', () => {
  try {
    setPlayProbe(() => { throw new Error('backend is down'); });
    assert.equal(canPlay({ title: 'Halo', uri: 'https://archive.org/details/x#ejs' }), false);
  } finally {
    setPlayProbe(null);
  }
});

// -------------------------------------------------------------- normalising --

test('a card is opened at the source the detail sheet would have picked as best', () => {
  const card = {
    title: 'Batman', year: 1989, kind: 'comic', best: 1,
    sources: [
      { magnet: 'magnet:?xt=urn:btih:aaa' },
      { magnet: 'https://archive.org/details/batman_issue_1' },
    ],
    art: { poster: 'p.jpg', rating: 7.5 },
  };
  const item = itemFromCard(card, 'comic');
  assert.equal(item.uri, 'https://archive.org/details/batman_issue_1');
  assert.equal(tileAction(item).id, 'batman_issue_1');
});

test('a card with only torrent sources still finds its archive id from the key', () => {
  const card = { title: 'x', kind: 'comic', best: 0, sources: [], key: 'ia:some_comic_2021' };
  assert.equal(tileAction(itemFromCard(card, 'comic')).id, 'some_comic_2021');
});

test('the intro sentence names every domain the schema defines', () => {
  const s = domainSentence();
  for (const d of allDomains()) assert.ok(s.includes(labelFor(d)), `${labelFor(d)} missing from "${s}"`);
});

// ----------------------------------------------------------------- renderHome
//
// No jsdom (project constraint). These fakes cover only the DOM surface
// renderHome touches. What is being pinned is the promise that decides whether
// this page is honest: a section that produces nothing must disappear, because
// an empty shelf claims something exists and then refuses to produce it.

function fakeEl(tag) {
  const node = {
    tagName: tag, className: '', textContent: '', type: '', title: '', tabIndex: 0,
    hidden: false, loading: '', alt: '', src: '', dataset: {}, children: [], parent: null,
    append(...kids) { for (const k of kids) { k.parent = node; node.children.push(k); } },
    replaceChildren(...kids) { node.children = []; node.append(...kids); },
    remove() {
      if (!node.parent) return;
      node.parent.children = node.parent.children.filter((c) => c !== node);
      node.parent = null;
    },
    setAttribute() {}, addEventListener() {}, focus() {}, scrollIntoView() {},
    querySelectorAll() { return []; },
  };
  return node;
}

// await, not return: renderHome finishes in a microtask after its browses
// settle, and restoring `document` the instant fn() handed back its promise
// pulled the DOM out from under the half-drawn page.
async function withFakeDom(fn) {
  const original = globalThis.document;
  globalThis.document = { createElement: fakeEl };
  try {
    return await fn();
  } finally {
    globalThis.document = original;
  }
}

const domainsOf = (host) => host.children.map((c) => c.dataset.domain);

test('a domain whose browse comes back empty leaves no section behind', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('div');
    await renderHome(host, {
      discoverRows: [],
      // Only the comic domain has anything; every other browse is empty.
      browse: (d) => (d === 'comic'
        ? [{ title: 'Space Odyssey', kind: 'comic', best: 0, sources: [{ magnet: 'https://archive.org/details/SpaceOdyssey_819' }] }]
        : []),
      handlers: { onActivate() {} },
    });
    assert.deepEqual(domainsOf(host), ['comic']);
  });
});

test('Books fallback keeps printed books and adds audiobooks beneath them', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('div');
    await renderHome(host, {
      discoverRows: [],
      browse: (d) => (d === 'literature' ? {
        shelves: [
          {
            title: 'Public domain classics',
            items: [{
              title: 'Pride and Prejudice', mediaType: 'text',
              play: 'https://archive.org/details/prideandprejudice',
            }],
          },
          {
            title: 'Audiobooks',
            mediaType: 'audio',
            items: [{
              title: 'Alice in Wonderland', mediaType: 'audio',
              play: 'https://archive.org/details/alice_librivox',
            }],
          },
        ],
      } : []),
      handlers: { onActivate() {} },
    });
    const books = host.children.find((n) => n.dataset.domain === 'literature');
    assert.ok(books, 'Books domain disappeared');
    const text = [];
    const walk = (n) => { text.push(n.textContent); for (const c of n.children) walk(c); };
    walk(books);
    assert.ok(text.includes('Public domain classics'), 'printed Books shelf disappeared');
    assert.ok(text.includes('Read'), 'printed Books shelf lost its Read action');
    assert.ok(text.includes('Audiobooks'), 'Audiobooks shelf disappeared');
    assert.ok(text.includes('Listen'), 'Audiobooks shelf lost its Listen action');
  });
});

test('a browse that fails removes its section rather than leaving a heading over nothing', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('div');
    await renderHome(host, {
      discoverRows: [],
      browse: () => Promise.reject(new Error('indexers timed out')),
      handlers: { onActivate() {} },
    });
    assert.deepEqual(domainsOf(host), []);
  });
});

test('a domain discover already covered is drawn without costing a browse', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('div');
    const asked = [];
    await renderHome(host, {
      discoverRows: [rowOf(LIVE_ROWS.find((r) => r.key === 'ia-comics'))],
      browse: (d) => { asked.push(d); return []; },
      handlers: { onActivate() {} },
    });
    assert.deepEqual(domainsOf(host), ['comic']);
    assert.ok(!asked.includes('comic'), 'comic was curated and must not be searched for');
    assert.deepEqual(asked.sort(), allDomains().filter((d) => d !== 'comic').sort());
  });
});

test('the resume shelf is kept above the domain sections', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('div');
    const resume = fakeEl('section');
    resume.className = 'shelf resume';
    await renderHome(host, {
      discoverRows: [rowOf(LIVE_ROWS.find((r) => r.key === 'ia-books'))],
      browse: () => [],
      resume,
      handlers: { onActivate() {} },
    });
    assert.equal(host.children[0], resume);
    assert.deepEqual(domainsOf(host).filter(Boolean), ['literature']);
  });
});

// The notice sits above the shelves, so the explanation is read before the row
// it explains rather than found underneath it.
test('the backend notice is drawn above the sections it explains', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('div');
    await renderHome(host, {
      discoverRows: [rowOf(LIVE_ROWS.find((r) => r.key === 'ia-comics'))],
      indexer: { configured: true, reachable: false, detail: 'indexer down' },
      browse: () => [],
      handlers: { onActivate() {} },
    });
    assert.equal(host.children[0].className, 'backend-notice');
    assert.ok(domainsOf(host).includes('comic'), 'the shelves still render');
  });
});

test('a healthy backend adds nothing to the page', async () => {
  await withFakeDom(async () => {
    const host = fakeEl('div');
    await renderHome(host, {
      discoverRows: [rowOf(LIVE_ROWS.find((r) => r.key === 'ia-comics'))],
      indexer: { configured: true, reachable: true },
      browse: () => [],
      handlers: { onActivate() {} },
    });
    assert.ok(!host.children.some((c) => c.className === 'backend-notice'));
  });
});

// --------------------------------------------------- browse rows that leave

// A category page draws on more than one catalogue now: archive.org items,
// which this client opens itself, and Vimm's Lair entries, which open on
// vimm.net. The verb has to follow the destination.
//
// This is the same defect the TMDB tiles had -- a word describing an intention
// instead of an outcome -- and it arrives here through a different door:
// itemFromDiscover used to drop `external` on the floor, because until now
// nothing on a browse page could leave the site.
test('a browse tile from another site is labelled with where it goes', () => {
  setPlayProbe(null);
  const item = itemFromDiscover({
    title: 'Super Mario World',
    mediaType: 'game',
    play: 'https://vimm.net/vault/2',
    source: "Vimm's Lair",
    external: { name: "Vimm's Lair", short: 'Vimm', host: 'vimm.net', page: 'https://vimm.net/vault/2' },
  }, 'game');

  assert.equal(item.external?.short, 'Vimm', 'external was dropped in normalisation');
  const action = tileAction(item);
  assert.equal(action.kind, 'open');
  // Not "Play": nothing here plays it. Not "Open": that is a shrug over a link
  // to somebody else's website. The arrow is half the message.
  assert.equal(action.label, 'Vimm ↗');
  assert.equal(action.external.host, 'vimm.net');
});

// The other half of the same rule: an archive.org tile on that same mixed page
// must keep saying "Play", or the fix would relabel every game on the site.
test('an archive.org tile on a mixed page still says Play', () => {
  setPlayProbe(null);
  const item = itemFromDiscover({
    title: 'Chrono Trigger',
    mediaType: 'game',
    play: 'https://archive.org/details/chrono-trigger#ejs',
    source: 'archive.org',
  }, 'game');

  assert.equal(item.external, null);
  assert.equal(tileAction(item).label, 'Play');
});
