/**
 * The isolated player.
 *
 * This is the whole of `/play/*`: one document, served with COOP and COEP, that
 * boots one game and embeds nothing. See play-target.js for why it exists as a
 * separate page rather than as a mode of the app -- briefly, isolation is a
 * property of a document, the app cannot have it without giving up the Internet
 * Archive's iframe player, and this page can because it has no iframe to give
 * up.
 *
 * It imports the SHIPPED modules -- the same fetchVerdict, the same
 * mountEmulator the app uses -- for the reason bootproof.js states: a page that
 * reimplemented any of that would prove only that the reimplementation works.
 *
 * WHAT IT REFUSES TO DO QUIETLY. Every failure here has a sentence, because the
 * failure mode of this whole area is silence: EmulatorJS handed a threaded core
 * on a page that is not isolated loads the core, throws inside it, reports
 * `started`, and paints nothing. If the headers do not arrive, this says the
 * headers did not arrive.
 */
import { parseTarget, romOrigins, checkRomUrl, SOURCE } from './play-target.js';
import { fetchVerdict, toPlayable, ROUTE } from './play.js';
import { mountEmulator } from './resolvers/game.js';
import { serverBase } from './server.js';

const el = (id) => document.getElementById(id);

/** Put a sentence on screen. `fatal` also clears the stage. */
function say(text, fatal = false) {
  const note = el('note');
  if (note) note.textContent = text;
  if (fatal) el('stage')?.replaceChildren();
}

function setTitle(text) {
  const h = el('title');
  if (h) h.textContent = text;
  if (text) document.title = `${text} — Yarr.It`;
}

/**
 * Ask the server what it knows about itself.
 *
 * Two things are needed and both must come from the server rather than from
 * here: the vault origin a ROM may be fetched from, and the list of systems it
 * believes EmulatorJS can run. Neither is hardcoded, for the same reason -- a
 * second copy drifts, and the drift shows up as a game that browses fine and
 * dies on the button press.
 *
 * Never throws. A server that cannot be reached costs the vault path and the
 * vocabulary check; it does not cost the archive.org path, which carries its
 * own answer.
 */
async function vaultOrigin(base) {
  try {
    const res = await fetch(`${base}/api/health`, { cache: 'no-store' });
    if (!res.ok) return '';
    const body = await res.json();
    const vault = body?.vimm?.vault;
    return typeof vault === 'string' ? vault : '';
  } catch {
    // The vault path will then refuse, which is the safe way to be wrong about
    // where bytes may come from.
    return '';
  }
}

/**
 * An archive.org item. The verdict service answers for it completely -- core,
 * name, byte sources, and whether it plays at all -- so nothing here has to
 * decide anything.
 *
 * `isolated` is left to default: verdictQuery reads `crossOriginIsolated` from
 * the platform, and on this page that is the true answer. Passing `true` by
 * hand would be a claim rather than a reading, and a claim that outlived a
 * broken header deploy is exactly the dead core described at the top.
 */
async function playArchive(id, base) {
  say('Checking this item…');
  const verdict = await fetchVerdict(id, { base });
  setTitle(verdict.title || id);

  if (verdict.route !== ROUTE.EMULATORJS) {
    const why = verdict.reasons?.[0]?.detail ?? 'this item does not play in our own player.';
    say(`This game cannot be played here — ${why}`, true);
    return;
  }

  // The destinations are checked before toPlayable is allowed to use them.
  // Everything else in a verdict costs a button if it is wrong; these two
  // fields are the ones that turn into a fetch, and this page is reached from a
  // URL rather than from a card, so they get the treatment normaliseBios gives
  // a firmware URL.
  const allow = romOrigins(location.origin, '');
  for (const src of [verdict.rom?.fetch, verdict.rom?.url]) {
    if (src && !checkRomUrl(src, allow, location.origin)) {
      say('This game cannot be played here — the play service named a ROM host this page may not fetch from.', true);
      return;
    }
  }

  // toPlayable, not a hand-rolled mount: it already knows the source ORDER
  // (archive.org's own CORS endpoint first, our relay as the fallback), the
  // name the core must see, and the firmware and core options for the machines
  // that need them. Rebuilding any of that here would be a second place for it
  // to be wrong.
  say('');
  try {
    toPlayable(verdict, { route: ROUTE.EMULATORJS }).mount(el('stage'));
  } catch (error) {
    say(`This game cannot be played here — ${error?.message ?? error}`, true);
  }
}

/**
 * A vault entry. The vault serves the bytes and the fragment says which core --
 * the same `#ejs=<core>&name=<title>` convention `declared()` reads in the app.
 *
 * The URL is BUILT from the published vault origin and the id, never taken from
 * the link. A link may say which item; it may not say which host.
 */
async function playVimm(id, core, name, vault) {
  setTitle(name || `Vault ${id}`);

  if (!vault) {
    say('This game cannot be played here — this server has no Vimm vault configured.', true);
    return;
  }
  if (!core) {
    say('This game cannot be played here — the link did not say which system it is for.', true);
    return;
  }

  // The core name is NOT checked against /api/play/systems, and that is
  // deliberate. That endpoint is the archive.org vocabulary -- it lists the
  // machines archive.org's `emulator` field can name -- and archive.org has no
  // PSP items at all, so checking a vault core against it would refuse exactly
  // the machine this page was built for. The vault validated its own cores
  // against the getCores() table in the emulator.min.js it serves, which is the
  // only list that has the release's real answer.
  const url = `${vault.replace(/\/+$/, '')}/api/rom/${encodeURIComponent(id)}`;
  const allow = romOrigins(location.origin, vault);
  if (!checkRomUrl(url, allow, location.origin)) {
    say('This game cannot be played here — the vault this server published is not one this page may fetch from.', true);
    return;
  }

  say('');
  mountEmulator(el('stage'), url, {
    core,
    // The vault sends the ROM as application/octet-stream with no filename, and
    // several cores read the extension to decide WHICH MACHINE they are. The
    // name is therefore load-bearing, not decoration -- see fetchRom in game.js.
    name: name || `vault-${id}`,
    label: name || `Vault ${id}`,
    sources: [url],
  });
}

export async function start() {
  const target = parseTarget(location.pathname, location.hash);
  if (!target.source || !target.id) {
    say('Nothing to play — this address does not name a game.', true);
    return;
  }

  // The one check that must happen before anything is fetched. Without
  // isolation the threaded cores load and throw inside themselves, and the page
  // shows a black rectangle that reports itself as running.
  if (globalThis.crossOriginIsolated !== true) {
    say(
      'This page is not cross-origin isolated, so the threaded emulator cores '
      + '(PSP and MS-DOS) cannot start. That means the COOP and COEP headers '
      + 'for /play/ are not arriving — a server configuration problem, not '
      + 'something wrong with this game.',
      true,
    );
    return;
  }

  const base = serverBase();
  if (target.source === SOURCE.ARCHIVE) await playArchive(target.id, base);
  else await playVimm(target.id, target.core, target.name, await vaultOrigin(base));
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start, { once: true });
  } else {
    start();
  }
}
