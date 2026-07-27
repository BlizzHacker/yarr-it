import { StreamEngine, classify, needsWebCodecs } from './engine.js';

const $ = (sel) => document.querySelector(sel);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

const state = { cards: [], active: null, engine: null };

// ---------------------------------------------------------------- search ----

async function search(query, kind) {
  const results = $('#results');
  results.replaceChildren();
  $('#status').textContent = 'Searching every indexer…';
  $('#status').hidden = false;

  try {
    const url = `/api/search?q=${encodeURIComponent(query)}${kind ? `&kind=${kind}` : ''}`;
    const res = await fetch(url);
    if (!res.ok) throw new Error(`search failed (${res.status})`);
    const data = await res.json();
    state.cards = data.cards || [];
    $('#status').hidden = true;

    if (!state.cards.length) {
      $('#status').textContent = 'Nothing found. Try a different title.';
      $('#status').hidden = false;
      return;
    }
    renderCards(state.cards);
  } catch (err) {
    $('#status').textContent = err.message;
    $('#status').hidden = false;
  }
}

function renderCards(cards) {
  const results = $('#results');
  results.replaceChildren();
  for (const card of cards) results.append(renderCard(card));
}

function renderCard(card) {
  const node = el('article', 'card');

  const head = el('div', 'card-head');
  const title = el('h3', 'card-title', card.title || 'Untitled');
  head.append(title);

  const meta = el('div', 'card-meta');
  if (card.year) meta.append(el('span', 'chip', String(card.year)));
  if (card.isSeries) meta.append(el('span', 'chip', `S${card.season}E${card.episode}`));
  meta.append(el('span', 'chip seeds', `${card.seeders} seeders`));
  meta.append(el('span', 'chip muted', `${card.sources.length} source${card.sources.length === 1 ? '' : 's'}`));
  head.append(meta);
  node.append(head);

  const list = el('div', 'sources');
  for (const [i, s] of card.sources.entries()) {
    const row = el('button', 'source');
    row.type = 'button';

    const left = el('div', 'source-left');
    left.append(el('span', 'q', s.quality || '—'));
    const codec = el('span', s.webSafe ? 'tag ok' : 'tag warn', s.codec || 'unknown');
    codec.title = s.webSafe
      ? 'Plays directly in your browser'
      : 'Needs hardware decode on your device (no server transcoding)';
    left.append(codec);
    if (s.source) left.append(el('span', 'tag muted', s.source));
    left.append(el('span', 'name', s.title));
    row.append(left);

    const right = el('div', 'source-right');
    right.append(el('span', 'size', s.sizeHuman));
    right.append(el('span', s.seeders > 0 ? 'seeds' : 'seeds dead', `${s.seeders}▲`));
    right.append(el('span', 'ix', s.indexer));
    row.append(right);

    if (i === card.best) row.classList.add('best');
    row.addEventListener('click', () => play(card, s));
    list.append(row);
  }
  node.append(list);
  return node;
}

// ---------------------------------------------------------------- player ----

function play(card, src) {
  const overlay = $('#player');
  overlay.hidden = false;
  $('#player-title').textContent = card.title + (card.year ? ` (${card.year})` : '');
  $('#player-sub').textContent = src.title;
  setPlayerStatus('Connecting to the swarm…');

  const video = $('#video');
  video.hidden = true;
  video.removeAttribute('src');

  if (!state.engine) {
    state.engine = new StreamEngine({ onStats: renderStats });
  }
  // Exposed for diagnostics; harmless in production and invaluable for
  // verifying the relay path from the console.
  window.__engine = state.engine;

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
          `${file.name.split('.').pop().toUpperCase()} container — your browser may not play this ` +
            `natively. Trying direct playback; if it stalls, pick an MP4 source.`,
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
 * Preferred path is file.streamURL, served by WebTorrent's service worker: it
 * answers range requests, so playback can start and seek while the download is
 * still in flight. If the worker is unavailable (private windows and some
 * embedded webviews block registration) we fall back to a blob, which works but
 * only once the file is complete.
 */
async function attachMedia(elem, file) {
  const ready = await (state.engine?._serverReady ?? false);
  if (ready && file.streamURL) {
    elem.src = file.streamURL;
    elem.play?.().catch(() => {
      /* autoplay policy -- the user can press play */
    });
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

function setPlayerStatus(text) {
  const n = $('#player-status');
  n.textContent = text;
  n.hidden = !text;
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

// ------------------------------------------------------------------ init ----

/** Accept a full magnet URI or a bare 40-char info hash. */
function normalizeMagnet(input) {
  const v = input.trim();
  if (/^magnet:\?/i.test(v)) return v;
  if (/^[a-f0-9]{40}$/i.test(v)) {
    const trackers = [
      'udp://tracker.opentrackr.org:1337/announce',
      'udp://open.demonii.com:1337/announce',
      'udp://tracker.openbittorrent.com:6969/announce',
      'udp://open.stealth.si:80/announce',
      'udp://exodus.desync.com:6969/announce',
    ];
    return (
      `magnet:?xt=urn:btih:${v.toLowerCase()}` +
      trackers.map((t) => `&tr=${encodeURIComponent(t)}`).join('')
    );
  }
  return null;
}

function init() {
  $('#search-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const q = $('#q').value.trim();
    if (!q) return;
    const kind = $('#kind').value;
    history.replaceState(null, '', `?q=${encodeURIComponent(q)}`);
    $('#intro').hidden = true;
    search(q, kind);
  });

  $('#magnet-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const magnet = normalizeMagnet($('#magnet').value);
    if (!magnet) {
      $('#status').textContent = 'That does not look like a magnet link or a 40-character info hash.';
      $('#status').hidden = false;
      return;
    }
    $('#status').hidden = true;
    const dn = /[?&]dn=([^&]+)/.exec(magnet);
    play(
      { title: dn ? decodeURIComponent(dn[1]).replace(/\+/g, ' ') : 'Pasted magnet', year: 0 },
      { magnet, title: magnet.slice(0, 90) },
    );
  });

  $('#player-close').addEventListener('click', closePlayer);
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !$('#player').hidden) closePlayer();
  });

  const initial = new URLSearchParams(location.search).get('q');
  if (initial) {
    $('#q').value = initial;
    $('#intro').hidden = true;
    search(initial, '');
  }

  // Magnet handoff from the extension, or a magnet: link opened via the
  // registered protocol handler.
  const magnet = new URLSearchParams(location.search).get('magnet');
  if (magnet) {
    const normalized = normalizeMagnet(magnet);
    if (normalized) {
      const dn = /[?&]dn=([^&]+)/.exec(normalized);
      play(
        { title: dn ? decodeURIComponent(dn[1]).replace(/\+/g, ' ') : 'Direct magnet', year: 0 },
        { magnet: normalized, title: normalized.slice(0, 90) },
      );
    }
  }
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', init);
} else {
  init();
}
