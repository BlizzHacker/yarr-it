/**
 * A boot harness, not a feature.
 *
 * It exists to answer one question that no unit test can: does a real
 * ColecoVision game, with real firmware, actually run past the BIOS screen in a
 * real browser? gearcoleco draws NO BIOS at a healthy frame rate and reports
 * itself started, so "the emulator launched" proves nothing -- the only proof is
 * frames advancing over several seconds with no error screen.
 *
 * It imports the SHIPPED modules and nothing else: the same fetchVerdict, the
 * same toPlayable, the same mountEmulator the app uses. A harness that
 * reimplemented any of that would be testing itself.
 *
 * Drive it from the console:  window.__proof.run('<archive.org identifier>')
 */
import { fetchVerdict, toPlayable, choosePlayer, ROUTE } from './play.js';

const log = [];
function say(msg) {
  log.push(msg);
  const el = document.querySelector('#log');
  if (el) el.textContent = log.join('\n');
}

async function run(id) {
  const verdict = await fetchVerdict(id);
  say(`verdict: route=${verdict.route} core=${verdict.core} rom=${verdict.rom?.name}`);
  say(`bios: ${JSON.stringify(verdict.biosNeeded)}`);
  if (verdict.route !== ROUTE.EMULATORJS) {
    say(`NOT OUR PLAYER: ${verdict.reasons?.[0]?.detail ?? ''}`);
    return { ok: false, verdict };
  }
  const playable = toPlayable(verdict, { route: choosePlayer(verdict) });
  const host = document.querySelector('#stage');
  host.replaceChildren();
  playable.mount(host);
  window.__proof.verdict = verdict;
  window.__proof.playable = playable;
  say('mounted; press the start button');
  return { ok: true, verdict };
}

/** Click the start button mountEmulator hangs the boot off. */
function start() {
  const btn = document.querySelector('#stage button');
  if (!btn) return 'no start button';
  btn.click();
  return 'clicked';
}

/**
 * A frame sample. EmulatorJS exposes the running core as EJS_emulator; its
 * `gameManager.getFrameNum()` is the emulator's own frame counter, which is the
 * only number that distinguishes a running game from an error screen being
 * rendered at 60fps -- both of which look identical from outside.
 */
function sample() {
  const e = window.EJS_emulator;
  let frame = null;
  try { frame = e?.gameManager?.getFrameNum?.() ?? null; } catch { frame = null; }
  const canvas = document.querySelector('#stage canvas');
  return {
    frame,
    started: Boolean(e?.started),
    paused: (() => { try { return e?.paused ?? null; } catch { return null; } })(),
    canvas: canvas ? { w: canvas.width, h: canvas.height } : null,
    biosUrl: window.EJS_biosUrl || '',
    core: window.EJS_core || '',
    options: window.EJS_defaultOptions ?? null,
  };
}

window.__proof = { run, start, sample, log };
say('ready');
