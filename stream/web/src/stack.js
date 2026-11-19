/**
 * The owner's stack: which service answers each kind of media, and the
 * household's request queue across Seerr and GG Requestz.
 *
 * Server-side, owner only. The other half of settings ("Your services") is a
 * stranger's own services in their own browser; this half is the instance's
 * own configuration, so it asks the server and shows nothing to anyone the
 * server does not recognise as the owner. A 404 from these endpoints is the
 * owner boundary's constant answer -- it is rendered as "sign in as the owner",
 * never as an error, because for everyone else it is the correct result.
 *
 * Pure functions are exported for tests; renderStack is the only DOM entry.
 */

import { apiFetch } from './server.js';

export const MODE_LABEL = {
  unified: 'Standalone — Yarr.It does it all itself',
  'bring-your-own': 'Your own services',
  mixed: 'Both — your services first, Yarr.It’s engine behind them',
  empty: 'Nothing connected',
};

const DOMAIN_LABEL = {
  video: 'Films & series', music: 'Music', game: 'Games', literature: 'Books',
  comic: 'Comics', image: 'Images',
};

const STATUS_LABEL = {
  pending: 'Waiting', approved: 'Approved', processing: 'Downloading',
  available: 'Available', declined: 'Declined', failed: 'Failed', cancelled: 'Cancelled',
};

/** Rows for the topology table: one per domain, acquirers in routing order. */
export function stackRows(stack, providers = []) {
  const health = new Map(providers.map((p) => [p.id, p.health || {}]));
  const withHealth = (list = []) => list.map((m) => ({ ...m, state: health.get(m.id)?.state || 'unknown' }));
  return Object.entries(stack?.domains || {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([domain, d]) => ({
      domain,
      label: DOMAIN_LABEL[domain] || domain,
      mode: d.mode,
      modeLabel: MODE_LABEL[d.mode] || d.mode,
      acquire: withHealth(d.roles?.acquisition),
      requests: withHealth(d.roles?.requests),
    }));
}

/** Request rows, with the one sentence that explains a missing button. */
export function requestRows(view) {
  return (view?.items || []).map((r) => ({
    id: r.id,
    title: r.year ? `${r.title} (${r.year})` : r.title,
    who: r.requestedBy || '',
    status: r.status,
    statusLabel: STATUS_LABEL[r.status] || r.status,
    detail: r.detail || '',
    artwork: r.artwork || '',
    source: r.providerId,
    decidable: !!r.decidable,
    why: r.decidable || r.status !== 'pending'
      ? ''
      : `Approve this in ${r.providerId === 'ggrequestz' ? 'GG Requestz' : r.providerId} — it does not let Yarr.It decide.`,
  }));
}

async function getJSON(path) {
  const res = await apiFetch(path);
  if (res.status === 404) return { owner: false };
  if (!res.ok) throw new Error(`${path} answered ${res.status}`);
  return { owner: true, body: await res.json() };
}

export async function loadStack() {
  const [stack, providers, requests] = await Promise.all([
    getJSON('/api/stack'),
    getJSON('/api/providers'),
    getJSON('/api/requests?status=all&limit=100'),
  ]);
  if (!stack.owner) return { owner: false };
  return {
    owner: true,
    stack: stack.body,
    providers: providers.body?.providers || [],
    requests: requests.owner ? requests.body : { items: [] },
  };
}

export async function decide(id, action) {
  const res = await apiFetch('/api/requests/decide', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, action }),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `decide answered ${res.status}`);
  return body;
}

export function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}

function member(m) {
  const n = el('span', `stk-m hp hp-${m.state}`, m.name);
  if (m.engine) n.append(el('small', 'stk-eng', ' built in'));
  return n;
}

/** Paint the Stack pane into host. Re-callable; it clears host first. */
export async function renderStack(host) {
  host.textContent = '';
  host.append(el('p', 'set-result', 'Asking the server…'));
  let data;
  try {
    data = await loadStack();
  } catch (e) {
    host.textContent = '';
    host.append(el('p', 'set-result', `Could not read the stack: ${e.message}`));
    return;
  }
  host.textContent = '';
  if (!data.owner) {
    host.append(el('p', 'set-lede',
      'Sign in as this server’s owner to see and run its stack. Nobody else is shown '
      + 'what it runs or who asked for what.'));
    return;
  }

  host.append(el('p', 'set-label', MODE_LABEL[data.stack.mode] || data.stack.mode));
  const table = el('div', 'stk-table');
  for (const row of stackRows(data.stack, data.providers)) {
    const r = el('div', 'stk-row');
    r.append(el('b', 'stk-domain', row.label));
    const who = el('span', 'stk-who');
    if (!row.acquire.length) who.append(el('span', 'stk-none', 'nothing obtains these'));
    row.acquire.forEach((m) => who.append(member(m)));
    r.append(who);
    if (row.requests.length) {
      const q = el('span', 'stk-req');
      q.append(el('small', '', 'requests via '));
      row.requests.forEach((m) => q.append(member(m)));
      r.append(q);
    }
    table.append(r);
  }
  host.append(table);

  host.append(el('div', 'set-sep'));
  host.append(el('p', 'set-label', 'Requests'));
  const failed = data.requests.failed || [];
  if (failed.length) {
    host.append(el('p', 'set-result', `Could not ask: ${failed.join(', ')}. The list below is short by those.`));
  }
  const rows = requestRows(data.requests);
  if (!rows.length) host.append(el('p', 'set-lede', 'Nobody is waiting on anything.'));
  for (const rq of rows) {
    const n = el('div', `stk-rq st-${rq.status}`);
    const top = el('div', 'stk-rq-top');
    top.append(el('b', '', rq.title));
    top.append(el('span', `stk-st st-${rq.status}`, rq.statusLabel));
    n.append(top);
    const meta = [rq.who && `asked by ${rq.who}`, rq.detail, rq.source].filter(Boolean).join(' · ');
    if (meta) n.append(el('small', 'stk-meta', meta));
    if (rq.decidable) {
      const acts = el('div', 'svc-actions');
      for (const [action, label, cls] of [['approve', 'Approve', ''], ['decline', 'Decline', 'danger']]) {
        const b = el('button', `btn btn-ghost ${cls}`.trim(), label);
        b.type = 'button';
        b.addEventListener('click', async () => {
          acts.querySelectorAll('button').forEach((x) => { x.disabled = true; });
          try {
            await decide(rq.id, action);
            renderStack(host);
          } catch (e) {
            n.append(el('p', 'set-result', e.message));
            acts.querySelectorAll('button').forEach((x) => { x.disabled = false; });
          }
        });
        acts.append(b);
      }
      n.append(acts);
    } else if (rq.why) {
      n.append(el('small', 'stk-why', rq.why));
    }
    host.append(n);
  }
}
