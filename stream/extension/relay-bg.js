/**
 * The relay itself: the only place in the extension that touches a home network.
 *
 * A service worker is not a document, so the mixed-content rule that blocks
 * https://yarrit.com from reaching http://192.168.0.26 does not apply to the
 * fetch made here. That is the entire unlock, and it is also why this file is
 * the one to be paranoid in. Four gates, in order, and a request has to clear
 * all four:
 *
 *   1. the page.    `sender.origin` -- set by Chrome, not by the page -- must be
 *                   yarrit.com or an instance the user added themselves.
 *   2. the request.  validateRequest() in relay-core.js; scheme, method, headers.
 *   3. the host.     the user must have granted this scheme+host at runtime,
 *                    through a real prompt, from a real click.
 *   4. the answer.   a redirect that leaves the granted host does not come back.
 *
 * Nothing here logs a URL, a header or a body. Radarr accepts `?apikey=` in the
 * query string and every one of these services authenticates with a header, so
 * a debug line in this file is a key in a log file.
 */

import {
  RELAY_INTERNAL, BUILTIN_ORIGINS, MAX_CONCURRENT, MAX_QUEUE, RELAY_TIMEOUT_MS,
  createLimiter, describeTarget, hostPattern, isTrustedOrigin, normaliseOrigin,
  permissionNeededMessage, sanitiseResponseHeaders, validateRequest, withTimeout,
} from './relay-core.js';

const ORIGINS_KEY = 'relayOrigins';   // sync: extra pages allowed to drive us
const HOSTS_KEY = 'relayHosts';       // sync: hosts the relay may fetch from
const PENDING_KEY = 'relayPending';   // session: hosts waiting on a grant

const limiter = createLimiter({ max: MAX_CONCURRENT, maxQueue: MAX_QUEUE });

// -------------------------------------------------------- allowed hosts ---

/**
 * The extension's own list of hosts the relay may reach, and the reason it
 * exists rather than just asking Chrome.
 *
 * `chrome.permissions.remove()` refuses with "You cannot remove required
 * permissions" for any origin that overlaps a manifest content-script match.
 * This extension puts a ▶ on torrent sites, so it matches `*://*&#47;*` -- which
 * overlaps everything. Measured: allowing http://192.168.0.251 and then
 * pressing Revoke throws, and the host stays granted forever.
 *
 * So Chrome's permission is the mechanism that lets the fetch happen, and this
 * list is what decides whether it should. Revoking removes it from here, which
 * always works, and the relay stops instantly. An off-switch that depends on an
 * API that can refuse is not an off-switch.
 */
export async function allowedHosts() {
  try {
    const { [HOSTS_KEY]: v } = await chrome.storage.sync.get({ [HOSTS_KEY]: [] });
    return Array.isArray(v) ? v.filter((p) => typeof p === 'string') : [];
  } catch {
    return [];
  }
}

// --------------------------------------------------------- trusted pages ---

export async function extraOrigins() {
  try {
    const { [ORIGINS_KEY]: v } = await chrome.storage.sync.get({ [ORIGINS_KEY]: [] });
    return Array.isArray(v) ? v.map(normaliseOrigin).filter(Boolean) : [];
  } catch {
    return [];
  }
}

async function setExtraOrigins(list) {
  const clean = [...new Set(list.map(normaliseOrigin).filter(Boolean))];
  await chrome.storage.sync.set({ [ORIGINS_KEY]: clean });
  await syncInjection();
  return clean;
}

// ------------------------------------------------------ pending requests ---

/**
 * A page asked for a host it has not been granted. Remember it so the popup can
 * offer a one-click Allow, because the prompt needs a gesture in an extension
 * surface and neither the page nor this worker can produce one.
 *
 * Only the origin is stored -- scheme, host, port. Never the path or query.
 */
async function rememberPending(pattern, target) {
  try {
    const { [PENDING_KEY]: cur } = await chrome.storage.session.get({ [PENDING_KEY]: [] });
    const list = Array.isArray(cur) ? cur.filter((p) => p.pattern !== pattern) : [];
    list.unshift({ pattern, label: describeTarget(target), at: Date.now() });
    await chrome.storage.session.set({ [PENDING_KEY]: list.slice(0, 20) });
    await paintBadge();
  } catch {
    /* session storage is unavailable in some profiles; the error text still
       tells the user what to click, so this is a nicety, not the mechanism. */
  }
}

async function clearPending(pattern) {
  try {
    const { [PENDING_KEY]: cur } = await chrome.storage.session.get({ [PENDING_KEY]: [] });
    const list = (Array.isArray(cur) ? cur : []).filter((p) => p.pattern !== pattern);
    await chrome.storage.session.set({ [PENDING_KEY]: list });
    await paintBadge();
  } catch { /* as above */ }
}

async function paintBadge() {
  try {
    const { [PENDING_KEY]: cur } = await chrome.storage.session.get({ [PENDING_KEY]: [] });
    const n = Array.isArray(cur) ? cur.length : 0;
    await chrome.action.setBadgeText({ text: n ? String(n) : '' });
    await chrome.action.setBadgeBackgroundColor({ color: '#f59e0b' });
  } catch { /* no action surface in some contexts */ }
}

// ------------------------------------------------------------ the relay ----

