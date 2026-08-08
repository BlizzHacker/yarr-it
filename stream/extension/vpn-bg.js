/**
 * Browser-level protection: the chrome.* half of it.
 *
 * vpn-core.js holds the rules and knows nothing about Chrome. This file is the
 * only place that touches `chrome.proxy`, `chrome.privacy` and the credentials,
 * and it is written the same way relay-bg.js is: assume the thing being
 * protected is worth protecting, and never claim more than was measured.
 *
 * THREE THINGS HAPPEN TOGETHER OR NOTHING WORTH HAVING HAPPENS
 *
 *   1. the proxy.   A PAC script with no DIRECT fallback, so a dead proxy fails
 *                   the request instead of quietly using the real address.
 *   2. the UDP lock. `webRTCIPHandlingPolicy = disable_non_proxied_udp`. WebTorrent
 *                   peers connect over WebRTC and WebRTC does not use an HTTP
 *                   proxy. Set the proxy alone and page loads are covered while
 *                   every peer still learns the real address -- that is, exactly
 *                   the traffic this was turned on for is the traffic still
 *                   leaking. That is worse than nothing, because it feels safe.
 *   3. the truth.   The options page is fed from what the browser reports, not
 *                   from what this file believes it set. See measure().
 *
 * ORDER IS NOT ARBITRARY
 * Turning on: the UDP lock first, then the proxy. The gap between them then has
 * WebRTC blocked and no proxy, which fails closed. The other order leaves a gap
 * with a working proxy and a wide-open WebRTC, which is the leak.
 * Turning off: the proxy first, then the UDP lock, for the same reason read
 * backwards -- and both are attempted even if the first one throws, because a
 * half-off state that still blocks WebRTC breaks video calls on every other site
 * and nobody would ever connect that to this extension.
 */

import {
  SAFE_WEBRTC_POLICY, buildPac, parseProxy, protectionSummary,
} from './vpn-core.js';

/**
 * All of this is `storage.local`, including the parts that are not secret.
 *
 * `sync` would carry this profile's settings to a browser where the proxy was
 * never applied, and that browser's options page would then read "Protected"
 * while its traffic goes out the front door. A setting that describes one
 * browser's network state must not travel to a browser it is not true of.
 * The credentials would be sync's own problem anyway: they would ride Google's
 * account storage to every machine signed in, which is not where a VPN password
 * belongs.
 */
const CONFIG_KEY = 'vpnConfig';
const CREDS_KEY = 'vpnCreds';

/**
 * `proxy` is a required permission and `privacy` is not. That split is not a
 * preference, it is what Chrome allows, and it was measured rather than assumed.
 *
 * With "proxy" sitting in optional_permissions exactly as intended:
 *
 *   chrome.permissions.request({ permissions: ['proxy'] })
 *     -> throws "Only permissions specified in the manifest may be requested."
 *
 * Chrome drops `proxy` from the parsed optional set, so there is no runtime path
 * to it at all -- declare it in `permissions` or `chrome.proxy` is undefined
 * forever. `privacy`, `webRequest` and `webRequestAuthProvider` were requested
 * the same way in the same breath and all three returned true.
 *
 * What declaring it up front costs the user was measured too, with
 * `management.getPermissionWarningsByManifest`: adding `proxy` to this
 * extension's manifest adds no install warning whatsoever. Adding `privacy`
 * adds "Change your privacy-related settings". So the permission that carries a
 * visible warning is the one still bought with a click, which is where the rule
 * about not asking up front actually buys something -- and the one that cannot
 * be asked for is also the one nobody is shown.
 *
 * Holding the permission is not using it. Nothing is routed anywhere until
 * someone presses the button.
 */
export const RUNTIME_PERMISSIONS = ['privacy'];

/**
 * The extra permissions the credential path needs, and why they are separate.
 *
 * Answering a proxy's 407 needs `webRequest` + `webRequestAuthProvider` and a
 * host permission broad enough to see the request -- which reads to the user as
 * "read and change all your data on all websites". Most proxies worth using here
 * need no password at all, so asking for that up front would be buying a scary
 * prompt for a feature most people never use. It is requested only when someone
 * actually types a username.
 */
export const AUTH_PERMISSIONS = ['webRequest', 'webRequestAuthProvider'];
export const AUTH_ORIGINS = ['*://*/*'];

/**
 * Services asked "what address did that come from".
 *
 * Asking at all means telling a third party your address, so it happens on a
 * button and never on a timer. The list exists because one of them being down
 * should not look like the proxy being down -- those two have opposite fixes.
 */
