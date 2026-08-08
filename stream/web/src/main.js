import { StreamEngine, classify, needsWebCodecs } from './engine.js';
import { createRegistry, makeSource, makeCollection, isCollection } from './source.js';
import { createTorrentResolver, normalizeMagnet } from './resolvers/torrent.js';
import { urlResolver } from './resolvers/url.js';
import { embedResolver } from './resolvers/embed.js';
import { playlistResolver } from './resolvers/playlist.js';
import { flashResolver } from './resolvers/flash.js';
import { gameResolver } from './resolvers/game.js';
import { archiveResolver } from './resolvers/archive.js';
import { renderPlayable, detachAll } from './player.js';
import { renderLibrary } from './library.js';
import { whoAmI, displayName, signInURL, signOutURL, vpnGuidance, egressStatus } from './account.js';
import { createAddonAPI, renderAddons, normaliseAddonURL, moveAddon } from './addons.js';
import { keepFitted } from './embedfit.js';
import {
  getContinueWatching, trackProgress, watchedFraction,
  getLibrary, addToLibrary, removeFromLibrary, keyFor,
} from './shelf.js';
import { attachSubtitles } from './subtitles.js';
import { PlaybackError } from './failures.js';
import { apiFetch, getServer, setServer, probeServer } from './server.js';
import { renderHome, itemFromCard, tileAction, domainSentence } from './home.js';
import { openReader } from './reader.js';
import {
  SERVICE_TYPES, allServiceTypes, createServiceStore, probeService,
  routeAdvice, describeService, healthLabel, normaliseServiceURL,
} from './services.js';
import { TRANSPORT, extensionAvailable } from './transport.js';

const $ = (s) => document.querySelector(s);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

const state = {
  cards: [],
  facets: null,
  query: '',
  engine: null,
  registry: null,
  playable: null,
  // Bumped at the start of every play() call. A resolve() that finishes
  // after a newer play() has started is stale -- comparing against the
  // counter it captured is how play() tells "still the one the user last
  // clicked" from "lost the race".
  resolveGen: 0,
  active: null,
  // The open page reader, if any. Held so a second open closes the first
  // rather than leaving its keydown listener on the document.
  reader: null,
  filters: {
    seeders: 1, minSize: '', maxSize: '',
    quality: new Set(), codec: new Set(), groups: new Set(),
    webSafe: false, adult: false, sort: 'seeders',
    // '' means both. A hosted backup always plays; a torrent depends on who
    // is seeding, so which you want is a real question.
    source: '',
  },
};

// ------------------------------------------------------------------ search --

function filterParams() {
  const f = state.filters;
  const p = new URLSearchParams({ q: state.query, sort: f.sort });
  if (f.seeders) p.set('minSeeders', String(f.seeders));
  if (f.minSize) p.set('minSizeMB', String(f.minSize));
  if (f.maxSize) p.set('maxSizeMB', String(f.maxSize));
  if (f.quality.size) p.set('quality', [...f.quality].join(','));
  if (f.codec.size) p.set('codec', [...f.codec].join(','));
  if (f.webSafe) p.set('webSafe', '1');
  if (f.source) p.set('source', f.source);
  if (f.groups.size) p.set('groups', [...f.groups].join(','));
  // Adult results are excluded server-side unless explicitly requested.
  if (f.adult) p.set('adult', '1');
  return p;
}

async function search({ showSpinner = true } = {}) {
  // A category with no words is a valid search: "show me games".
  if (!state.query && !state.filters.groups.size) return;
  $('#intro').hidden = true;
  $('#discover').hidden = true;
  $('#get').hidden = true;
  // A playlist Collection browsed earlier leaves #library visible (it's
  // only ever shown, never hidden, by renderLibrary). #player is a
  // full-viewport overlay, so that stays invisible right up until the
  // player closes -- then the old channel list resurfaces underneath a
  // brand new, unrelated search. Every fresh search must start clean.
  $('#library').hidden = true;
  if (showSpinner) {
    $('#status').textContent = 'Searching every indexer…';
    $('#status').hidden = false;
    showSkeletons();
  }

  try {
    const res = await apiFetch(`/api/search?${filterParams()}`);
    const data = await res.json().catch(() => ({}));

    if (!res.ok) {
      // A 504 means the indexers were slow, not that the query was bad — offer
      // a retry rather than a dead end.
      showRetry(data.error || `Search failed (${res.status}).`, () => search());
      return;
    }

    state.cards = data.cards || [];
    state.facets = data.facets || null;
    $('#status').hidden = true;
    renderFilters();
    renderResults(data);
  } catch (err) {
    showRetry(`Could not reach the search service: ${err.message}`, () => search());
  }
}

/** Re-filter without re-querying the indexers; the server filters its cache. */
const refilter = debounce(() => search({ showSpinner: false }), 250);

function showRetry(message, onRetry) {
  const s = $('#status');
  s.replaceChildren(document.createTextNode(message + ' '));
  const b = el('button', 'retry', 'Try again');
  b.type = 'button';
  b.addEventListener('click', () => { s.replaceChildren(); onRetry(); });
  s.append(b);
  s.hidden = false;
}

function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
}

// ----------------------------------------------------------------- filters --

function renderFilters() {
  const f = state.facets;
  // The filter bar stays visible from the first paint. Hiding it until results
  // arrive means the one moment you would want to narrow a search -- before
  // running it -- is the one moment the controls are missing.
  $('#filters').hidden = false;
  $('#filters').classList.toggle('awaiting', !f);

  // The categories are drawn from the start, with counts filled in once a
  // search has produced them. Rendering them only from facets meant they did
  // not exist until after a search had already run, so a category could only
  // ever narrow results you had -- never ask for a category in the first
  // place, which is the first thing anyone tries.
  groupRow($('#f-groups'), f?.groups ?? ALL_GROUPS.map((value) => ({ value })),
    state.filters.groups);
  if (!f) return;
  $('#f-adult').classList.toggle('on', state.filters.adult);
  $('#f-adult').textContent = f.adultCount ? `18+ ${f.adultCount}` : '18+';
  chipRow($('#f-quality'), f.qualities, state.filters.quality);
  chipRow($('#f-codec'), f.codecs, state.filters.codec);
  $('#f-websafe').classList.toggle('on', state.filters.webSafe);
  $('#f-instant').classList.toggle('on', state.filters.source === 'instant');
  $('#f-swarm').classList.toggle('on', state.filters.source === 'swarm');
  $('#f-instant').textContent = f.instantCount ? `Backups ${f.instantCount}` : 'Backups';
  $('#f-swarm').textContent = f.swarmCount ? `Torrents ${f.swarmCount}` : 'Torrents';
}

// Human labels for the Newznab buckets the API reports.
// Every category the indexers bucket into, so the chips exist before any
// search has run. 'adult' is deliberately absent -- it has its own explicit
// 18+ toggle and must never be enabled by ticking a category box.
const ALL_GROUPS = ['movies', 'tv', 'anime', 'games', 'comics', 'music', 'books', 'apps', 'other'];

const GROUP_LABELS = {
  movies: 'Movies', tv: 'TV', anime: 'Anime', music: 'Music',
  games: 'Games', comics: 'Comics', apps: 'Apps', books: 'Books', other: 'Other',
};