async function relay(payload, sender) {
  // Gate 1: who is asking. `sender.origin` comes from Chrome, so a page cannot
  // claim to be yarrit.com. Checked here rather than only in the content script
  // because the content script is code the user's browser runs on a page, and
  // the page is the untrusted party in this relationship.
  const origin = sender?.origin || (sender?.url ? normaliseOrigin(sender.url) : '');
  if (!isTrustedOrigin(origin, await extraOrigins())) {
    return { error: 'this site is not allowed to use the Yarr.It relay' };
  }

  // Gate 2: is the request itself sane.
  const v = validateRequest(payload);
  if (!v.ok) return { error: v.error };

  // Gate 3: has the user allowed this host. Both halves are required -- our own
  // list says they still want it, Chrome's permission is what makes the fetch
  // legal. Either one missing and the answer is no.
  let granted = false;
  try {
    granted = (await allowedHosts()).includes(v.pattern)
      && await chrome.permissions.contains({ origins: [v.pattern] });
  } catch {
    granted = false;
  }
  if (!granted) {
    await rememberPending(v.pattern, v.url);
    return { error: permissionNeededMessage(v.url), needsPermission: v.pattern };
  }
  await clearPending(v.pattern);

  try {
    return await limiter.run(() => withTimeout(
      perform(v),
      RELAY_TIMEOUT_MS,
      `${describeTarget(v.url)} did not answer in time`,
    ));
  } catch (e) {
    return { error: e?.message || 'the relay failed' };
  }
}

async function perform(v) {
  const ctl = new AbortController();
  const abort = setTimeout(() => ctl.abort(), RELAY_TIMEOUT_MS);
  try {
    const res = await fetch(v.url, {
      method: v.method,
      headers: v.headers,
      body: v.body,
      // The rule this whole file exists to honour. An extension fetch would
      // otherwise attach whatever cookies the browser holds for that host, and
      // a relay that forwards ambient credentials is a way to launder a session
      // from one origin into another.
      credentials: 'omit',
      cache: 'no-store',
      redirect: 'follow',
      referrer: '',
      referrerPolicy: 'no-referrer',
      signal: ctl.signal,
    });

    // Gate 4: where the answer actually came from. `redirect: 'follow'` is
    // needed because real services redirect (`/` -> `/web/`), but a redirect
    // that walks off the granted host would turn one grant into an open fetch
    // proxy. Chrome's CORS would usually stop it; this makes it certain.
    const landed = hostPattern(res.url || v.url);
    if (landed && landed !== v.pattern) {
      let ok = false;
      try {
        ok = (await allowedHosts()).includes(landed)
          && await chrome.permissions.contains({ origins: [landed] });
      } catch { ok = false; }
      if (!ok) {
        return { error: `that address redirected to ${describeTarget(res.url)}, which you have not allowed` };
      }
    }

    const body = await res.text();
    return {
      status: res.status,
      headers: sanitiseResponseHeaders(res.headers.entries()),
      body,
    };
  } catch (e) {
    if (e?.name === 'AbortError') {
      return { error: `${describeTarget(v.url)} did not answer in time` };
    }
    // A failed fetch here is nearly always "nothing is listening". Deliberately
    // not echoing the URL: it may carry an API key in its query string.
    return { error: `could not reach ${describeTarget(v.url)}` };
  } finally {
    clearTimeout(abort);
  }
}

// ---------------------------------------------------------- registration ---

/**
 * Keep the injected scripts in step with what the user has allowed.
 *
 * yarrit.com is in the manifest. Anything else the user added needs registering
 * at runtime, and needs unregistering the moment they take it away -- a relay
 * that keeps announcing itself on a site you removed is a relay you cannot
 * turn off.
 */
const DYNAMIC_IDS = ['yarrit-relay-extra', 'yarrit-relay-extra-main'];

export async function syncInjection() {
  let existing = [];
  try {
    existing = await chrome.scripting.getRegisteredContentScripts({ ids: DYNAMIC_IDS });
  } catch { existing = []; }
  if (existing.length) {
    try {
      await chrome.scripting.unregisterContentScripts({ ids: existing.map((s) => s.id) });
    } catch { /* already gone */ }
  }

  const matches = [];
  for (const o of await extraOrigins()) {
    const p = hostPattern(o + '/');
    if (!p) continue;
    try {
      if (await chrome.permissions.contains({ origins: [p] })) matches.push(p);
    } catch { /* skip */ }
  }
  if (!matches.length) return;

  try {
    await chrome.scripting.registerContentScripts([
      {
        id: 'yarrit-relay-extra',
        matches,
        js: ['relay.js'],
        runAt: 'document_start',
        world: 'ISOLATED',
        allFrames: false,
        persistAcrossSessions: true,
      },
      {
        id: 'yarrit-relay-extra-main',
        matches,
        js: ['relay-main.js'],
        runAt: 'document_start',
        world: 'MAIN',
        allFrames: false,
        persistAcrossSessions: true,
      },
    ]);
  } catch { /* a match pattern Chrome refuses; the built-in origin still works */ }
}

// ----------------------------------------------------------------- wiring ---

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg?.type === RELAY_INTERNAL) {
    relay(msg.payload, sender).then(sendResponse, (e) => sendResponse({
      error: e?.message || 'the relay failed',
    }));
    return true; // async
  }
  // The options page edits the trusted-origin list directly in storage; this is
  // how it asks for the injected scripts to be brought back into step.
  if (msg?.type === 'yarrit:relay:sync') {
    syncInjection().then(() => sendResponse({ ok: true }), () => sendResponse({ ok: false }));
    return true;
  }
  return false;
});

// A revoked host must stop being reachable, and an added one must start.
chrome.permissions.onAdded.addListener((p) => {
  for (const o of p.origins || []) clearPending(o);
  syncInjection();
});
chrome.permissions.onRemoved.addListener(() => { syncInjection(); });
chrome.runtime.onInstalled.addListener(() => { syncInjection(); paintBadge(); });
chrome.runtime.onStartup.addListener(() => { syncInjection(); paintBadge(); });

export { BUILTIN_ORIGINS, setExtraOrigins, relay as __relayForTests };