const IP_SERVICES = [
  { url: 'https://api.ipify.org/?format=json', pick: (t) => JSON.parse(t).ip },
  { url: 'https://icanhazip.com/', pick: (t) => t.trim() },
];
const IP_TIMEOUT_MS = 12000;

const DEFAULTS = {
  enabled: false,
  scheme: '',
  host: '',
  port: 0,
  killSwitch: true,
  exemptLan: true,
};

// ------------------------------------------------------------- settings ----

export async function readConfig() {
  try {
    const { [CONFIG_KEY]: v } = await chrome.storage.local.get({ [CONFIG_KEY]: null });
    return v && typeof v === 'object' ? { ...DEFAULTS, ...v } : { ...DEFAULTS };
  } catch {
    return { ...DEFAULTS };
  }
}

async function writeConfig(cfg) {
  await chrome.storage.local.set({ [CONFIG_KEY]: cfg });
  return cfg;
}

async function readCreds() {
  try {
    const { [CREDS_KEY]: v } = await chrome.storage.local.get({ [CREDS_KEY]: null });
    return v && typeof v === 'object' ? v : { username: '', password: '' };
  } catch {
    return { username: '', password: '' };
  }
}

// ---------------------------------------------------------- permissions ----

async function has(permissions, origins) {
  try {
    return await chrome.permissions.contains({ permissions, ...(origins ? { origins } : {}) });
  } catch {
    return false;
  }
}

export async function permissionState() {
  return {
    // Both halves, because either one missing means protection that leaks. The
    // proxy API being present is checked directly rather than through
    // permissions.contains, since it is granted at install and cannot be lost.
    core: Boolean(chrome.proxy?.settings) && await has(RUNTIME_PERMISSIONS),
    auth: await has(AUTH_PERMISSIONS, AUTH_ORIGINS),
  };
}

// --------------------------------------------------------- what is real ----

/**
 * What the browser says is in force, which is not the same as what we asked for.
 *
 * `levelOfControl` is the part that matters and the part a stored boolean cannot
 * tell you: another extension can take the proxy setting away from this one, and
 * enterprise policy can pin it. In both cases our own record still says
 * "enabled". Reading it back means the options page says "not protected" when
 * that is the truth, instead of repeating our intentions to us.
 */
export async function measure() {
  const out = {
    proxyMode: null,
    proxyControl: null,
    webrtcPolicy: null,
    webrtcControl: null,
    apiMissing: false,
  };
  if (!chrome.proxy?.settings || !chrome.privacy?.network?.webRTCIPHandlingPolicy) {
    out.apiMissing = true;
    return out;
  }
  try {
    const p = await chrome.proxy.settings.get({});
    out.proxyMode = p?.value?.mode ?? null;
    out.proxyControl = p?.levelOfControl ?? null;
  } catch { /* left null, and null is reported as not protected */ }
  try {
    const w = await chrome.privacy.network.webRTCIPHandlingPolicy.get({});
    out.webrtcPolicy = w?.value ?? null;
    out.webrtcControl = w?.levelOfControl ?? null;
  } catch { /* as above */ }
  return out;
}

/** Everything the options page draws, assembled in one place. */
export async function state() {
  const cfg = await readConfig();
  const perms = await permissionState();
  const creds = await readCreds();
  const m = perms.core ? await measure() : { apiMissing: true };

  // Live means this extension is the one holding the proxy setting *and* it is
  // holding a PAC. Anything else -- cleared, taken over by another extension,
  // pinned by policy -- is not protection, whatever we wrote down earlier.
  const live = m.proxyMode === 'pac_script'
    && m.proxyControl === 'controlled_by_this_extension';

  return {
    config: { ...cfg, enabled: live },
    stored: cfg.enabled,
    // Said out loud rather than silently corrected: if these disagree, someone
    // or something else moved the setting, and that is worth seeing.
    drift: cfg.enabled !== live,
    permissions: perms,
    measured: m,
    hasCredentials: Boolean(creds.username),
    authWired: authWired(),
    summary: protectionSummary({
      enabled: live,
      killSwitch: cfg.killSwitch,
      exemptLan: cfg.exemptLan,
      webrtcPolicy: m.webrtcPolicy,
    }),
  };
}

// ------------------------------------------------------------- applying ----

async function lockWebRTC() {
  await chrome.privacy.network.webRTCIPHandlingPolicy.set({
    value: SAFE_WEBRTC_POLICY,
    scope: 'regular',
  });
}

async function applyProxy(cfg) {
  await chrome.proxy.settings.set({
    scope: 'regular',
    value: {
      mode: 'pac_script',
      pacScript: {
        data: buildPac(cfg),
        // The other half of the kill switch, and the half that is easy to miss.
        // The PAC handles "the proxy is down"; `mandatory` handles "the PAC
        // itself failed", which otherwise drops the network stack back to
        // direct connections -- the same silent leak by a different route.
        mandatory: Boolean(cfg.killSwitch),
      },
    },
  });
}

