/**
 * The built-in engine's controls, in the Stack tab.
 *
 * Everything a standalone install needs to go from empty to filing films:
 * an indexer, a download client, two folders, a quality order -- then a
 * library to search for. Owner only; the server answers 404 to anyone else
 * and this renders nothing in that case (stack.js already says why).
 *
 * Secrets are write-only from here: the server never sends a key back, so a
 * field left blank on edit keeps what is stored.
 */

import { apiFetch } from './server.js';
import { el } from './stack.js';

const STATE_LABEL = {
  available: 'On disk', downloading: 'Downloading', requested: 'Wanted',
  missing: 'Not monitored', importing: 'Filing',
};

/** "1080p, 2160p 720p" -> ['1080p','2160p','720p'], unknowns dropped. */
export function parseQualities(text) {
  const known = new Set(['4320p', '2160p', '1440p', '1080p', '1080i', '720p', '576p', '480p']);
  return [...new Set(String(text || '').split(/[\s,]+/).map((q) => q.trim().toLowerCase())
    .filter((q) => known.has(q)))];
}

/** One line for an interactive-search row. */
export function releaseLine(r) {
  const gb = r.size ? `${(r.size / 2 ** 30).toFixed(1)} GB` : '';
  return [r.quality, r.source, r.codec, gb, `${r.seeders ?? 0} seeds`, r.indexer]
    .filter(Boolean).join(' · ');
}

export function libraryLine(it) {
  const state = STATE_LABEL[it.state] || it.state;
  if (it.kind === 'series' && it.total) return `${state} · ${it.have}/${it.total} episodes`;
  return it.quality ? `${state} · ${it.quality}` : state;
}

async function call(path, method = 'GET', body) {
  const res = await apiFetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (res.status === 404 && path === '/api/engine') return null;
  if (!res.ok) throw new Error(data.error || `${path} answered ${res.status}`);
  return data;
}


function input(placeholder, value = '', type = 'text') {
  const i = el('input');
  i.type = type;
  i.placeholder = placeholder;
  i.value = value;
  i.autocomplete = type === 'password' ? 'new-password' : 'off';
  i.spellcheck = false;
  return i;
}

function button(label, cls, onClick) {
  const b = el('button', `btn ${cls || ''}`.trim(), label);
  b.type = 'button';
  b.addEventListener('click', async () => {
    b.disabled = true;
    try { await onClick(); } finally { b.disabled = false; }
  });
  return b;
}

function note(host, text) {
  const p = el('p', 'set-result', text);
  host.append(p);
  return p;
}