/**
 * Category checkboxes, in the spirit of a torrent site's category bar.
 * 'adult' is excluded here because it has its own explicit 18+ toggle — it
 * should never be something you enable by accident while ticking boxes.
 */
function groupRow(host, values, selected) {
  host.replaceChildren();
  for (const { value, count } of (values || [])) {
    if (value === 'adult') continue;
    const c = el('span', 'chip', GROUP_LABELS[value] || value);
    // Before a search there is nothing to count yet.
    if (count != null) c.append(el('span', 'cnt', String(count)));
    if (selected.has(value)) c.classList.add('on');
    c.addEventListener('click', () => {
      selected.has(value) ? selected.delete(value) : selected.add(value);
      c.classList.toggle('on');
      // With no words typed, picking a category IS the search -- "show me
      // games" -- so it runs one rather than re-filtering an empty result set.
      // A chip is a choice, not a command. Firing a search the instant one is
      // ticked means the page runs off and fetches something while you are
      // still deciding what you want -- and ticking two categories fired two
      // searches, the first of them wasted.
      //
      // So: if results are already on screen, narrowing them is instant and
      // free (the server filters its cache). If there are none, the chip just
      // arms the search and the Search button runs it.
      if (state.cards.length) refilter();
      else armSearch();
    });
    host.append(c);
  }
}

function chipRow(host, values, selected) {
  host.replaceChildren();
  for (const { value, count } of (values || []).slice(0, 7)) {
    const c = el('span', 'chip', value);
    c.title = `${count} source${count === 1 ? '' : 's'}`;
    if (selected.has(value.toLowerCase())) c.classList.add('on');
    c.addEventListener('click', () => {
      const k = value.toLowerCase();
      selected.has(k) ? selected.delete(k) : selected.add(k);
      c.classList.toggle('on');
      refilter();
    });
    host.append(c);
  }
}

/** Placeholder tiles so the grid has shape while a cold search runs. */
function showSkeletons(n = 12) {
  const grid = $('#grid');
  grid.replaceChildren();
  for (let i = 0; i < n; i++) {
    const t = el('div', 'tile skel');
    t.append(el('div', 'poster'));
    t.append(el('div', 'tname-sk'));
    t.append(el('div', 'tmeta-sk'));
    grid.append(t);
  }
}

// ----------------------------------------------------------------- results --

function renderResults(data) {
  const grid = $('#grid');
  grid.replaceChildren();

  const bar = $('#resultbar');
  bar.replaceChildren();
  bar.hidden = false;
  bar.append(el('b', null, `${state.cards.length}`));
  // A category browse has no query, so naming one renders as empty quotes.
  const picked = [...state.filters.groups].map((g) => GROUP_LABELS[g] || g);
  const what = state.query
    ? `for “${state.query}”`
    : (picked.length ? `in ${picked.join(' + ')}` : '');
  bar.append(el('span', null,
    `result${state.cards.length === 1 ? '' : 's'} ${what}`.trim() +
    (data.total && data.total !== state.cards.length ? ` · ${data.total} before filters` : '')));

  if (data.stale) {
    const n = el('p', 'stale-note',
      'Indexers are slow right now — showing the last known results. Search again for fresh ones.');
    bar.after(n);
  }

  if (!state.cards.length) {
    $('#status').textContent = 'Nothing matched. Try loosening the filters.';
    $('#status').hidden = false;
    return;
  }
  for (const c of state.cards) grid.append(tile(c));
}

function tile(card) {
  const t = el('button', 'tile');
  t.type = 'button';

  const p = el('div', 'poster');
  // The placeholder is drawn whether or not there is a poster to try, and the
  // image is laid over it. Half these results are archive.org items whose
  // thumbnail service answers with an error rather than a picture, and a
  // broken-image glyph in a 2:3 box is the worst way to say "no cover".
  p.append(el('div', 'noart', card.title));
  if (card.art?.poster) {
    const img = el('img');
    img.loading = 'lazy';
    img.alt = '';
    img.addEventListener('error', () => { img.remove(); });
    img.src = card.art.poster;
    p.append(img);
  }

  // A seeder count is a guess at whether something will play. For a result
  // served by a host that is always up there is nothing to guess, so showing
  // "0▲" there would read as broken when it is the most reliable card on the
  // page.
  if (card.instant) {
    p.append(el('span', 'badge instant', 'INSTANT'));
  } else {
    p.append(el('span', card.seeders > 0 ? 'badge' : 'badge dead', `${card.seeders}▲`));
  }
  if (card.art?.rating) p.append(el('span', 'rating', card.art.rating.toFixed(1)));
  const bq = card.platform || card.sources[card.best]?.quality;
  if (bq) p.append(el('span', 'best-q', bq));
  // What this card is for, in its own domain's word. A search for "batman"
  // returns films, comics and games together, and until now every one of them
  // was offered with the same silent "click me".
  p.append(el('span', `verb verb-${card.kind || 'other'}`, tileAction(itemFromCard(card, card.kind)).label));
  t.append(p);

  t.append(el('div', 'tname', card.title));
  const bits = [];
  if (card.year) bits.push(card.year);
  if (card.isSeries) bits.push(`S${card.season}E${card.episode}`);
  if (card.instant) {
    bits.push('plays instantly');
  } else {
    bits.push(`${card.sources.length} source${card.sources.length === 1 ? '' : 's'}`);
  }
  t.append(el('div', 'tmeta', bits.join(' · ')));

  t.addEventListener('click', () => openCard(card));
  return t;
}

/**
 * Open a search result the way its own domain says it should open.
 *
 * A comic and a film both arrive here as a card, and both used to land in the
 * detail sheet, whose only offer is a source to stream. For anything you read
 * or look at there is nothing to stream: the source is a details page on
 * archive.org, and streaming it meant an iframe of somebody else's website.
 */
function openCard(card) {
  const action = tileAction(itemFromCard(card, card.kind));
  if (action.kind === 'reader') {
    openReaderFor(action.id, card.title, action.verb);
    return;
  }
  openDetail(card);
}

// ---------------------------------------------------------------- discover --

/**
 * Browsable landing rows from TMDB.
 *
 * These are catalogue entries, not torrents — nothing here is indexed or
 * hosted. Clicking one runs an ordinary search for its title, which is where
 * any actual sources come from.
 */
/**
 * Put the landing page back after the last category is deselected.
 *
 * Deselecting into an empty search would otherwise leave the results grid
 * showing whatever the previous category returned, with nothing selected to
 * explain it.
 */
function restoreLanding() {
  state.cards = [];
  state.facets = null;
  $('#grid').replaceChildren();
  $('#resultbar').hidden = true;
  $('#status').hidden = true;
  $('#library').hidden = true;
  $('#intro').hidden = false;
  $('#discover').hidden = false;
  $('#get').hidden = false;
  renderFilters();
}

/**
 * Ask for everything one domain has, with no query.
 *
 * This is the fallback for a domain /api/discover has no curated row for --
 * today Music and Images. It is not the first choice because a cold browse is
 * a full indexer fan-out: measured against the live server, 20-45s cold and
 * about 0.3s once the 45-minute cache has it. Discover, by contrast, is
 * cached for three hours and answers immediately.
 */
