/**
 * What `/play/<source>/<id>` means, and where that page is allowed to fetch
 * bytes from.
 *
 * WHY THERE IS A SECOND PAGE AT ALL.
 *
 * Two EmulatorJS cores are published only as threaded builds -- `ppsspp` (PSP)
 * and `dosbox_pure` (MS-DOS); the plain `-wasm.data` names 404 on the CDN.
 * Threads need SharedArrayBuffer, which the browser only creates on a
 * cross-origin-isolated document, which needs COOP *and* COEP.
 *
 * yarrit.com cannot send COEP. Its whole reason for existing is embedding other
 * people's things -- the Internet Archive's own Emularity player in an iframe
 * above all -- and a COEP document may only embed a cross-origin iframe that
 * sends COEP back. Isolating the origin would trade every Internet Archive game
 * for the PSP ones. That trade is why vimm.go marks PSP and DOS entries
 * `vaultElsewhere` and sends people to the vault, which IS isolated.
 *
 * But isolation is a property of a DOCUMENT, not an origin. One route, serving
 * one page that embeds nothing, can be isolated while the rest of the site is
 * not. That is all this is: the smallest document that can hold an emulator,
 * on a path Caddy gives the two headers to.
 *
 * WHY THIS FILE IS THE PARANOID ONE.
 *
 * Every other page decides what to fetch from an answer the server gave it.
 * This one decides from its own URL, because that is what a route is -- and a
 * URL is written by whoever sends the link. So it takes exactly two things on
 * trust, an opaque identifier and a core NAME, and the one field that becomes a
 * network destination is checked against an allowlist rather than copied. That
 * is the rule normaliseBios already applies to a firmware URL; a ROM is the
 * same kind of field.
 */

/** Where a game's bytes come from. The first path segment after /play/. */
export const SOURCE = {
  /** An archive.org item. The verdict service answers for it in full. */
  ARCHIVE: 'archive',
  /** A Vimm's Lair vault entry, served by our own vault instance. */
  VIMM: 'vimm',
};

export const PLAY_PREFIX = '/play/';

/**
 * An archive.org identifier, or a vault id. Both are opaque to us and both are
 * addressed by name, so the only question worth asking is whether this is one
 * token or a path pretending to be one.
 */
const ID_SHAPE = /^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$/;

/**
 * A core name shaped like the ones EmulatorJS publishes -- `psp`, `dos`,
 * `segaSaturn`, `sega32x`, `vice_x64sc`.
 *
 * This is a SHAPE check, not a vocabulary check, and the difference matters.
 * EmulatorJS interpolates the name into the URL it fetches its core from, so a
 * name with a separator in it is a traversal inside whichever origin serves the
 * cores. That is what this stops. Whether the name is a real machine is the
 * server's published list to answer -- see `/api/play/systems` -- because a
 * second copy of that vocabulary living here is precisely the drift that has
 * already cost this project a thousand games that browsed fine and died on the
 * button press.
 */
const CORE_SHAPE = /^[A-Za-z0-9_]{1,24}$/;

/**
 * Read a target out of a location.
 *
 * Returns `{source, id, core, name}` with empty strings for anything absent or
 * refused. Never throws: this runs on a URL a stranger may have written, and a
 * page that throws on a malformed link shows a blank screen instead of a
 * sentence.
 */
export function parseTarget(pathname = '', hash = '') {
  const out = { source: '', id: '', core: '', name: '' };

  const path = String(pathname ?? '');
  if (!path.startsWith(PLAY_PREFIX)) return out;

  const parts = path.slice(PLAY_PREFIX.length).split('/').filter(Boolean);
  if (parts.length < 2) return out;

  const [source, rawId] = parts;
  if (source !== SOURCE.ARCHIVE && source !== SOURCE.VIMM) return out;
  out.source = source;

  // Decoded before it is checked, so a percent-encoded separator is judged as
  // the separator it is. `..%2F..%2Fadmin` is a path; a path is not an id.
  let id = '';
  try {
    id = decodeURIComponent(rawId);
  } catch {
    return out; // a malformed escape is not an identifier either
  }
  if (!ID_SHAPE.test(id)) return out;
  out.id = id;

  const frag = new URLSearchParams(String(hash ?? '').replace(/^#/, ''));
  const core = frag.get('ejs') ?? '';
  if (CORE_SHAPE.test(core)) out.core = core;
  out.name = (frag.get('name') ?? '').slice(0, 200);

  return out;
}

/**
 * The address of a target, for the side of the app that LINKS to this page.
 *
 * Built here rather than by string concatenation at each call site so that the
 * escaping is done once. An archive.org identifier is usually boring and
 * occasionally is not; a game title is frequently not.
 */
export function targetPath({ source, id, core = '', name = '' } = {}) {
  if (!source || !id) return '';
  let path = `${PLAY_PREFIX}${source}/${encodeURIComponent(id)}`;
  const frag = new URLSearchParams();
  if (core) frag.set('ejs', core);
  if (name) frag.set('name', name);
  const tail = frag.toString();
  return tail ? `${path}#${tail}` : path;
}

// ------------------------------------------------------------ the allowlist --

/**
 * Hosts that may serve a ROM to this page.
 *
 * `vault` is whatever this server published as its Vimm vault origin, or empty
 * when it has none -- a self-hosted instance with no vault must not have one
 * hardcoded here, and the hosted one must not be reachable from somebody else's
 * deployment just because they run our code.
 */
export function romOrigins(pageOrigin = '', vault = '') {
  const hosts = new Set(['archive.org']);
  const suffixes = ['.archive.org']; // ia801603.us.archive.org and its siblings
  const origins = new Set();
  if (pageOrigin) origins.add(pageOrigin);
  if (vault) {
    try {
      origins.add(new URL(vault).origin);
    } catch {
      /* an unparseable vault origin allows nothing, which is the safe way to
         be wrong about it */
    }
  }
  return { hosts, suffixes, origins };
}

/**
 * May this page fetch a ROM from here?
 *
 * A relative URL is resolved against the page, so `/bridge/iptv?u=…` is our own
 * relay and is allowed on any deployment, including a self-hosted one whose
 * origin is not yarrit.com.
 *
 * The host test is on the whole label, never `endsWith` on the bare name:
 * `archive.org.evil.test` ends with nothing that matters and `notarchive.org`
 * merely contains it, and the obvious version of this check accepts both.
 */
export function checkRomUrl(raw, allow, pageOrigin = '') {
  if (!raw || !allow) return false;
  let url;
  try {
    url = new URL(String(raw), pageOrigin || undefined);
  } catch {
    return false;
  }
  // Only https. The page is isolated and served over TLS, so a plain-http
  // subresource is blocked as mixed content regardless -- refusing it here
  // means somebody gets a sentence instead of a fetch that fails with nothing
  // in it. Every other scheme, javascript: and data: included, falls out too.
  if (url.protocol !== 'https:') return false;

  if (allow.origins.has(url.origin)) return true;
  if (allow.hosts.has(url.hostname)) return true;
  return allow.suffixes.some((suffix) => url.hostname.endsWith(suffix));
}
