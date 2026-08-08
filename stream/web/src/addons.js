/**
 * Addons: paste a URL, see what it provides, turn it on or off, reorder.
 *
 * WHY THIS IS THE SCREEN THAT MATTERS
 *
 * Yarr.It speaks the Stremio addon protocol, so hundreds of addons that already
 * exist work here on day one -- in a browser, with nothing installed. This file
 * is where a person actually does that, so it has one job beyond the buttons:
 * be honest about what each addon is. Stremio shows an installed addon as a
 * name and a logo, and everything else about it -- whether it can search,
 * whether it will expose your IP to a swarm, whether its URL contains your paid
 * account key -- is something you find out later. All of that is known at
 * install time and is shown here.
 *
 * WHAT IS NOT DECIDED HERE
 *
 * The addon list lives on the server, not in localStorage. That is deliberate
 * and it is the opposite of services.js, which keeps a stranger's Radarr key in
 * their own browser and never sends it anywhere. The reason for the difference:
 * the server is the thing that fetches an addon, so the server has to hold the
 * list, and an addon list that lived only in a browser would mean a TV could
 * never see it.
 *
 * Health is not invented here either. The six states come from schema.json and
 * are deliberately not collapsed into "down": each implies a different fix, and
 * services.js already writes the sentence for each one, so it is imported
 * rather than reworded. A second vocabulary for the same six states is how two
 * screens end up disagreeing about whether something is broken.
 */

import { labelFor, canonicalDomain } from './schema.js';
import { HEALTH_LABELS, healthLabel } from './services.js';

const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

// ------------------------------------------------------------------ the URL

/**
 * Clean up whatever a person pasted into the box.
 *
 * Returns `{ url, error }`. One of the two is always empty. This differs from
 * normaliseServiceURL, which returns a bare string, and the difference is
 * earned: a service address is typed from memory and a typo is obvious, while
 * an addon URL is pasted from someone else's install button and the reason it
 * was rejected is not guessable. "That does not look like an address" is a
 * dead end when the real answer is "that is a stremio:// link, which is fine,
 * we converted it".
 *
 * The three shapes that actually arrive:
 *
 *   stremio://v3-cinemeta.strem.io/manifest.json   an Install button
 *   https://v3-cinemeta.strem.io/manifest.json     copied from the address bar
 *   v3-cinemeta.strem.io                           typed from memory
 */