async function browseDomain(domain) {
  // minSeeders=0 because a hosted archive.org result has no swarm at all, and
  // the default of 1 would drop the only results these rows have.
  const res = await apiFetch(`/api/search?kind=${encodeURIComponent(domain)}&minSeeders=0`);
  if (!res.ok) throw new Error(`browse ${domain}: ${res.status}`);
  const data = await res.json();
  // A rail is a rail, not a result set; the rest is a search away.
  return (data.cards || []).slice(0, 24);
}

/**
 * The landing page: one section per domain in schema.json, in that order.
 *
 * Everything about which sections exist, what they are called and what verb
 * their cards carry comes from the vocabulary — see home.js. This function
 * only supplies the two things a module of pure structure cannot have: where
 * the data comes from, and what a click does.
 */
async function loadHome() {
  const host = $('#discover');
  let rows = [];
  try {
    const res = await apiFetch('/api/discover');
    if (res.ok) rows = (await res.json()).rows || [];
  } catch {
    // No curated rows is survivable: every domain simply falls back to a
    // browse, which is slower but is still a landing page.
  }

  // What you already started comes first. Anyone signed out gets an empty
  // list and no row, which is the correct amount of nagging.
  let resume = null;
  try {
    const started = await getContinueWatching();
    if (started.length) resume = resumeShelf(started);
  } catch {
    /* the resume row is a bonus; the catalogue still renders without it */
  }

  try {
    await renderHome(host, {
      discoverRows: rows,
      browse: browseDomain,
      resume,
      handlers: {
        onActivate(item, action) {
          if (action.kind === 'reader') {
            openReaderFor(action.id, item.title, action.verb);
            return;
          }
          if (action.kind === 'open') {
            // A card came from a search and has sources to choose between; a
            // discover item with a play target IS the thing and opens directly.
            if (item.card) openCard(item.card);
            else play({ title: item.title, year: item.year || 0 },
              { uri: item.uri, title: item.title });
            return;
          }
          // A catalogue entry is a name to go looking for.
          const q = item.year ? `${item.title} ${item.year}` : item.title;
          $('#q').value = q;
          state.query = q;
          history.replaceState(null, '', `?q=${encodeURIComponent(q)}`);
          search();
        },
      },
    });
  } catch {
    /* discovery is a nicety; a failure just leaves the intro copy in place */
  }
}

/** The Continue Watching row, newest first. */
function resumeShelf(items) {
  const shelf = el('section', 'shelf');
  shelf.append(el('h3', null, 'Continue watching'));
  const rail = el('div', 'rail');
  for (const p of items) rail.append(resumeTile(p));
  shelf.append(rail);
  return shelf;
}

function resumeTile(p) {
  const t = el('button', 'tile');
  t.type = 'button';
  const label = p.title || p.key;
  t.title = `Resume ${label}`;

  const poster = el('div', 'poster');
  if (p.poster) {
    const img = el('img');
    img.loading = 'lazy';
    img.alt = label;
    img.src = p.poster;
    poster.append(img);
  } else {
    poster.append(el('div', 'noart', label));
  }

  // A bar across the artwork says how far in you are without needing a number.
  const bar = el('div', 'progress');
  const fill = el('div', 'progress-fill');
  fill.style.width = `${Math.round(watchedFraction(p) * 100)}%`;
  bar.append(fill);
  poster.append(bar);
  t.append(poster);

  t.append(el('div', 'tname', label));
  const mins = Math.max(0, Math.round((p.duration - p.position) / 60));
  t.append(el('div', 'tmeta', p.duration ? `${mins} min left` : 'Resume'));

  t.addEventListener('click', () => {
    const q = p.title || p.key;
    $('#q').value = q;
    state.query = q;
    history.replaceState(null, '', `?q=${encodeURIComponent(q)}`);
    search();
  });
  return t;
}

// ------------------------------------------------------------------ reader --

/**
 * The overlay that shows a comic, a book or a picture set as pages.
 *
 * Kept beside the player rather than inside it: they share nothing but a
 * z-index. The player streams bytes into a media element; this one steps
 * through JPEGs from /api/pages and never touches the torrent engine.
 */
function readerElements() {
  return {
    root: $('#reader'),
    title: $('#reader-title'),
    count: $('#reader-count'),
    stage: $('#reader-stage'),
    img: $('#reader-page'),
    embed: $('#reader-embed'),
    prev: $('#reader-prev'),
    next: $('#reader-next'),
    status: $('#reader-status'),
  };
}

function openReaderFor(id, title, verb) {
  state.reader?.close();
  document.body.style.overflow = 'hidden';
  state.reader = openReader({
    id,
    title,
    verb,
    els: readerElements(),
    apiFetch,
    onClose: () => {
      state.reader = null;
      // A detail sheet left open underneath still wants the page frozen.
      if ($('#detail').hidden) document.body.style.overflow = '';
    },
  });
}

function closeReader() {
  state.reader?.close();
  state.reader = null;
}

// ------------------------------------------------------------------ detail --

function openDetail(card) {
  state.active = card;
  $('#detail').hidden = false;
  document.body.style.overflow = 'hidden';

  $('#d-hero').style.backgroundImage = card.art?.backdrop ? `url("${card.art.backdrop}")` : '';
  $('#d-poster').style.backgroundImage = card.art?.poster ? `url("${card.art.poster}")` : '';
  $('#d-title').textContent = card.title;

  const sub = [];
  if (card.year) sub.push(card.year);
  if (card.isSeries) sub.push(`Season ${card.season}, Episode ${card.episode}`);
  if (card.art?.rating) sub.push(`★ ${card.art.rating.toFixed(1)}`);
  if (card.platform) sub.push(card.platform);
  sub.push(card.instant ? 'Plays instantly — no download' : `${card.seeders} seeders`);
  $('#d-sub').textContent = sub.join('  ·  ');

  $('#d-overview').textContent = card.art?.overview || '';
  $('#d-overview').hidden = !card.art?.overview;

  const g = $('#d-genres');
  g.replaceChildren();
  for (const name of card.art?.genres || []) g.append(el('span', 'chip', name));

  $('#d-srch').textContent = card.instant
    ? 'Hosted by archive.org — press play'
    : `${card.sources.length} source${card.sources.length === 1 ? '' : 's'} — pick one to stream`;

  const list = $('#d-sources');
  list.replaceChildren();
  card.sources.forEach((s, i) => list.append(sourceRow(card, s, i === card.best)));

  renderSaveButton(card);
}

/**
 * The save control, drawn only for a viewer who has somewhere to save to.
 *
 * Membership is read from the library rather than remembered locally, so the
 * button tells the truth after the same title was saved on another device.
 */
async function renderSaveButton(card) {
  const btn = $('#d-save');
  btn.hidden = true;

  let saved;
  try {
    const items = await getLibrary();
    saved = items.some((it) => it.key === keyFor(card));
  } catch {
    return; // signed out, or the shelf is unreachable: draw nothing
  }
  if (state.active !== card) return; // a newer detail opened while we waited

  const paint = () => {
    btn.dataset.saved = saved ? '1' : '0';
    btn.textContent = saved ? '✓ In your library' : '+ Save to library';
  };
  paint();
  btn.hidden = false;

  btn.onclick = async () => {
    btn.disabled = true;
    const next = !saved;
    try {
      await (next ? addToLibrary(card) : removeFromLibrary(card));
      saved = next;
      paint();
    } catch {
      // Leave the button showing what the server still believes.
      paint();
    } finally {
      btn.disabled = false;
    }
  };
}

