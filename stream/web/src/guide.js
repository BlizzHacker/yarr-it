/**
 * What the game is, and which key is fire.
 *
 * A booted emulator with no instructions is a picture you cannot touch. Every
 * key does nothing until the right one does something, and the right one is
 * different on every machine: SELECT is `v`, fire on a 2600 is `x`, and an
 * arcade cabinet does literally nothing at all until a coin goes in. On a
 * television there is not even a keyboard to experiment with.
 *
 * So this renders the guide the server assembled -- see search/play_guide.go for
 * where each piece comes from and how authoritative it is.
 *
 * THE ONE RULE THIS FILE MUST NOT BREAK: the Archive's `emulator_instructions`
 * describes THEIR player's keys. "The 1 key pushes the SELECT switch" is true on
 * archive.org and false here, where 1 is quick-save. The server labels that text
 * with whose player it is about (`notesFor`), and this file renders the label
 * rather than dropping it. Confidently wrong instructions are worse than none:
 * somebody who presses the key they were given and gets nothing concludes the
 * emulator is broken.
 *
 * Everything is built with createElement and textContent. Nothing here ever
 * assigns innerHTML, because the text is somebody else's -- it comes from an
 * archive.org item description that anybody can upload. The server already
 * flattens it to plain text; this is the second of the two locks.
 */

import { ROUTE } from './play.js';

const el = (doc, tag, cls, text) => {
  const node = doc.createElement(tag);
  if (cls) node.className = cls;
  if (text != null) node.textContent = text;
  return node;
};

/**
 * Whose key map is on screen, said out loud.
 *
 * On the archive route our key map is still worth showing -- it is what
 * switching players would give you -- but it must not be presented as what the
 * frame on screen responds to.
 */
export function controlsTitle(guide, route) {
  const machine = guide?.controlsFor;
  const where = route === ROUTE.ARCHIVE ? ' in the Yarr.It player' : '';
  return machine ? `${machine} controls${where}` : `Controls${where}`;
}

/**
 * The sentence under the key map on the archive route.
 *
 * Returns '' on our own route, because there is nothing to disclaim: the keys
 * shown are the keys that work.
 */
export function controlsCaveat(route) {
  if (route !== ROUTE.ARCHIVE) return '';
  return 'These are the keys in the Yarr.It player. The Internet Archive\'s '
    + 'player has its own key map — switch players above to use these.';
}

/** Whether there is anything at all to draw. */
export function hasGuide(guide) {
  return Boolean(
    guide && (guide.description || guide.notes || guide.controls?.length || guide.manuals?.length),
  );
}

/**
 * Turn the server's paragraph-separated text into paragraphs.
 *
 * The server emits a blank line between paragraphs and a single newline for a
 * break the source asked for, so this splits on the blank line and lets the
 * single newlines stay inside a paragraph as ordinary wrapping.
 */
export function paragraphs(text) {
  return String(text ?? '')
    .split(/\n{2,}/)
    .map((p) => p.replace(/\n/g, ' ').trim())
    .filter(Boolean);
}

/** A size a person reads, for a manual link. */
function fileSize(bytes) {
  if (!bytes) return '';
  const units = ['B', 'KB', 'MB', 'GB'];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

/**
 * Draw the whole panel.
 *
 * Returns null when there is nothing to say -- an empty panel is worse than no
 * panel, and returning null is how the caller is told which of the two to draw.
 *
 * `doc` is injected so this can be tested without a browser; every element is
 * created through it.
 */
export function renderGuide(guide, { route = ROUTE.EMULATORJS, doc = document } = {}) {
  if (!hasGuide(guide)) return null;

  const panel = el(doc, 'div', 'guide');

  if (guide.description) {
    const section = el(doc, 'section', 'guide-sec');
    section.append(el(doc, 'h4', null, 'About this game'));
    for (const p of paragraphs(guide.description)) {
      section.append(el(doc, 'p', null, p));
    }
    panel.append(section);
  }

  if (guide.controls?.length) {
    const section = el(doc, 'section', 'guide-sec');
    section.append(el(doc, 'h4', null, controlsTitle(guide, route)));

    // Said BEFORE the list, because on these machines the list is the footnote
    // and the keyboard is the answer. Printing 25 rows of RetroPad bindings for
    // a DOS game and never mentioning the keyboard is accurate about the
    // emulator and useless about the game.
    if (guide.keyboard) {
      section.append(el(doc, 'p', null,
        'This machine is driven by the keyboard — type as you would on the real '
        + 'thing. The mapping below is the extra gamepad EmulatorJS adds on top.'));
    }

    const list = el(doc, 'dl', 'keymap');
    for (const control of guide.controls) {
      if (!control?.button || !control?.key) continue;
      list.append(el(doc, 'dt', null, control.button));
      list.append(el(doc, 'dd', null, control.key));
    }
    section.append(list);

    // On a phone there is no keyboard and the key map is not the answer, so the
    // thing that IS the answer gets said rather than left to be discovered.
    if (route === ROUTE.EMULATORJS) {
      section.append(el(doc, 'p', 'guide-note',
        'On a touchscreen, use the on-screen pad — it appears over the game.'));
    }

    const caveat = controlsCaveat(route);
    if (caveat) section.append(el(doc, 'p', 'guide-note', caveat));

    if (guide.controller) {
      section.append(el(doc, 'p', 'guide-note',
        `The Internet Archive lists this game's controller as: ${guide.controller}.`));
    }
    panel.append(section);
  }

  if (guide.notes) {
    const section = el(doc, 'section', 'guide-sec');
    section.append(el(doc, 'h4', null, "The Internet Archive's notes"));
    for (const p of paragraphs(guide.notes)) {
      section.append(el(doc, 'p', null, p));
    }
    // The honesty rule, rendered. Only when the keys named would be the wrong
    // ones for the player actually on screen.
    if (guide.notesFor === ROUTE.ARCHIVE && route !== ROUTE.ARCHIVE) {
      section.append(el(doc, 'p', 'guide-note',
        'Written for the Internet Archive\'s own player, so any keys it names '
        + 'are theirs, not the ones above.'));
    }
    panel.append(section);
  }

  if (guide.manuals?.length) {
    const section = el(doc, 'section', 'guide-sec');
    section.append(el(doc, 'h4', null, 'Manuals and scans'));
    const list = el(doc, 'ul', 'guide-files');
    for (const file of guide.manuals) {
      if (!file?.url) continue;
      const link = el(doc, 'a', null, file.name || 'Document');
      link.href = file.url;
      link.target = '_blank';
      link.rel = 'noopener noreferrer';
      const item = el(doc, 'li');
      item.append(link);
      const size = fileSize(file.sizeBytes);
      if (size) item.append(el(doc, 'span', 'guide-size', ` ${size}`));
      list.append(item);
    }
    section.append(list);
    panel.append(section);
  }

  return panel;
}
