import { StreamEngine, classify, needsWebCodecs } from './engine.js';
import { createRegistry, makeSource, isCollection } from './source.js';
import { createTorrentResolver, normalizeMagnet } from './resolvers/torrent.js';
import { urlResolver } from './resolvers/url.js';
import { embedResolver } from './resolvers/embed.js';
import { playlistResolver } from './resolvers/playlist.js';
import { flashResolver } from './resolvers/flash.js';
import { gameResolver } from './resolvers/game.js';
import { archiveResolver, archiveFileResolver } from './resolvers/archive.js';
import { renderPlayable, detachAll } from './player.js';
import { renderLibrary } from './library.js';
import { PlaybackError } from './failures.js';

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
  filters: {
    seeders: 1, minSize: '', maxSize: '',
    quality: new Set(), codec: new Set(), groups: new Set(),
    webSafe: false, adult: false, sort: 'seeders',
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
  if (f.groups.size) p.set('groups', [...f.groups].join(','));
  // Adult results are excluded server-side unless explicitly requested.
  if (f.adult) p.set('adult', '1');
  return p;
}

async function search({ showSpinner = true } = {}) {
  if (!state.query) return;
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
    const res = await fetch(`/api/search?${filterParams()}`);
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
  if (!f) return;

  groupRow($('#f-groups'), f.groups, state.filters.groups);
  $('#f-adult').classList.toggle('on', state.filters.adult);
  $('#f-adult').textContent = f.adultCount ? `18+ ${f.adultCount}` : '18+';
  chipRow($('#f-quality'), f.qualities, state.filters.quality);
  chipRow($('#f-codec'), f.codecs, state.filters.codec);
  $('#f-websafe').classList.toggle('on', state.filters.webSafe);
}