function sourceRow(card, s, isBest) {
  const row = el('button', isBest ? 'source best' : 'source');
  row.type = 'button';

  const l = el('div', 'sl');
  // Quality, codec, size and seeders are all torrent vocabulary. On a hosted
  // game every one of them renders as "unknown" or "0", which reads as a
  // broken row rather than the most reliable one on the page.
  if (card.instant) {
    l.append(el('span', 'q', 'PLAY'));
    l.append(el('span', 'tag ok', s.source || 'Game'));
  } else {
    l.append(el('span', 'q', s.quality || '—'));
    const codec = el('span', s.webSafe ? 'tag ok' : 'tag warn', s.codec || 'unknown');
    codec.title = s.webSafe
      ? 'Plays directly in your browser'
      : 'Needs hardware decode on your device — no server transcoding';
    l.append(codec);
    if (s.source) l.append(el('span', 'tag', s.source));
  }
  l.append(el('span', 'name', s.title));
  row.append(l);

  const r = el('div', 'sr');
  if (card.instant) {
    r.append(el('span', 'seeds', 'INSTANT'));
  } else {
    r.append(el('span', null, s.sizeHuman));
    r.append(el('span', s.seeders > 0 ? 'seeds' : 'seeds dead', `${s.seeders}▲`));
  }
  r.append(el('span', null, s.indexer));
  row.append(r);

  row.addEventListener('click', () => play(card, s));
  return row;
}

function closeDetail() {
  $('#detail').hidden = true;
  document.body.style.overflow = '';
}

// ------------------------------------------------------------------ player --


/**
 * Scale a fixed-size third-party embed up to fill the stage.
 *
 * The Internet Archive's emulator draws into a canvas that is 300x150 and stays
 * 300x150 at every viewport size -- measured identical at 640x480, 1482x415 and
 * 1600x900. Left alone that is about 3% of a full-screen player, jammed against
 * the left edge. Their page is cross-origin so no stylesheet of ours reaches
 * inside it, but the iframe ELEMENT is ours, and a transform on it scales
 * everything it contains.
 */
function fitEmbedToStage(playable, el) {
  state.stopFit?.();
  state.stopFit = null;

  const wrap = document.querySelector('#embed-fit');
  if (!wrap) return;
  if (playable.render !== 'embed') {
    wrap.hidden = true;
    return;
  }
  wrap.hidden = false;
  const stage = wrap.closest('.stage');
  if (stage) state.stopFit = keepFitted(wrap, el, stage);
}

function playerElements() {
  return {
    video: $('#video'),
    audio: $('#audio'),
    image: $('#image'),
    embed: $('#embed'),
    canvas: $('#canvas'), // Ruffle (.swf) and EmulatorJS (ROMs) mount here
  };
}

function buildRegistry() {
  if (!state.engine) state.engine = new StreamEngine({ onStats: renderStats });
  window.__engine = state.engine; // diagnostics
  const registry = createRegistry()
    .register(createTorrentResolver({ engine: state.engine, classify }))
    .register(embedResolver)      // before url: a YouTube link is also an http URL
    .register(archiveResolver)    // before url/game: archive.org runs its own player
    .register(flashResolver)      // before url: a .swf is also an http URL
    .register(gameResolver)       // before url: a .nes/.smc is also an http URL
    .register(playlistResolver)   // before url: .m3u8 is also an http URL
    .register(urlResolver);
  window.__registry = registry; // diagnostics
  return registry;
}

/**
 * Best-effort filename for a resolved Playable, so needsWebCodecs can look at
 * the actual container extension instead of a magnet link.
 *
 * WebTorrent's file.streamURL (what the torrent resolver sets as `src`) is
 * `<sw-scope>/<infoHash>/<encoded file path>` -- the final path segment is
 * the real filename, which is what this recovers. The url/playlist resolvers
 * set `src` to the source URI itself, which is usually the same shape.
 *
 * This is not reliable in every case, and there is nothing to fall back to
 * that fixes that: source.js's makePlayable (render/src/mime/tier/cleanup)
 * does not carry the original filename as its own field, so when `src` is
 * not a filename-shaped URL -- e.g. the no-service-worker torrent fallback in
 * torrent.js's resolve(), which plays from a bare `blob:` URL -- there is no
 * filename to recover at all. In that case this just returns `src` itself;
 * needsWebCodecs will find no matching extension and stay silent rather than
 * guess. That's a known gap, not a bug: it only affects the rare
 * service-worker-unavailable fallback path, not normal torrent playback.
 */
function playableFilename(playable) {
  try {
    const { pathname } = new URL(playable.src, location.href);
    const last = pathname.split('/').pop();
    if (last) return decodeURIComponent(last);
  } catch {
    /* src isn't a parseable URL (e.g. an opaque blob: id) -- fall through */
  }
  return playable.src;
}

/**
 * Resolve any source and render whatever comes back. `src.magnet` is still
 * honoured so existing search results keep working unchanged.
 *
 * Two guards protect this against the async gap between "resolve() called"
 * and "resolve() settles":
 *
 *  - The outgoing Playable's cleanup() is called up front, before anything
 *    else, so a torrent's swarm (or any other resolver's held resource) is
 *    always released the instant a new source is picked -- not just when
 *    the player is closed, and not left to whatever side effect the next
 *    resolver happens to have (StreamEngine.add() tearing down the previous
 *    torrent is one such side effect, not a substitute for this).
 *  - `resolveGen` guards overlapping calls: if the user picks a second
 *    source before the first has finished resolving, only the resolution
 *    matching the *latest* play() call is allowed to touch the DOM or
 *    state.playable. A resolution that loses the race still gets its
 *    Playable cleaned up so it can't leak in the background.
 */
async function play(card, src) {
  const gen = ++state.resolveGen;

  try {
    state.playable?.cleanup();
  } catch (err) {
    console.warn('[player] cleanup of outgoing playable failed:', err?.message || err);
  }
  state.playable = null;

  $('#library').hidden = true;
  $('#player').hidden = false;
  $('#player-title').textContent = card.title + (card.year ? ` (${card.year})` : '');
  $('#player-sub').textContent = src.title ?? '';
  setPlayerStatus('Resolving…');

  const els = playerElements();
  detachAll(els);

  if (!state.registry) state.registry = buildRegistry();

  const uri = src.magnet ?? src.uri;
  try {
    const out = await state.registry.resolve(makeSource({ kind: 'auto', uri }));

    if (gen !== state.resolveGen) {
      // A newer play() call has since taken over. Never touch the DOM or
      // state.playable with a stale result -- but still release whatever
      // this resolution acquired (a torrent's cleanup destroys its swarm)
      // so the loser of the race doesn't leak.
      if (!isCollection(out)) {
        try {
          out.cleanup?.();
        } catch (err) {
          console.warn('[player] cleanup of abandoned playable failed:', err?.message || err);
        }
      }
      return;
    }

    if (isCollection(out)) {
      $('#player').hidden = true;
      renderLibrary(out, {
        mount: $('#library'),
        onPick: (picked) => play({ title: picked.meta.title || picked.uri }, { uri: picked.uri }),
      });
      return;
    }

    $('#library').hidden = true;
    if (needsWebCodecs(playableFilename(out))) {
      setPlayerStatus('Unusual container — if this stalls, pick an MP4 source.');
    }
    const el = renderPlayable(out, els);
    state.playable = out;
    fitEmbedToStage(out, el);

    // Remember where this viewer gets to, so the same title resumes on any
    // other device. Only real media has a position; an image or an emulator
    // has nothing to record.
    state.stopTracking?.();
    state.stopTracking = (out.render === 'video' || out.render === 'audio')
      ? trackProgress(el, card)
      : null;

    // Subtitles that shipped with the release. Attached after playback has
    // started so fetching them never delays the picture, and failure is
    // silent -- no subtitles is the normal case, not an error.
    if (out.subtitles?.length) {
      attachSubtitles(el, out.subtitles, (t) => t.load())
        .then((tracks) => {
          if (tracks.length) {
            setPlayerStatus(`${tracks.length} subtitle track${tracks.length === 1 ? '' : 's'} available — use the player's captions menu`);
            setTimeout(() => setPlayerStatus(''), 6000);
          }
        })
        .catch(() => {});
    }
    el.addEventListener('playing', () => setPlayerStatus(''), { once: true });
    // Only <video>/<audio> fire a 'playing' event. An image, an iframe embed and
    // a canvas player (Ruffle/EmulatorJS) never will, so their status has to be
    // cleared here or the overlay sits on "Resolving…" forever.
    if (out.render !== 'video' && out.render !== 'audio') setPlayerStatus('');
  } catch (err) {
    if (gen !== state.resolveGen) return;
    const why = err instanceof PlaybackError ? err.message : `Could not start: ${err.message}`;
    setPlayerStatus(why);
  }
}