export async function renderEngine(host) {
  host.textContent = '';
  // The three reads are independent; one round trip, not three.
  let st, lib, q;
  try {
    [st, lib, q] = await Promise.all([
      call('/api/engine'),
      call('/api/engine/library').catch(() => ({ items: [] })),
      call('/api/engine/queue').catch(() => ({ grabs: [] })),
    ]);
  } catch (e) {
    note(host, `Engine: ${e.message}`);
    return;
  }
  if (!st) return; // not the owner, or the engine is off on this server

  const redraw = () => renderEngine(host);
  host.append(el('p', 'set-label', 'Yarr.It engine'));
  host.append(el('p', `hp hp-${st.health.state}`, st.health.detail || 'Ready'));

  // --- indexers ---
  host.append(el('p', 'set-label', 'Indexers'));
  host.append(el('p', 'set-lede',
    'Any Torznab or Newznab feed — a tracker’s own, Jackett, or a Prowlarr per-indexer feed. '
    + 'Choose “Prowlarr” to search everything an existing Prowlarr knows.'));
  for (const ix of st.indexers) {
    const row = el('div', 'svc');
    row.append(el('b', 'svc-name', ix.name));
    row.append(el('span', 'svc-kind', `${ix.kind}${ix.keySet ? ' · key set' : ''}`));
    const out = el('small', 'stk-meta');
    const acts = el('div', 'svc-actions');
    acts.append(button('Test', 'btn-ghost', async () => {
      const r = await call('/api/engine/test', 'POST', { indexer: ix.id });
      out.textContent = r.detail;
    }));
    acts.append(button('Remove', 'btn-ghost danger', async () => {
      await call(`/api/engine/indexers?id=${encodeURIComponent(ix.id)}`, 'DELETE');
      redraw();
    }));
    row.append(acts, out);
    host.append(row);
  }
  {
    const name = input('Name');
    const kind = el('select');
    for (const k of ['torznab', 'newznab', 'prowlarr']) kind.append(new Option(k, k));
    const url = input('http://jackett:9117/api/v2.0/indexers/all/results/torznab/api');
    const key = input('API key', '', 'password');
    const msg = el('small', 'stk-meta');
    const row = el('div', 'set-row');
    row.append(name, kind, url, key, button('Add indexer', '', async () => {
      try {
        await call('/api/engine/indexers', 'POST',
          { name: name.value, kind: kind.value, url: url.value, apiKey: key.value, enabled: true });
        redraw();
      } catch (e) { msg.textContent = e.message; }
    }));
    host.append(row, msg);
  }

  // --- download client ---
  host.append(el('p', 'set-label', 'Download client'));
  const c = st.clients[0] || {};
  {
    const url = input('http://qbittorrent:8080', c.url || '');
    const user = input('Username', c.username || '');
    const pass = input(c.passwordSet ? 'Password (unchanged)' : 'Password', '', 'password');
    const cat = input('Category', c.category || 'yarrit');
    const msg = el('small', 'stk-meta');
    const row = el('div', 'set-row');
    row.append(url, user, pass, cat, button('Save qBittorrent', '', async () => {
      try {
        const saved = await call('/api/engine/clients', 'POST', {
          id: c.id, name: 'qBittorrent', kind: 'qbittorrent', url: url.value,
          username: user.value, password: pass.value, category: cat.value, enabled: true,
        });
        // Later saves from this panel must update this client, not add another.
        c.id = saved.id;
        const t = await call('/api/engine/test', 'POST', { client: saved.id });
        msg.textContent = t.detail;
      } catch (e) { msg.textContent = e.message; }
    }));
    host.append(row, msg);
  }

  // --- folders & quality ---
  host.append(el('p', 'set-label', 'Folders & quality'));
  {
    const s = st.settings;
    const movie = input('Films folder, e.g. /media/movies', s.movieRoot || '');
    const series = input('Series folder, e.g. /media/tv', s.seriesRoot || '');
    const dl = input('Downloads, as Yarr.It sees them', s.downloadPath || '');
    const rdl = input('Downloads, as qBittorrent names them', s.remoteDownloadPath || '');
    const quals = input('Qualities, best first', (s.profile.qualities || []).join(', '));
    const cutoff = input('Stop upgrading at', s.profile.cutoff || '');
    const prefer = el('input');
    prefer.type = 'checkbox';
    prefer.checked = !!s.preferEngine;
    const preferLabel = el('label', 'set-check');
    preferLabel.append(prefer, el('span', '', ' Send requests to the engine even when Radarr/Sonarr are connected'));
    const msg = el('small', 'stk-meta');
    const grid = el('div', 'eng-grid');
    grid.append(movie, series, dl, rdl, quals, cutoff);
    host.append(grid, preferLabel, button('Save', '', async () => {
      try {
        await call('/api/engine/settings', 'PUT', {
          ...s, movieRoot: movie.value, seriesRoot: series.value, downloadPath: dl.value,
          remoteDownloadPath: rdl.value, preferEngine: prefer.checked,
          profile: { ...s.profile, qualities: parseQualities(quals.value), cutoff: cutoff.value.trim().toLowerCase() },
        });
        msg.textContent = 'Saved.';
      } catch (e) { msg.textContent = e.message; }
    }), msg);
  }

  // --- library ---
  host.append(el('p', 'set-label', 'Library'));
  {
    const q = input('Add a film or series…');
    const results = el('div', 'eng-results');
    const row = el('div', 'set-row');
    row.append(q, button('Find', '', async () => {
      results.textContent = '';
      try {
        const r = await call(`/api/engine/lookup?q=${encodeURIComponent(q.value)}`);
        for (const h of r.results.slice(0, 12)) {
          const line = el('div', 'stk-rq');
          line.append(el('b', '', `${h.title}${h.year ? ` (${h.year})` : ''}`), el('small', 'stk-meta', h.kind));
          line.append(button('Add & search', 'btn-ghost', async () => {
            await call('/api/engine/library', 'POST', { canonicalId: h.canonicalId, search: true });
            redraw();
          }));
          results.append(line);
        }
        if (!r.results.length) results.append(el('small', 'stk-meta', 'Nothing found.'));
      } catch (e) { results.append(el('small', 'stk-meta', e.message)); }
    }));
    host.append(row, results);
  }
  for (const it of lib.items) {
    const row = el('div', 'stk-rq');
    const top = el('div', 'stk-rq-top');
    top.append(el('b', '', `${it.title}${it.year ? ` (${it.year})` : ''}`), el('span', 'stk-st', libraryLine(it)));
    row.append(top);
    const out = el('div', 'eng-results');
    const acts = el('div', 'svc-actions');
    acts.append(button('Search now', 'btn-ghost', async () => {
      const r = await call('/api/engine/search', 'POST', { id: it.id, auto: true });
      out.textContent = r.grabbed ? `Grabbed ${r.grabbed}.` : (r.detail || 'Nothing fit the profile yet.');
    }));
    if (it.kind === 'movie') {
      acts.append(button('Releases', 'btn-ghost', async () => {
        out.textContent = 'Asking every indexer…';
        const r = await call('/api/engine/search', 'POST', { id: it.id });
        out.textContent = '';
        if (r.failed?.length) out.append(el('small', 'stk-meta', `Could not ask: ${r.failed.join('; ')}`));
        for (const rel of r.releases.slice(0, 25)) {
          const line = el('div', `eng-rel${rel.rejected?.length ? ' off' : ''}`);
          line.append(el('span', '', rel.title), el('small', 'stk-meta', releaseLine(rel)));
          if (rel.rejected?.length) line.append(el('small', 'stk-why', rel.rejected.join('; ')));
          line.append(button('Grab', 'btn-ghost', async () => {
            await call('/api/engine/grab', 'POST', { id: it.id, release: rel });
            redraw();
          }));
          out.append(line);
        }
        if (!r.releases.length) out.append(el('small', 'stk-meta', 'No releases found.'));
      }));
    }
    acts.append(button('Remove', 'btn-ghost danger', async () => {
      await call(`/api/engine/library?id=${encodeURIComponent(it.id)}`, 'DELETE');
      redraw();
    }));
    row.append(acts, out);
    host.append(row);
  }
  if (!lib.items.length) host.append(el('p', 'set-lede', 'Nothing in the library yet.'));

  // --- recent activity ---
  if (q.grabs.length) {
    host.append(el('p', 'set-label', 'Recent grabs'));
    for (const g of q.grabs.slice(-10).reverse()) {
      const pct = g.state === 'downloading' && g.progress ? ` ${Math.round(g.progress * 100)}%` : '';
      host.append(el('small', 'stk-meta', `${g.state}${pct} · ${g.release}${g.detail ? ` — ${g.detail}` : ''}`));
    }
  }
}