// Human labels for the Newznab buckets the API reports.
const GROUP_LABELS = {
  movies: 'Movies', tv: 'TV', anime: 'Anime', music: 'Music',
  games: 'Games', apps: 'Apps', books: 'Books', other: 'Other',
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
    c.append(el('span', 'cnt', String(count)));
    if (selected.has(value)) c.classList.add('on');
    c.addEventListener('click', () => {
      selected.has(value) ? selected.delete(value) : selected.add(value);
      c.classList.toggle('on');
      refilter();
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
  bar.append(el('span', null,
    `result${state.cards.length === 1 ? '' : 's'} for “${state.query}”` +
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
  if (card.art?.poster) {
    const img = el('img');
    img.loading = 'lazy';
    img.alt = card.title;
    img.src = card.art.poster;
    p.append(img);
  } else {
    p.append(el('div', 'noart', card.title));
  }

  const seedBadge = el('span', card.seeders > 0 ? 'badge' : 'badge dead', `${card.seeders}▲`);
  p.append(seedBadge);
  if (card.art?.rating) p.append(el('span', 'rating', card.art.rating.toFixed(1)));
  const bq = card.sources[card.best]?.quality;
  if (bq) p.append(el('span', 'best-q', bq));
  t.append(p);

  t.append(el('div', 'tname', card.title));
  const bits = [];
  if (card.year) bits.push(card.year);
  if (card.isSeries) bits.push(`S${card.season}E${card.episode}`);
  bits.push(`${card.sources.length} source${card.sources.length === 1 ? '' : 's'}`);
  t.append(el('div', 'tmeta', bits.join(' · ')));

  t.addEventListener('click', () => openDetail(card));
  return t;
}

// ---------------------------------------------------------------- discover --

/**
 * Browsable landing rows from TMDB.
 *
 * These are catalogue entries, not torrents — nothing here is indexed or
 * hosted. Clicking one runs an ordinary search for its title, which is where
 * any actual sources come from.
 */
async function loadDiscover() {
  const host = $('#discover');
  try {
    const res = await fetch('/api/discover');
    if (!res.ok) return;
    const { rows } = await res.json();
    if (!rows?.length) return;

    host.replaceChildren();
    for (const row of rows) {
      const shelf = el('section', 'shelf');
      shelf.append(el('h3', null, row.title));
      const rail = el('div', 'rail');
      for (const item of row.items) rail.append(discoverTile(item));
      shelf.append(rail);
      host.append(shelf);
    }
  } catch {
    /* discovery is a nicety; a failure just leaves the intro copy in place */
  }
}

function discoverTile(item) {
  const t = el('button', 'tile');
  t.type = 'button';
  t.title = item.overview || item.title;

  const p = el('div', 'poster');
  if (item.poster) {
    const img = el('img');
    img.loading = 'lazy';
    img.alt = item.title;
    img.src = item.poster;
    p.append(img);
  } else {
    p.append(el('div', 'noart', item.title));
  }
  if (item.rating) p.append(el('span', 'rating', item.rating.toFixed(1)));
  if (item.mediaType === 'tv') p.append(el('span', 'best-q', 'TV'));
  t.append(p);

  t.append(el('div', 'tname', item.title));
  t.append(el('div', 'tmeta', item.year ? String(item.year) : ''));

  t.addEventListener('click', () => {
    const q = item.year ? `${item.title} ${item.year}` : item.title;
    $('#q').value = q;
    state.query = q;
    history.replaceState(null, '', `?q=${encodeURIComponent(q)}`);
    search();
  });
  return t;
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
  sub.push(`${card.seeders} seeders`);
  $('#d-sub').textContent = sub.join('  ·  ');

  $('#d-overview').textContent = card.art?.overview || '';
  $('#d-overview').hidden = !card.art?.overview;

  const g = $('#d-genres');
  g.replaceChildren();
  for (const name of card.art?.genres || []) g.append(el('span', 'chip', name));

  $('#d-srch').textContent = `${card.sources.length} source${card.sources.length === 1 ? '' : 's'} — pick one to stream`;

  const list = $('#d-sources');
  list.replaceChildren();
  card.sources.forEach((s, i) => list.append(sourceRow(card, s, i === card.best)));
}

function sourceRow(card, s, isBest) {
  const row = el('button', isBest ? 'source best' : 'source');
  row.type = 'button';

  const l = el('div', 'sl');
  l.append(el('span', 'q', s.quality || '—'));
  const codec = el('span', s.webSafe ? 'tag ok' : 'tag warn', s.codec || 'unknown');
  codec.title = s.webSafe
    ? 'Plays directly in your browser'
    : 'Needs hardware decode on your device — no server transcoding';
  l.append(codec);
  if (s.source) l.append(el('span', 'tag', s.source));
  l.append(el('span', 'name', s.title));
  row.append(l);

  const r = el('div', 'sr');
  r.append(el('span', null, s.sizeHuman));
  r.append(el('span', s.seeders > 0 ? 'seeds' : 'seeds dead', `${s.seeders}▲`));
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
    .register(archiveFileResolver) // a chosen file inside an archive.org item
    .register(archiveResolver)     // before url: archive.org items need /metadata
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

function streamPasted() {
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

// -------------------------------------------------------------------- init --

function init() {
  if (localStorage.getItem('privacy-ack') === '1') $('#privacy').hidden = true;
  $('#privacy-ok').addEventListener('click', () => {
    localStorage.setItem('privacy-ack', '1');
    $('#privacy').hidden = true;
  });

  $('#search-form').addEventListener('submit', (e) => {
    e.preventDefault();
    state.query = $('#q').value.trim();
    if (!state.query) return;
    history.replaceState(null, '', `?q=${encodeURIComponent(state.query)}`);
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
  $('#filter-reset').addEventListener('click', () => {
    state.filters = {
      seeders: 1, minSize: '', maxSize: '',
      quality: new Set(), codec: new Set(), groups: new Set(),
      webSafe: false, adult: false, sort: 'seeders',
    };
    $('#f-seeders').value = 1; $('#f-minsize').value = ''; $('#f-maxsize').value = '';
    $('#f-sort').value = 'seeders';
    renderFilters();
    search({ showSpinner: false });
  });

  $('#detail-close').addEventListener('click', closeDetail);
  $('#detail').addEventListener('click', (e) => { if (e.target.id === 'detail') closeDetail(); });
  $('#player-close').addEventListener('click', closePlayer);
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    if (!$('#player').hidden) closePlayer();
    else if (!$('#detail').hidden) closeDetail();
  });

  const params = new URLSearchParams(location.search);
  const initial = params.get('q');
  if (!initial) loadDiscover();
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