function setPlayerStatus(t) {
  const n = $('#player-status');
  n.textContent = t;
  n.hidden = !t;
}

function fmtBytes(b) {
  if (!b) return '0 B';
  const u = ['B', 'KiB', 'MiB', 'GiB'];
  const i = Math.min(Math.floor(Math.log(b) / Math.log(1024)), u.length - 1);
  return `${(b / 1024 ** i).toFixed(1)} ${u[i]}`;
}

function renderStats(s) {
  $('#stat-peers').textContent = s.peers;
  $('#stat-bridge').textContent = s.bridgePeers;
  $('#stat-speed').textContent = `${fmtBytes(s.downloadSpeed)}/s`;
  $('#stat-done').textContent = `${(s.progress * 100).toFixed(1)}%`;
  $('#progress').style.width = `${Math.min(100, s.progress * 100)}%`;
}

function closePlayer() {
  state.playable?.cleanup();
  state.playable = null;
  $('#player').hidden = true;
  $('#library').hidden = true;
  // detachAll pauses/loads every real media element (video, audio) and clears
  // src on all of them, not just video+image -- with the audio and embed
  // elements now in play, leaving those untouched would let a paused-looking
  // player keep an <audio> element playing invisibly in the background.
  // Flush the final position before the element is torn down: after
  // detachAll, currentTime is gone, and that last position is the one
  // somebody actually wants back.
  state.stopTracking?.();
  state.stopTracking = null;

  state.stopFit?.();

  state.stopFit = null;

  { const w = document.querySelector('#embed-fit'); if (w) w.hidden = true; }

  detachAll(playerElements());
  state.engine?.destroyTorrent();
}

// ------------------------------------------------------------------ magnet --

const PASTE_HINT =
  'That is not something this can play. Paste a magnet link, an info hash, ' +
  'a direct media URL, an .m3u/.m3u8 playlist, or a YouTube/Vimeo link.';

/**
 * Best-effort human title for a non-magnet paste: the URL's last path
 * segment (e.g. "movie.mp4" or a playlist name), falling back to its
 * hostname when the path is empty (e.g. a bare "https://example.com/").
 */
function titleFromUri(uri) {
  try {
    const u = new URL(uri);
    const last = u.pathname.split('/').filter(Boolean).pop();
    if (!last) return u.hostname;
    try {
      return decodeURIComponent(last);
    } catch {
      return last;
    }
  } catch {
    return uri;
  }
}

/**
 * Pull the playable links out of a web page.
 *
 * A browser cannot do this itself. Reading cross-origin HTML needs an
 * Access-Control-Allow-Origin header and no torrent site sends one, so pasting
 * a description page produced only a CORS error in the console -- which reads
 * like a bug here rather than a rule of the platform. The relay fetches the
 * page instead and returns just the links.
 */
async function linksFromPage(pageUrl) {
  const response = await fetch(`/bridge/page?u=${encodeURIComponent(pageUrl)}`);
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || 'could not read that page');
  return body;
}

/**
 * Show what a page had on it. One link plays straight away -- a description
 * page with a single magnet is unambiguous and making somebody click twice for
 * it is just friction.
 */
function offerPageLinks(page) {
  const links = page.links ?? [];
  if (links.length === 1) {
    const only = links[0];
    play({ title: only.name || page.title || titleFromUri(only.url), year: 0 },
      { uri: only.url, title: only.url.slice(0, 90) });
    return;
  }

  $('#status').hidden = true;
  $('#player').hidden = true;
  renderLibrary(
    makeCollection({
      title: page.title || titleFromUri(page.source),
      sources: links.map((l) => makeSource({
        kind: 'auto',
        uri: l.url,
        meta: { title: l.name || l.url.slice(0, 80) },
      })),
    }),
    {
      mount: $('#library'),
      onPick: (picked) => play({ title: picked.meta.title || picked.uri }, { uri: picked.uri }),
    },
  );
  $('#library').hidden = false;
}

/**
 * Show what pressing Search will do, once a category is ticked but nothing has
 * been searched for yet.
 *
 * Without this a ticked chip appears to do nothing at all, which reads as
 * broken rather than as staged.
 */
function armSearch() {
  const picked = [...state.filters.groups].map((g) => GROUP_LABELS[g] || g);
  const btn = $('#search-form button[type=submit]');
  if (picked.length) {
    btn.textContent = 'Browse';
    showStatus(`Press Browse to see everything in ${picked.join(' + ')}`
      + ', or type a title to search within it.');
  } else {
    btn.textContent = 'Search';
    $('#status').hidden = true;
    restoreLanding();
  }
}

function showStatus(text) {
  const s = $('#status');
  s.replaceChildren(document.createTextNode(text));
  s.hidden = false;
}

async function streamPasted() {
  const raw = $('#magnet').value.trim();
  if (!raw) {
    showRetry(PASTE_HINT, () => {});
    return;
  }

  // normalizeMagnet's real job: turn a bare 40-hex info hash into a magnet
  // with trackers. If it doesn't recognize the input as a magnet/info hash,
  // pass the raw trimmed input straight through -- the registry decides
  // whether any resolver (url/embed/playlist) claims it.
  const magnet = normalizeMagnet(raw);
  const uri = magnet ?? raw;

  if (!state.registry) state.registry = buildRegistry();
  if (!state.registry.find(uri)) {
    // Nothing here can play a web page, but a torrent site's page is a
    // perfectly reasonable thing to paste -- it is where the magnet lives.
    if (/^https?:\/\//i.test(uri)) {
      showStatus('Reading that page…');
      try {
        const page = await linksFromPage(uri);
        offerPageLinks(page);
      } catch (err) {
        showRetry(`${err.message}. ${PASTE_HINT}`, () => {});
      }
      return;
    }
    showRetry(PASTE_HINT, () => {});
    return;
  }

  if (magnet) {
    const dn = /[?&]dn=([^&]+)/.exec(magnet);
    play(
      { title: dn ? decodeURIComponent(dn[1]).replace(/\+/g, ' ') : 'Pasted magnet', year: 0 },
      { magnet, title: magnet.slice(0, 90) },
    );
    return;
  }

  play(
    { title: titleFromUri(uri), year: 0 },
    { uri, title: uri.slice(0, 90) },
  );
}

