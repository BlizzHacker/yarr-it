/**
 * The window into the relay, and the switch that turns it off.
 *
 * A relay into someone's private network that they cannot see into, or cannot
 * revoke without uninstalling, is not something to ship. This page is the whole
 * visible surface: what is allowed, who may ask, and a Revoke next to each.
 *
 * It is also the only place `chrome.permissions.request` can be called from
 * with any reliability. Chrome requires a user gesture, and a gesture only
 * exists in an extension surface -- not in a web page, and not in the service
 * worker, which has no window to have been clicked in.
 */

import { BUILTIN_ORIGINS, describeTarget, hostPattern, normaliseOrigin } from './relay-core.js';

const $ = (s) => document.querySelector(s);

/**
 * What this page lists is the extension's own allowlist, not chrome.permissions.
 *
 * Two measured reasons, both of which make chrome.permissions the wrong source
 * of truth for a screen whose whole job is "here is what it may reach, and here
 * is how to stop it":
 *
 *   `permissions.getAll()` returns content-script match patterns as well as
 *   granted hosts, so this extension reports `*://*&#47;*` -- it puts a ▶ on
 *   torrent sites -- and the list would claim the relay may reach the web.
 *
 *   `permissions.remove()` throws "You cannot remove required permissions" for
 *   any origin overlapping those same matches, which is all of them. Revoke
 *   would be a button that does nothing.
 */
const HOSTS_KEY = 'relayHosts';

async function allowedHosts() {
  const { [HOSTS_KEY]: v } = await chrome.storage.sync.get({ [HOSTS_KEY]: [] });
  return Array.isArray(v) ? v.filter((p) => typeof p === 'string') : [];
}

async function setAllowedHosts(list) {
  await chrome.storage.sync.set({ [HOSTS_KEY]: [...new Set(list)].sort() });
}

function patternLabel(p) {
  // "http://192.168.0.26/*" reads better as "http://192.168.0.26 (any port)",
  // because the trailing /* implies a path restriction that does not exist.
  return p.replace(/\/\*$/, '');
}

function say(el, text, ok = true) {
  el.textContent = text;
  el.className = `msg show ${ok ? 'ok' : 'err'}`;
  clearTimeout(say._t);
  say._t = setTimeout(() => { el.className = 'msg'; }, 6000);
}

function row(label, tag, buttons) {
  const li = document.createElement('li');
  const span = document.createElement('span');
  span.className = 'host';
  span.textContent = label;
  li.append(span);
  if (tag) {
    const t = document.createElement('span');
    t.className = 'tag';
    t.textContent = tag;
    li.append(t);
  }
  for (const b of buttons) li.append(b);
  return li;
}

function button(text, cls, onClick) {
  const b = document.createElement('button');
  b.textContent = text;
  if (cls) b.className = cls;
  b.addEventListener('click', onClick);
  return b;
}

/**
 * Rebuild a list, and only commit if nothing newer started meanwhile.
 *
 * Granting fires `permissions.onAdded` *and* returns to the code that asked, so
 * two renders run at once. Each clears the list, then awaits, then appends --
 * and interleaved, that is clear, clear, append, append: every row shown twice.
 * Observed on screen the first time a host was allowed, which is exactly the
 * moment the user is looking at this page to check what it did.
 */
const generation = new Map();
async function paint(selector, build) {
  const gen = (generation.get(selector) || 0) + 1;
  generation.set(selector, gen);
  const rows = await build();
  if (generation.get(selector) !== gen) return; // a newer render won
  const list = $(selector);
  list.textContent = '';
  for (const r of rows) list.append(r);
}

function emptyRow(text) {
  const li = document.createElement('li');
  li.className = 'empty';
  li.textContent = text;
  return li;
}

// ------------------------------------------------------- granted services --

function renderGranted() {
  return paint('#granted', async () => {
    const hosts = await allowedHosts();
    if (!hosts.length) {
      return [emptyRow('Nothing yet. Yarr.It cannot reach anything on your network.')];
    }
    return hosts.map((o) => row(patternLabel(o), 'any port', [
      button('Revoke', 'danger', () => revoke(o)),
    ]));
  });
}

/** Ask Chrome for a host. Must run inside the click, or the prompt is refused. */
async function grant(pattern, msgEl) {
  try {
    const ok = await chrome.permissions.request({ origins: [pattern] });
    if (ok) await setAllowedHosts([...(await allowedHosts()), pattern]);
    say(msgEl, ok
      ? `Yarr.It can now reach ${patternLabel(pattern)}.`
      : 'You said no, so nothing changed.', ok);
    renderAll();
    return ok;
  } catch (e) {
    say(msgEl, e?.message || 'Chrome refused that request.', false);
    return false;
  }
}

/**
 * Stop the relay reaching a host.
 *
 * Our list comes off first and that is what actually closes the door -- it is
 * the gate the relay checks, and removing from it cannot fail. Handing the
 * underlying permission back to Chrome is attempted afterwards and is allowed
 * to fail, because for this extension it always does: the ▶ content script
 * matches every site, so Chrome calls every host permission required.
 */
