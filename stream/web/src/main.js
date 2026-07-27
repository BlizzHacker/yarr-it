import { StreamEngine, classify, needsWebCodecs } from './engine.js';

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
  active: null,
  filters: { seeders: 1, minSize: '', maxSize: '', quality: new Set(), codec: new Set(), webSafe: false, sort: 'relevance' },
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
  return p;
}

async function search({ showSpinner = true } = {}) {
  if (!state.query) return;
  $('#intro').hidden = true;
  $('#discover').hidden = true;
  if (showSpinner) {
    $('#status').textContent = 'Searching every indexer…';
    $('#status').hidden = false;
    $('#grid').replaceChildren();
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
  $('#filters').hidden = !f;
  if (!f) return;

  chipRow($('#f-quality'), f.qualities, state.filters.quality);
  chipRow($('#f-codec'), f.codecs, state.filters.codec);
  $('#f-websafe').classList.toggle('on', state.filters.webSafe);
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

function play(card, src) {
  $('#player').hidden = false;
  $('#player-title').textContent = card.title + (card.year ? ` (${card.year})` : '');
  $('#player-sub').textContent = src.title;
  setPlayerStatus('Connecting to the swarm…');

  const video = $('#video');
  video.hidden = true;
  video.removeAttribute('src');
  $('#image').hidden = true;

  if (!state.engine) state.engine = new StreamEngine({ onStats: renderStats });
  window.__engine = state.engine; // diagnostics

  state.engine.add(src.magnet, {
    onReady: (file) => {
      const kind = classify(file.name);
      setPlayerStatus(`Buffering ${file.name}…`);

      if (kind === 'image') {
        file.blob().then((b) => {
          const img = $('#image');
          img.src = URL.createObjectURL(b);
          img.hidden = false;
          setPlayerStatus('');
        });
        return;
      }
      if (needsWebCodecs(file.name)) {
        setPlayerStatus(
          `${file.name.split('.').pop().toUpperCase()} container — if this stalls, pick an MP4 source.`,
        );
      }
      video.hidden = false;
      attachMedia(video, file);
      video.addEventListener('playing', () => setPlayerStatus(''), { once: true });
    },
    onError: (err) => setPlayerStatus(`Could not start: ${err.message}`),
  });
}

/**
 * Point a media element at a torrent file.
 *
 * file.streamURL is served by WebTorrent's service worker, which answers range
 * requests so playback can start and seek while the download is in flight. If
 * the worker is unavailable (private windows, some webviews) we fall back to a
 * blob, which works only once the file is complete.
 */
async function attachMedia(elem, file) {
  const ready = await (state.engine?._serverReady ?? false);
  if (ready && file.streamURL) {
    elem.src = file.streamURL;
    elem.play?.().catch(() => {});
    return;
  }
  setPlayerStatus('Streaming unavailable in this browser — downloading fully first…');
  try {
    const blob = await file.blob();
    elem.src = URL.createObjectURL(blob);
    setPlayerStatus('');
    elem.play?.().catch(() => {});
  } catch (err) {
    setPlayerStatus(`Playback error: ${err.message}`);
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
  $('#player').hidden = true;
  const v = $('#video');
  v.pause?.();
  v.removeAttribute('src');
  v.load?.();
  $('#image').hidden = true;
  $('#image').removeAttribute('src');
  state.engine?.destroyTorrent();
}

// ------------------------------------------------------------------ magnet --

/** Accept a full magnet URI or a bare 40-character info hash. */
function normalizeMagnet(input) {
  const v = (input || '').trim();
  if (/^magnet:\?/i.test(v)) return v;
  if (/^[a-f0-9]{40}$/i.test(v)) {
    const trackers = [
      'udp://tracker.opentrackr.org:1337/announce',
      'udp://open.demonii.com:1337/announce',
      'udp://open.stealth.si:80/announce',
      'udp://exodus.desync.com:6969/announce',
      'udp://tracker.torrent.eu.org:451/announce',
    ];
    return `magnet:?xt=urn:btih:${v.toLowerCase()}` +
      trackers.map((t) => `&tr=${encodeURIComponent(t)}`).join('');
  }
  return null;
}

function streamPasted() {
  const magnet = normalizeMagnet($('#magnet').value);
  if (!magnet) {
    showRetry('That is not a magnet link or a 40-character info hash.', () => {});
    return;
  }
  const dn = /[?&]dn=([^&]+)/.exec(magnet);
  play(
    { title: dn ? decodeURIComponent(dn[1]).replace(/\+/g, ' ') : 'Pasted magnet', year: 0 },
    { magnet, title: magnet.slice(0, 90) },
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
  $('#f-websafe').addEventListener('click', () => {
    state.filters.webSafe = !state.filters.webSafe;
    $('#f-websafe').classList.toggle('on', state.filters.webSafe);
    refilter();
  });
  $('#filter-reset').addEventListener('click', () => {
    state.filters = { seeders: 1, minSize: '', maxSize: '', quality: new Set(), codec: new Set(), webSafe: false, sort: 'relevance' };
    $('#f-seeders').value = 1; $('#f-minsize').value = ''; $('#f-maxsize').value = '';
    $('#f-sort').value = 'relevance';
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