// ---------------------------------------------------------------- settings --

/**
 * Point this client at a different Yarr.It, and connect your own services.
 *
 * The panel exists so that someone who runs their own stack is not reduced to
 * editing localStorage in a console -- and, since the services section landed,
 * so that a guest with no account has somewhere to put the address of their own
 * home server.
 */
function openSettings() {
  $('#set-server').value = getServer();
  showServerResult('', null);
  closeServiceEditor();
  renderServices();
  // Probed on open rather than only on demand. A settings screen that shows
  // stale state is a settings screen that gets believed, and the states worth
  // showing here (a rejected key, a blocked address) are the ones nobody would
  // think to press a button to discover.
  testAllServices();
  $('#settings').hidden = false;
  document.body.style.overflow = 'hidden';
  // select(), not focus(). Focus alone leaves a cursor sitting in the existing
  // value, so typing an address merges with the old one instead of replacing
  // it -- "192.168.0.50" typed over "yarrit.com" becomes "192.168.0.50yarrit.com".
  // Almost nobody opens this box to edit one character; they open it to enter a
  // different server, so the whole value should go on the first keystroke.
  $('#set-server').select();
}

function closeSettings() {
  $('#settings').hidden = true;
  // A detail sheet left open underneath still wants the page behind it frozen.
  if ($('#detail').hidden) document.body.style.overflow = '';
}

function showServerResult(text, ok) {
  const r = $('#set-result');
  r.textContent = text;
  r.classList.toggle('ok', ok === true);
  r.classList.toggle('bad', ok === false);
  r.hidden = !text;
}

async function testServer() {
  const raw = $('#set-server').value.trim();
  if (!raw) {
    showServerResult('Blank uses this site, which is already answering.', true);
    return;
  }
  showServerResult('Checking…', null);
  const res = await probeServer(raw);
  showServerResult(res.ok ? 'Reached it — that is a Yarr.It server.' : res.error, res.ok);
}

function saveServer() {
  const raw = $('#set-server').value.trim();
  const saved = setServer(raw);
  // Refusing to save beats saving nothing quietly: a typo would otherwise look
  // like it had been accepted while the client fell back to this site.
  if (raw && !saved) {
    showServerResult('That does not look like an address.', false);
    return;
  }
  // Every module resolves the address as it makes each call, but searches and
  // shelves already on the page came from the old one. A reload is the honest
  // way to leave nothing behind from the previous server.
  location.reload();
}

// ----------------------------------------------------------- your services --

/**
 * Somebody else's home server, configured by somebody with no account.
 *
 * This section is what separates a demo from a product. A guest opens it, types
 * in the address of the Radarr on their own shelf, and it works -- or it says,
 * in one sentence, exactly which wall stopped it and what gets past. Nothing
 * here asks who they are, because nothing here needs to: the config lives in
 * this browser and the requests go straight from this browser to their box.
 */

const svcStore = createServiceStore();

// id -> { status: 'testing' | 'done', route, health }. Kept out of the store on
// purpose: a probe result is about right now, and persisting it would let a
// stale "healthy" outlive the service it described.
const svcProbes = new Map();
// The id being edited, '' for a new one, null when the editor is closed.
let svcEditing = null;

function renderServices() {
  const list = $('#svc-list');
  list.textContent = '';
  const rows = svcStore.list();

  if (!rows.length) {
    list.append(el('p', 'svc-empty',
      'Nothing connected yet. Add the Radarr, Sonarr, Jellyfin, Plex, Komga or RomM you '
      + 'already run and it will show up here.'));
  }

  for (const svc of rows) list.append(serviceRow(svc));
  renderServiceAdvice(rows);
}

function serviceRow(svc) {
  const spec = SERVICE_TYPES[svc.type];
  const node = el('div', 'svc' + (svc.enabled ? '' : ' off'));
  node.dataset.id = svc.id;

  const top = el('div', 'svc-top');
  top.append(el('b', 'svc-name', svc.name));
  top.append(el('span', 'svc-kind', spec.label));

  const probe = svcProbes.get(svc.id);
  if (!svc.enabled) top.append(el('span', 'hp hp-not_configured', 'Not in use'));
  else if (!probe) top.append(el('span', 'hp hp-untested', 'Not tested yet'));
  else if (probe.status === 'testing') top.append(el('span', 'hp hp-testing', 'Testing…'));
  else top.append(el('span', `hp hp-${probe.health.state}`, healthLabel(probe.health.state)));

  const actions = el('div', 'svc-actions');
  for (const [act, label, cls] of [
    ['test', 'Test', ''],
    ['edit', 'Edit', ''],
    ['toggle', svc.enabled ? 'Disable' : 'Enable', ''],
    ['remove', 'Remove', 'danger'],
  ]) {
    const b = el('button', cls, label);
    b.type = 'button';
    b.dataset.act = act;
    actions.append(b);
  }
  top.append(actions);
  node.append(top);

  // The address, never the key. Whether a key exists is worth saying; what it
  // is, is not, and putting it on screen is how it ends up in a screenshot.
  const meta = el('p', 'svc-meta');
  meta.append(document.createTextNode(svc.url));
  meta.append(el('span', '', ` · ${describeService(svc)} · `));
  meta.append(el('span', '', svc.key ? `${spec.credential} saved` : `no ${spec.credential.toLowerCase()} yet`));
  node.append(meta);

  if (svc.enabled && probe && probe.status === 'done') {
    node.append(el('p', 'svc-route', routeLine(probe.route)));
    // transport.js and the probe already wrote a sentence aimed at whoever is
    // reading this screen. It is repeated as written: paraphrasing it here
    // would put two explanations of one problem in front of the same person.
    if (probe.health.detail) node.append(el('p', 'svc-detail', probe.health.detail));
  }
  return node;
}

/** Which of the three doors this service came through, in plain words. */
function routeLine(route) {
  if (route.transport === TRANSPORT.DIRECT) return 'Route: reachable directly from this page.';
  if (route.transport === TRANSPORT.EXTENSION) return 'Route: reached through the Yarr.It extension.';
  if (route.transport === TRANSPORT.SERVER) return 'Route: proxied by your own Yarr.It instance.';
  return 'Route: nothing here can reach it — you would need your own instance.';
}

function renderServiceAdvice(rows) {
  const box = $('#svc-advice');
  box.textContent = '';
  const advice = routeAdvice(rows, {
    hasExtension: extensionAvailable(),
    pageProtocol: location.protocol,
  });
  box.hidden = !advice;
  if (!advice) return;

  box.append(el('p', 'adv-why', advice.reason));
  for (const r of advice.routes) {
    const a = el('a', '', r.title);
    a.href = r.href;
    a.rel = 'noopener';
    a.append(el('em', '', r.detail));
    box.append(a);
  }
}

async function testService(id) {
  const svc = svcStore.get(id);
  if (!svc) return;
  svcProbes.set(id, { status: 'testing' });
  renderServices();
  const result = await probeService(svc);
  svcProbes.set(id, { status: 'done', ...result });
  renderServices();
}