export function normaliseAddonURL(raw) {
  if (raw == null) return { url: '', error: 'Paste an addon address.' };
  let s = String(raw).trim();
  if (!s) return { url: '', error: 'Paste an addon address.' };

  // Install buttons on real addon pages emit stremio:// links -- confirmed on
  // Torrentio's configure page and OpenSubtitles' landing page, both of which
  // contain stremio:// hrefs. Someone clicking "Install" and pasting the result
  // here has done nothing wrong, and the only thing separating that string from
  // a working URL is the scheme.
  if (/^stremio:\/\//i.test(s)) s = 'https://' + s.slice('stremio://'.length);

  // Host-and-port has to be settled before anything reads a colon as a scheme
  // separator, or "nas:11470" is rejected as an unknown protocol called "nas".
  const hostPort = /^[a-z][a-z0-9+.-]*:\d+([/?#]|$)/i.test(s);
  const scheme = hostPort ? null : /^([a-z][a-z0-9+.-]*):/i.exec(s);
  if (scheme) {
    const proto = scheme[1].toLowerCase();
    if (proto === 'ipfs' || proto === 'ipns') {
      // A real transport in the protocol, and genuinely not implemented on the
      // server. Saying so beats "invalid address", which sends someone looking
      // for a typo that is not there.
      return { url: '', error: 'IPFS addons are not supported yet. Use an http:// or https:// address.' };
    }
    if (proto !== 'http' && proto !== 'https') {
      return { url: '', error: 'Only http:// and https:// addresses work here.' };
    }
  } else {
    s = 'https://' + s;
  }

  let u;
  try {
    u = new URL(s);
  } catch {
    return { url: '', error: 'That does not look like an address.' };
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') {
    return { url: '', error: 'Only http:// and https:// addresses work here.' };
  }
  if (!u.hostname) return { url: '', error: 'That address has no host.' };
  if (u.username || u.password) {
    // Would be sent to whatever that host resolves to. The server refuses these
    // too; catching it here means the explanation arrives before the round trip.
    return { url: '', error: 'Remove the username and password from the address. Addons are not authenticated that way.' };
  }

  // Configuration lives in the path for configurable addons, so the path is
  // kept in full and only the manifest filename is normalised onto the end.
  let path = u.pathname.replace(/\/+$/, '');
  if (!/\/manifest\.json$/i.test(path)) path += '/manifest.json';

  // Query and hash are never part of an addon address, and carrying them is how
  // a tracking parameter ends up stored as part of an identity.
  return { url: u.origin + path, error: '' };
}

/**
 * Does this URL carry configuration -- which, for a configurable addon, is
 * where the key is?
 *
 * Confirmed on the wire: https://torrentio.strem.fun/providers=yts/manifest.json
 * answers 200 with a manifest of its own. That is the mechanism by which a paid
 * debrid key ends up inside an addon URL, and it is why this is worth a warning
 * on screen before anyone shares the link.
 */
export function isConfiguredAddonURL(raw) {
  try {
    const u = new URL(String(raw));
    const path = u.pathname.replace(/\/+$/, '').replace(/\/manifest\.json$/i, '');
    return path.replace(/^\/+/, '') !== '';
  } catch {
    return false;
  }
}

/** The host, for a row that must not print a key into the page. */
export function addonHost(raw) {
  try {
    return new URL(String(raw)).host;
  } catch {
    return '';
  }
}

// ------------------------------------------------------------- describing it

/**
 * One sentence saying what this addon actually does.
 *
 * Built from the roles and capabilities the server bridged out of the manifest,
 * not from the addon's own description -- descriptions are marketing, and
 * Torrentio's is a 400-character list of scraper names.
 */
export function describeAddon(a) {
  if (!a) return '';
  const can = [];
  if (a.capabilities?.includes('search')) can.push('search');
  else if (a.roles?.includes('discovery')) can.push('browse');
  if (a.capabilities?.includes('details')) can.push('details');
  if (a.capabilities?.includes('stream')) can.push('streams');
  if (a.resources?.includes('subtitles')) can.push('subtitles');

  const domains = (a.domains || []).map((d) => labelFor(d)).filter(Boolean);

  // Domains are checked first, and the order matters. An addon can be a
  // perfectly good searchable catalogue of podcasts and still have nowhere to
  // put a single result here, so "search" would be true and useless. What the
  // reader needs to know is whether anything will appear.
  if (!domains.length) return 'Nothing this version can use.';
  if (!can.length) return `Nothing to show for ${domains.join(', ')} yet.`;
  return `${can.join(', ')} for ${domains.join(', ')}`;
}

/**
 * Everything a person should be told before this addon is left switched on.
 *
 * Each of these is knowable from the manifest at install time and is exactly
 * what a name-and-logo row hides. They are warnings, not errors: an addon may
 * be all three and still be the one somebody wants.
 */
export function addonWarnings(a) {
  if (!a) return [];
  const out = [];
  if (a.configured) {
    out.push({
      kind: 'configured',
      text: 'This addon’s address contains your settings, and for some addons that includes a paid account key. Do not share the link.',
    });
  }
  if (a.p2p) {
    out.push({
      kind: 'p2p',
      text: 'This addon serves peer-to-peer streams. Playing one exposes your IP address to everyone else in the swarm.',
    });
  }
  if (a.adultContent) {
    out.push({ kind: 'adult', text: 'This addon includes adult content.' });
  }
  if (a.unsupportedTypes?.length) {
    // The alternative is that those items silently never appear, which looks
    // exactly like a broken addon and gets reported as one.
    out.push({
      kind: 'unsupported',
      text: `Yarr.It cannot show these content types yet: ${a.unsupportedTypes.join(', ')}. Everything else from this addon still works.`,
    });
  }
  return out;
}

/**
 * Which of the six health states should stop someone using this addon.
 *
 * `degraded` is deliberately not in here. An addon that serves one type we
 * cannot place and four we can is still worth having on, and greying it out
 * would remove a working thing over a partial complaint.
 */
export function addonIsUsable(a) {
  const s = a?.health?.state;
  return s === 'healthy' || s === 'degraded';
}

// ------------------------------------------------------------------ ordering

/**
 * Move one addon up or down, returning the new id order.
 *
 * Pure, and returns ids rather than mutating rows, because the server's reorder
 * endpoint takes ids and a UI that reorders its own array and then posts a
 * different thing is a UI with two orders in it.
 */
export function moveAddon(addons, id, delta) {
  const ids = (addons || []).map((a) => a.id);
  const from = ids.indexOf(id);
  if (from < 0 || !delta) return ids;
  const to = Math.min(ids.length - 1, Math.max(0, from + delta));
  if (to === from) return ids;
  ids.splice(to, 0, ids.splice(from, 1)[0]);
  return ids;
}

// ---------------------------------------------------------------- the client

/**
 * The addon endpoints, as a small object the UI can hold.
 *
 * `credentials: 'same-origin'` on every call and no way to override it: these
 * routes are session-gated, and the session belongs to the instance that issued
 * it. This mirrors apiFetch in server.js rather than reimplementing the rule.
 */
export function createAddonAPI({ fetchImpl = fetch, base = '' } = {}) {
  const url = (p) => (base ? base.replace(/\/+$/, '') + p : p);

  async function send(path, options = {}) {
    let res;
    try {
      res = await fetchImpl(url(path), {
        credentials: base ? 'omit' : 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        ...options,
      });
    } catch (e) {
      // A network failure and a rejected addon are different problems with
      // different fixes, so they must not arrive as the same string.
      return { ok: false, error: `Could not reach your Yarr.It server. ${e?.message || ''}`.trim() };
    }
    let body = null;
    try {
      body = await res.json();
    } catch {
      body = null;
    }
    if (!res.ok) {
      if (res.status === 401) {
        return { ok: false, needsSignIn: true, error: 'Sign in to manage addons.' };
      }
      return { ok: false, error: body?.error || `The server answered ${res.status}.` };
    }
    return { ok: true, body: body || {} };
  }

  return {
    async list() {
      const r = await send('/api/addons');
      if (!r.ok) return { ...r, addons: [] };
      return { ok: true, addons: r.body.addons || [], domains: r.body.domains || [], limits: r.body.limits || {} };
    },

    async add(raw) {
      // Normalised here as well as on the server. Not duplication for its own
      // sake: this is what turns a pasted stremio:// link into something the
      // server will accept, and it is what puts the reason on screen without a
      // round trip.
      const { url: clean, error } = normaliseAddonURL(raw);
      if (error) return { ok: false, error };
      const r = await send('/api/addons', { method: 'POST', body: JSON.stringify({ url: clean }) });
      if (!r.ok) return r;
      return { ok: true, addon: r.body.addon };
    },

    remove(id) {
      return send('/api/addons/remove', { method: 'POST', body: JSON.stringify({ id }) });
    },

    setEnabled(id, enabled) {
      return send('/api/addons/enabled', { method: 'POST', body: JSON.stringify({ id, enabled: !!enabled }) });
    },

    reorder(ids) {
      return send('/api/addons/order', { method: 'POST', body: JSON.stringify({ ids }) });
    },

    async search(query, domain = '') {
      const q = new URLSearchParams({ q: query });
      const d = canonicalDomain(domain);
      if (d) q.set('domain', d);
      const r = await send(`/api/addons/search?${q}`);
      if (!r.ok) return { ...r, items: [], failed: [] };
      // `failed` is carried through rather than dropped. Without it, a short
      // list and a broken addon look identical, and "no results" is the most
      // misleading thing a search can say when the truth is "we could not ask".
      return { ok: true, items: r.body.items || [], failed: r.body.failed || [] };
    },
  };
}

// ------------------------------------------------------------------ the view

/**
 * Draw the installed list into `host`.
 *
 * Rows are built with textContent throughout. Addon names, descriptions and
 * URLs are all third-party strings, and this is the one screen in the product
 * whose entire content comes from a server somebody else runs.
 */
export function renderAddons(host, { addons = [], handlers = {}, limits = {} } = {}) {
  host.replaceChildren();

  if (!addons.length) {
    const empty = el('p', 'addons-empty',
      'No addons yet. Paste an addon’s manifest address above — most addon pages have an Install button you can copy the link from.');
    host.append(empty);
    return host;
  }

  addons.forEach((a, i) => {
    const row = el('div', 'addon-row');
    row.dataset.id = a.id;
    row.dataset.health = a.health?.state || 'unknown';
    if (!a.enabled) row.dataset.disabled = 'true';

    const head = el('div', 'addon-head');
    head.append(el('span', 'addon-name', a.name || a.id));
    if (a.version) head.append(el('span', 'addon-version', `v${a.version}`));

    // The state, in the words schema.json already has. Never a red dot: a
    // rejected key and an unplugged switch look identical from a colour.
    const pill = el('span', 'addon-health', healthLabel(a.health?.state));
    pill.dataset.state = a.health?.state || 'unknown';
    head.append(pill);
    row.append(head);

    row.append(el('p', 'addon-does', describeAddon(a)));

    // The host, never the full URL. The path is where the key is.
    if (a.safeUrl || a.url) row.append(el('p', 'addon-host', addonHost(a.safeUrl || a.url)));

    if (a.health?.detail) row.append(el('p', 'addon-detail', a.health.detail));

    for (const w of addonWarnings(a)) {
      const note = el('p', 'addon-warning', w.text);
      note.dataset.kind = w.kind;
      row.append(note);
    }

    const actions = el('div', 'addon-actions');

    const toggle = el('button', 'addon-toggle', a.enabled ? 'Turn off' : 'Turn on');
    toggle.type = 'button';
    toggle.dataset.action = 'toggle';
    toggle.addEventListener('click', () => handlers.onToggle?.(a.id, !a.enabled));
    actions.append(toggle);

    // Order is a real preference: the first addon that answers a query is the
    // one whose results a viewer sees first.
    const up = el('button', 'addon-up', 'Move up');
    up.type = 'button';
    up.dataset.action = 'up';
    up.disabled = i === 0;
    up.addEventListener('click', () => handlers.onMove?.(a.id, -1));
    actions.append(up);

    const down = el('button', 'addon-down', 'Move down');
    down.type = 'button';
    down.dataset.action = 'down';
    down.disabled = i === addons.length - 1;
    down.addEventListener('click', () => handlers.onMove?.(a.id, 1));
    actions.append(down);

    const remove = el('button', 'addon-remove', 'Remove');
    remove.type = 'button';
    remove.dataset.action = 'remove';
    remove.addEventListener('click', () => handlers.onRemove?.(a.id));
    actions.append(remove);

    row.append(actions);
    host.append(row);
  });

  if (limits.maxInstalled && addons.length >= limits.maxInstalled) {
    host.append(el('p', 'addons-limit',
      `That is the maximum of ${limits.maxInstalled} addons. Remove one before adding another.`));
  }
  return host;
}

export { HEALTH_LABELS, healthLabel };