async function revoke(pattern) {
  await setAllowedHosts((await allowedHosts()).filter((x) => x !== pattern));
  let handedBack = false;
  try {
    handedBack = await chrome.permissions.remove({ origins: [pattern] });
  } catch { handedBack = false; }
  say($('#host-msg'), handedBack
    ? `Revoked ${patternLabel(pattern)}.`
    : `Revoked ${patternLabel(pattern)} — Yarr.It will not reach it again. `
      + 'Chrome keeps the underlying site access because Yarr.It already runs on every '
      + 'site to add ▶ buttons; turn those off in the popup to drop it entirely.');
  renderAll();
}

$('#add-host').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const raw = $('#host-input').value.trim();
  const origin = normaliseOrigin(raw);
  const pattern = origin && hostPattern(origin + '/');
  if (!pattern) {
    say($('#host-msg'), 'That does not look like an address Chrome can grant.', false);
    return;
  }
  if (await grant(pattern, $('#host-msg'))) $('#host-input').value = '';
});

// ------------------------------------------------------ pending requests ---

function renderPending() {
  return paint('#pending', async () => {
    let pending = [];
    try {
      const got = await chrome.storage.session.get({ relayPending: [] });
      pending = Array.isArray(got.relayPending) ? got.relayPending : [];
    } catch { pending = []; }

    // Something already allowed is not pending, whatever the record says.
    const hosts = await allowedHosts();
    const live = pending.filter((p) => !hosts.includes(p.pattern));

    $('#pending-section').hidden = live.length === 0;
    return live.map((p) => row(p.label || patternLabel(p.pattern), null, [
      button('Allow', 'primary', () => grant(p.pattern, $('#host-msg'))),
      button('Dismiss', 'danger', async () => {
        const got = await chrome.storage.session.get({ relayPending: [] });
        const next = (got.relayPending || []).filter((x) => x.pattern !== p.pattern);
        await chrome.storage.session.set({ relayPending: next });
        chrome.action.setBadgeText({ text: next.length ? String(next.length) : '' });
        renderAll();
      }),
    ]));
  });
}

// --------------------------------------------------------- trusted pages ---

async function extraOrigins() {
  const { relayOrigins = [] } = await chrome.storage.sync.get({ relayOrigins: [] });
  return Array.isArray(relayOrigins) ? relayOrigins.map(normaliseOrigin).filter(Boolean) : [];
}

async function saveOrigins(list) {
  await chrome.storage.sync.set({ relayOrigins: [...new Set(list)] });
  // The worker owns script registration; storage alone would not un-inject a
  // page the user just removed.
  try { await chrome.runtime.sendMessage({ type: 'yarrit:relay:sync' }); } catch { /* asleep */ }
}

function renderOrigins() {
  return paint('#origins', async () => [
    ...BUILTIN_ORIGINS.map((o) => row(o, 'built in', [])),
    ...(await extraOrigins()).map((o) => row(o, 'yours', [
      button('Remove', 'danger', async () => {
        await saveOrigins((await extraOrigins()).filter((x) => x !== o));
        say($('#origin-msg'), `${o} can no longer use the relay.`);
        renderAll();
      }),
    ])),
  ]);
}

$('#add-origin').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const origin = normaliseOrigin($('#origin-input').value);
  if (!origin) {
    say($('#origin-msg'), 'That does not look like a page address.', false);
    return;
  }
  if (BUILTIN_ORIGINS.includes(origin)) {
    say($('#origin-msg'), `${origin} is already trusted.`);
    return;
  }
  // Trusting a page means injecting into it, and injection needs the host
  // permission -- so the same click has to buy both. Requested first because it
  // is the part that can be refused.
  const pattern = hostPattern(origin + '/');
  let ok = false;
  try {
    ok = await chrome.permissions.request({ origins: [pattern] });
  } catch (e) {
    say($('#origin-msg'), e?.message || 'Chrome refused that request.', false);
    return;
  }
  if (!ok) {
    say($('#origin-msg'), 'Yarr.It needs access to that site to run there, so nothing changed.', false);
    return;
  }
  await saveOrigins([...(await extraOrigins()), origin]);
  say($('#origin-msg'), `${describeTarget(origin)} may now use the relay.`);
  $('#origin-input').value = '';
  renderAll();
});

// ------------------------------------------------------------------ boot ---

function renderAll() {
  renderGranted();
  renderPending();
  renderOrigins();
}

chrome.permissions.onAdded.addListener(renderAll);
chrome.permissions.onRemoved.addListener(renderAll);
// A page asking for a new host is exactly when someone is sitting on this
// screen waiting for it to appear. Without this the list is whatever it was
// when the tab opened, and the request seems to have gone nowhere.
chrome.storage.onChanged.addListener((changes, area) => {
  if (area === 'session' && changes.relayPending) renderPending();
  if (area === 'sync' && changes.relayOrigins) renderOrigins();
  if (area === 'sync' && changes[HOSTS_KEY]) { renderGranted(); renderPending(); }
});
renderAll();