async function testAllServices() {
  // In parallel, and each one already bounded by transport.js. Serially, ten
  // services behind a dead VPN would take ten timeouts to paint one panel --
  // the panel someone opened in order to fix them.
  await Promise.all(svcStore.list().filter((s) => s.enabled).map((s) => testService(s.id)));
}

// --- the editor ------------------------------------------------------------

function fillServiceTypes() {
  const sel = $('#svc-type');
  if (sel.options.length) return;
  // Built from the table rather than typed into the markup, so the picker can
  // never drift from the set of things this client can actually talk to.
  for (const id of allServiceTypes()) {
    const o = document.createElement('option');
    o.value = id;
    o.textContent = SERVICE_TYPES[id].label;
    sel.append(o);
  }
}

function syncKeyLabels() {
  const spec = SERVICE_TYPES[$('#svc-type').value];
  if (!spec) return;
  $('#svc-key-label').textContent = spec.credential;
  const where = `Where to find it: ${spec.credentialHint}.`;
  // Two of these have no header scheme and take the key in the query string.
  // Said out loud, because it changes where that key can end up.
  $('#svc-key-hint').textContent = spec.keyInURL
    ? `${where} ${spec.label} has no header for this, so the key travels in the query `
      + 'string of the request to your own server — it may appear in that server\'s logs.'
    : where;
}

function openServiceEditor(id) {
  fillServiceTypes();
  svcEditing = id ?? '';
  const svc = id ? svcStore.get(id) : null;

  $('#svc-type').value = svc ? svc.type : 'radarr';
  $('#svc-name').value = svc ? svc.name : '';
  $('#svc-url').value = svc ? svc.url : '';
  $('#svc-key').value = svc ? svc.key : '';
  $('#svc-enabled').checked = svc ? svc.enabled : true;
  syncKeyLabels();
  showServiceResult('', null);

  $('#svc-editor').hidden = false;
  $('#svc-buttons').hidden = true;
  $('#svc-name').focus();
}

function closeServiceEditor() {
  svcEditing = null;
  $('#svc-editor').hidden = true;
  $('#svc-buttons').hidden = false;
  // The key must not sit in a DOM node after the box is closed.
  $('#svc-key').value = '';
}

function showServiceResult(text, ok) {
  const r = $('#svc-result');
  r.textContent = text;
  r.classList.toggle('ok', ok === true);
  r.classList.toggle('bad', ok === false);
  r.hidden = !text;
}

/** What the boxes currently say, as a service that may not be saved yet. */
function draftService() {
  return {
    id: svcEditing || 'draft',
    type: $('#svc-type').value,
    name: $('#svc-name').value.trim() || SERVICE_TYPES[$('#svc-type').value].label,
    url: normaliseServiceURL($('#svc-url').value),
    key: $('#svc-key').value.trim(),
    enabled: true,
  };
}

/**
 * Probe what is in the boxes, before it is saved.
 *
 * Testing the draft rather than the stored copy is the whole value of the
 * button: it answers "is this key right" while the key is still in front of
 * you, instead of after you have saved it and have to guess which field to
 * change.
 */
async function testDraftService() {
  const draft = draftService();
  if (!draft.url) {
    showServiceResult('That does not look like an address.', false);
    return;
  }
  showServiceResult('Checking…', null);
  const { route, health } = await probeService(draft);
  showServiceResult(
    `${healthLabel(health.state)} — ${health.detail} (${routeLine(route).replace(/^Route: /, '')})`,
    health.state === 'healthy',
  );
}

function saveServiceEditor() {
  const input = {
    type: $('#svc-type').value,
    name: $('#svc-name').value,
    url: $('#svc-url').value,
    key: $('#svc-key').value,
    enabled: $('#svc-enabled').checked,
  };
  const res = svcEditing ? svcStore.update(svcEditing, input) : svcStore.add(input);
  // Refusing to save beats saving nothing quietly: an address that appears to
  // save is a row that looks configured, answers nothing, and gives no clue
  // which half of it is wrong.
  if (!res.ok) {
    showServiceResult(res.error, false);
    return;
  }
  const id = res.service.id;
  svcProbes.delete(id);
  closeServiceEditor();
  renderServices();
  if (res.service.enabled) testService(id);
}

function onServiceListClick(e) {
  const btn = e.target.closest('button[data-act]');
  if (!btn) return;
  const id = btn.closest('.svc')?.dataset.id;
  if (!id) return;

  if (btn.dataset.act === 'test') { testService(id); return; }
  if (btn.dataset.act === 'edit') { openServiceEditor(id); return; }
  if (btn.dataset.act === 'toggle') {
    const svc = svcStore.get(id);
    svcStore.update(id, { enabled: !svc.enabled });
    svcProbes.delete(id);
    renderServices();
    if (!svc.enabled) testService(id);
    return;
  }
  if (btn.dataset.act === 'remove') {
    const svc = svcStore.get(id);
    if (!confirm(`Remove ${svc.name}? The key stored for it is deleted from this browser too.`)) return;
    svcStore.remove(id);
    svcProbes.delete(id);
    renderServices();
  }
}

// -------------------------------------------------------------------- init --


/**
 * Paint the account and network-protection panels.
 *
 * Both were fully working on the server and unreachable in the page: /auth/login
 * has always redirected to Authentik and /auth/me has always answered, but
 * nothing ever called them, so "how do I sign in?" had no answer.
 */
async function refreshAccount() {
  const g = vpnGuidance(getServer());
  const lede = $('#vpn-lede');
  if (lede) lede.textContent = g.body + ' ' + g.appliesTo;
  const cmd = $('#vpn-cmd');
  if (cmd) cmd.textContent = g.command;

  // Report the address the outside world sees, when the server offers it. A
  // claim of protection nobody can check is worth nothing.
  egressStatus().then((e) => {
    const el = $('#vpn-status');
    if (!el || !e) return;
    el.textContent = 'Your server currently reaches the internet as ' + e + '.';
    el.hidden = false;
  });

  const me = await whoAmI();
  const who = $('#acct-who');
  const inBtn = $('#acct-signin');
  const outBtn = $('#acct-signout');
  const hdrIn = $('#signin-btn');
  const hdrOut = $('#signout-btn');

  if (me) {
    if (who) who.textContent = displayName(me);
    if (inBtn) inBtn.hidden = true;
    if (outBtn) outBtn.hidden = false;
    if (hdrIn) hdrIn.hidden = true;
    if (hdrOut) hdrOut.hidden = false;
  } else {
    if (who) who.textContent = 'Not signed in';
    if (inBtn) inBtn.hidden = false;
    if (outBtn) outBtn.hidden = true;
    if (hdrIn) hdrIn.hidden = false;
    if (hdrOut) hdrOut.hidden = true;
  }
}

function wireAccount() {
  const go = (url) => () => { location.href = url(); };
  for (const id of ['#acct-signin', '#signin-btn']) {
    const el = $(id);
    if (el) el.addEventListener('click', go(signInURL));
  }
  for (const id of ['#acct-signout', '#signout-btn']) {
    const el = $(id);
    if (el) el.addEventListener('click', go(signOutURL));
  }
  refreshAccount();
}