/**
 * Turn it on. Fails loudly and leaves nothing half-applied.
 *
 * The permission is only checked here, never requested: `permissions.request`
 * needs a user gesture and a service worker has no window that could have been
 * clicked in. options.js does the asking.
 */
export async function enable(input) {
  const parsed = parseProxy(input?.address);
  if (!parsed.ok) return { ok: false, error: parsed.error };

  if (!(await has(RUNTIME_PERMISSIONS))) {
    return {
      ok: false,
      error: 'Yarr.It cannot close the WebRTC leak without the privacy permission, '
        + 'and a proxy without that is not worth turning on.',
    };
  }
  if (!chrome.proxy?.settings || !chrome.privacy?.network?.webRTCIPHandlingPolicy) {
    return { ok: false, error: 'Chrome granted the permission but has not handed over the API yet. Try once more.' };
  }

  const cfg = {
    enabled: true,
    scheme: parsed.scheme,
    host: parsed.host,
    port: parsed.port,
    killSwitch: input?.killSwitch !== false,
    exemptLan: input?.exemptLan !== false,
  };

  try {
    await lockWebRTC();   // first, so the gap fails closed
    await applyProxy(cfg);
  } catch (e) {
    // Do not leave WebRTC blocked with no proxy to show for it. That state
    // breaks every video call on the machine and gives back nothing.
    await disable().catch(() => {});
    return { ok: false, error: e?.message || 'Chrome refused the proxy settings.' };
  }

  await writeConfig(cfg);
  wireAuth();
  return { ok: true, state: await state() };
}

/**
 * Turn it off, all the way.
 *
 * Both halves are attempted even if the first throws. A half-off state that
 * still blocks non-proxied UDP is the worst outcome available here: video calls
 * break everywhere, this extension looks off, and nobody connects the two.
 * `clear()` rather than setting a default value, because clear hands the setting
 * back to whatever else would have owned it; writing a value keeps this
 * extension listed as the controller of a setting it no longer cares about.
 */
export async function disable() {
  const errors = [];
  try {
    if (chrome.proxy?.settings) await chrome.proxy.settings.clear({ scope: 'regular' });
  } catch (e) {
    errors.push(`proxy: ${e?.message || 'refused'}`);
  }
  try {
    if (chrome.privacy?.network?.webRTCIPHandlingPolicy) {
      await chrome.privacy.network.webRTCIPHandlingPolicy.clear({ scope: 'regular' });
    }
  } catch (e) {
    errors.push(`WebRTC: ${e?.message || 'refused'}`);
  }

  const cfg = await readConfig();
  await writeConfig({ ...cfg, enabled: false });

  if (errors.length) {
    return {
      ok: false,
      error: `Could not fully restore ${errors.join(' and ')}. `
        + 'Check chrome://settings and disable other proxy extensions.',
      state: await state(),
    };
  }
  return { ok: true, state: await state() };
}

// ---------------------------------------------------------- credentials ----

/**
 * Answer a proxy's 407, and nothing else's.
 *
 * Four refusals before any credential leaves this file, because the alternative
 * is handing a VPN password to whoever asks:
 *
 *   not a proxy challenge  -> a site's own 401 is not ours to answer.
 *   not enabled            -> nothing should be asking.
 *   not our proxy          -> `details.challenger` must be the exact host and
 *                             port that was configured. Another proxy in the
 *                             chain, or a different one entirely, gets nothing.
 *   already tried once     -> a wrong password otherwise loops forever, silently,
 *                             and looks like the proxy being down.
 *
 * Chrome cannot take credentials from the PAC URL, which is why this exists at
 * all; parseProxy() refuses `user:pass@host` for the same reason.
 */
const attempts = new Map();

function remember(requestId) {
  if (attempts.size > 200) attempts.clear(); // no onCompleted listener, so cap it
  const n = (attempts.get(requestId) || 0) + 1;
  attempts.set(requestId, n);
  return n;
}

export async function decideAuth(details) {
  if (!details?.isProxy) return {};
  const cfg = await readConfig();
  if (!cfg.enabled || !cfg.host) return {};

  const ch = details.challenger || {};
  const sameHost = String(ch.host || '').toLowerCase() === String(cfg.host).toLowerCase();
  const samePort = Number(ch.port) === Number(cfg.port);
  if (!sameHost || !samePort) return {};

  const creds = await readCreds();
  if (!creds.username) return {};
  if (remember(details.requestId) > 1) return {};

  return { authCredentials: { username: creds.username, password: creds.password } };
}