/**
 * The add-on panel.
 *
 * Wired here rather than left as a module nobody imports, which is how the
 * service setup and the sign-in button both shipped invisible. A finished
 * feature that no control reaches has not shipped.
 */
const addonAPI = createAddonAPI();

async function refreshAddons() {
  const host = $('#addon-list');
  if (!host) return;
  let addons = [];
  try {
    addons = await addonAPI.list();
  } catch {
    // Signed out is the normal case: the list route needs a session because an
    // add-on URL can carry a debrid key. An empty panel is the right answer,
    // not an error.
    addons = [];
  }
  renderAddons(host, {
    addons,
    handlers: {
      onRemove: async (id) => { await addonAPI.remove(id).catch(() => {}); refreshAddons(); },
      onMove: async (id, delta) => {
        const next = moveAddon(addons, id, delta);
        await addonAPI.reorder(next.map((a) => a.id)).catch(() => {});
        refreshAddons();
      },
    },
  });
}

function wireAddons() {
  const input = $('#addon-url');
  const btn = $('#addon-add');
  const out = $('#addon-result');
  if (!input || !btn) return;

  const say = (msg, ok) => {
    if (!out) return;
    out.textContent = msg;
    out.classList.toggle('ok', ok === true);
    out.classList.toggle('bad', ok === false);
    out.hidden = !msg;
  };

  const add = async () => {
    const url = normaliseAddonURL(input.value);
    if (!url) {
      say('That does not look like an add-on manifest URL.', false);
      return;
    }
    say('Checking…', null);
    try {
      const a = await addonAPI.add(url);
      say(`Added ${a?.name || 'add-on'}.`, true);
      input.value = '';
      refreshAddons();
    } catch (e) {
      say(String(e && e.message ? e.message : e), false);
    }
  };

  btn.addEventListener('click', add);
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') add(); });
  refreshAddons();
}

function init() {
  if (localStorage.getItem('privacy-ack') === '1') $('#privacy').hidden = true;
  $('#privacy-ok').addEventListener('click', () => {
    localStorage.setItem('privacy-ack', '1');
    $('#privacy').hidden = true;
  });

  $('#search-form').addEventListener('submit', (e) => {
    e.preventDefault();
    state.query = $('#q').value.trim();
    // No words but a category ticked is a browse: "show me games".
    if (!state.query && !state.filters.groups.size) return;
    history.replaceState(null, '', state.query ? `?q=${encodeURIComponent(state.query)}` : '?');
    $('#search-form button[type=submit]').textContent = 'Search';
    search();
  });

  $('#magnet-go').addEventListener('click', streamPasted);
  $('#magnet').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); streamPasted(); }
  });

  $('#f-seeders').addEventListener('input', (e) => {
    state.filters.seeders = Number(e.target.value) || 0; refilter();
  });
  $('#f-minsize').addEventListener('input', (e) => { state.filters.minSize = e.target.value; refilter(); });
  $('#f-maxsize').addEventListener('input', (e) => { state.filters.maxSize = e.target.value; refilter(); });
  $('#f-sort').addEventListener('change', (e) => { state.filters.sort = e.target.value; refilter(); });
  $('#f-adult').addEventListener('click', () => {
    state.filters.adult = !state.filters.adult;
    $('#f-adult').classList.toggle('on', state.filters.adult);
    refilter();
  });

  $('#f-websafe').addEventListener('click', () => {
    state.filters.webSafe = !state.filters.webSafe;
    $('#f-websafe').classList.toggle('on', state.filters.webSafe);
    refilter();
  });
  for (const [id, value] of [['#f-instant', 'instant'], ['#f-swarm', 'swarm']]) {
    $(id).addEventListener('click', () => {
      // Clicking the one already active clears back to showing both, which is
      // what every other toggle on this bar does.
      state.filters.source = state.filters.source === value ? '' : value;
      $('#f-instant').classList.toggle('on', state.filters.source === 'instant');
      $('#f-swarm').classList.toggle('on', state.filters.source === 'swarm');
      if (state.cards.length) refilter();
    });
  }

  $('#filter-reset').addEventListener('click', () => {
    state.filters = {
      seeders: 1, minSize: '', maxSize: '',
      quality: new Set(), codec: new Set(), groups: new Set(),
      webSafe: false, adult: false, sort: 'seeders', source: '',
    };
    $('#f-seeders').value = 1; $('#f-minsize').value = ''; $('#f-maxsize').value = '';
    $('#f-sort').value = 'seeders';
    renderFilters();
    search({ showSpinner: false });
  });

  $('#settings-open').addEventListener('click', openSettings);
  wireAccount();
  wireAddons();
  $('#settings-close').addEventListener('click', closeSettings);
  $('#settings').addEventListener('click', (e) => { if (e.target.id === 'settings') closeSettings(); });
  $('#set-test').addEventListener('click', testServer);
  $('#set-save').addEventListener('click', saveServer);
  $('#set-server').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); testServer(); }
  });
  $('#set-reset').addEventListener('click', () => {
    setServer('');
    location.reload();
  });

  $('#svc-add').addEventListener('click', () => openServiceEditor(null));
  $('#svc-test-all').addEventListener('click', testAllServices);
  $('#svc-list').addEventListener('click', onServiceListClick);
  $('#svc-type').addEventListener('change', syncKeyLabels);
  $('#svc-test-one').addEventListener('click', testDraftService);
  $('#svc-cancel').addEventListener('click', closeServiceEditor);
  $('#svc-editor').addEventListener('submit', (e) => {
    e.preventDefault();
    saveServiceEditor();
  });

  $('#detail-close').addEventListener('click', closeDetail);
  $('#detail').addEventListener('click', (e) => { if (e.target.id === 'detail') closeDetail(); });
  $('#player-close').addEventListener('click', closePlayer);
  $('#reader-close').addEventListener('click', closeReader);
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    // Innermost first: settings can be opened over a detail sheet, and the
    // service editor sits inside settings. Escaping out of a half-typed key
    // straight past the panel would lose the whole entry.
    if (!$('#svc-editor').hidden) closeServiceEditor();
    else if (!$('#settings').hidden) closeSettings();
    else if (!$('#reader').hidden) closeReader();
    else if (!$('#player').hidden) closePlayer();
    else if (!$('#detail').hidden) closeDetail();
  });

  // Draw the category chips immediately. Without this they appear only once a
  // search has returned facets, which is exactly the state where you cannot
  // use them to choose what to search for.
  renderFilters();

  // The intro promises whatever the vocabulary actually covers. Hand-written,
  // it said "films, TV, music and images" long after books, comics and games
  // had been added — and the copy is the first thing that tells a visitor a
  // domain exists at all.
  const covers = domainSentence();
  if (covers) {
    $('#intro-covers').textContent = covers;
    $('#q').placeholder = `Search ${covers.toLowerCase()}…`;
  }

  const params = new URLSearchParams(location.search);
  const initial = params.get('q');
  if (!initial) loadHome();
  if (initial) {
    $('#q').value = initial;
    state.query = initial;
    search();
  }
  const magnet = normalizeMagnet(params.get('magnet'));
  if (magnet) {
    const dn = /[?&]dn=([^&]+)/.exec(magnet);
    play({ title: dn ? decodeURIComponent(dn[1]).replace(/\+/g, ' ') : 'Direct magnet', year: 0 },
      { magnet, title: magnet.slice(0, 90) });
  }
}

if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
else init();