function authListener(details, callback) {
  decideAuth(details).then(callback, () => callback({}));
}

/**
 * Whether Chrome is actually letting us answer proxy password prompts.
 *
 * Reported rather than assumed, because "credentials saved" and "credentials
 * can be delivered" are different facts and the gap between them is invisible:
 * the request just fails with ERR_INVALID_AUTH_CREDENTIALS and nothing points
 * at this extension.
 */
export function authWired() {
  return Boolean(chrome.webRequest?.onAuthRequired?.hasListener?.(authListener));
}

/**
 * Registered synchronously when the API exists, which is the only version of
 * this that survives the service worker going to sleep: Chrome will not wake a
 * worker for an event whose listener is added inside a promise.
 *
 * `chrome.webRequest` is simply undefined until the optional permission is
 * granted, so the presence check is also the permission check.
 */
export function wireAuth() {
  if (!chrome.webRequest?.onAuthRequired) return false;
  if (chrome.webRequest.onAuthRequired.hasListener(authListener)) return true;
  try {
    chrome.webRequest.onAuthRequired.addListener(
      authListener,
      { urls: ['<all_urls>'] },
      ['asyncBlocking'],
    );
    return true;
  } catch {
    // MV3 refuses the blocking form without `webRequestAuthProvider`. Not fatal:
    // a proxy that needs no password still works, and the options page says so.
    return false;
  }
}

wireAuth();

// ----------------------------------------------------------- apparent IP ---

/** What the internet currently thinks this browser's address is. */
export async function apparentIp() {
  const tried = [];
  for (const svc of IP_SERVICES) {
    const ctl = new AbortController();
    const t = setTimeout(() => ctl.abort(), IP_TIMEOUT_MS);
    try {
      const res = await fetch(svc.url, {
        credentials: 'omit',
        cache: 'no-store',
        referrerPolicy: 'no-referrer',
        signal: ctl.signal,
      });
      if (!res.ok) throw new Error(`answered ${res.status}`);
      const ip = svc.pick(await res.text());
      if (!ip) throw new Error('answered with nothing');
      return { ok: true, ip, via: new URL(svc.url).host };
    } catch (e) {
      tried.push(`${new URL(svc.url).host}: ${e?.name === 'AbortError' ? 'timed out' : (e?.message || 'failed')}`);
    } finally {
      clearTimeout(t);
    }
  }
  // With the kill switch on, this is the correct outcome when the proxy is down.
  // Say that, because "no answer" and "your real address" are opposite results
  // and only one of them is a problem.
  return { ok: false, error: `No answer from ${tried.join('; ')}`, tried };
}

// ---------------------------------------------------------------- wiring ---

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  // Only this extension's own pages may drive the proxy. `sender.id` alone is
  // not enough: the ▶ content script runs on every site and carries this
  // extension's id, so a page that got a message past it could reroute the whole
  // browser. The sender's URL is the discriminator -- Chrome sets it, and for a
  // content script it is the website's URL, not ours.
  //
  // Measured the hard way: `!sender.tab` looks like the same check and is not.
  // options_ui sets open_in_tab, so the options page has a tab and was refused
  // by its own worker.
  const ours = chrome.runtime.getURL('');
  const fromOurPages = sender?.id === chrome.runtime.id
    && typeof sender?.url === 'string'
    && sender.url.startsWith(ours);
  if (typeof msg?.type !== 'string' || !msg.type.startsWith('yarrit:vpn:')) return false;
  if (!fromOurPages) {
    sendResponse({ ok: false, error: 'only the Yarr.It options page can change this' });
    return true;
  }

  const done = (p) => {
    p.then(sendResponse, (e) => sendResponse({ ok: false, error: e?.message || 'failed' }));
    return true;
  };

  switch (msg.type) {
    case 'yarrit:vpn:state':
      return done(state().then((s) => ({ ok: true, state: s })));
    case 'yarrit:vpn:enable':
      return done(enable(msg.payload));
    case 'yarrit:vpn:disable':
      return done(disable());
    case 'yarrit:vpn:credentials':
      return done((async () => {
        const username = String(msg.payload?.username || '');
        const password = String(msg.payload?.password || '');
        if (!username) await chrome.storage.local.remove(CREDS_KEY);
        else await chrome.storage.local.set({ [CREDS_KEY]: { username, password } });
        wireAuth();
        return { ok: true, state: await state() };
      })());
    case 'yarrit:vpn:ip':
      return done(apparentIp());
    default:
      return false;
  }
});

// A permission granted on the options page has to reach the worker, which may
// have been asleep when the prompt was answered.
chrome.permissions.onAdded.addListener(() => { wireAuth(); });
