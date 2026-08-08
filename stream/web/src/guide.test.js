import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  renderGuide, hasGuide, paragraphs, controlsTitle, controlsCaveat,
} from './guide.js';
import { ROUTE } from './play.js';

/**
 * The panel exists because a booted emulator with no instructions is a picture
 * you cannot touch. So the tests are about the two ways that can still go wrong:
 * saying nothing, and saying something confidently wrong.
 *
 * The confidently-wrong case is the sharp one. archive.org's own
 * `emulator_instructions` describes THEIR player's keys -- "The 1 key pushes the
 * SELECT switch" is true on archive.org and false here, where 1 is quick-save.
 * Rendering that text unlabelled beside our player would be worse than an empty
 * panel: somebody presses the key they were given, nothing happens, and they
 * conclude the emulator is broken.
 */

// The smallest document that can hold an element tree. Enough for a module that
// only ever calls createElement, append and textContent -- and using it rather
// than a DOM library is itself part of the test, because anything this file
// needed beyond these three calls would be a sign it had started building HTML.
function fakeDoc() {
  const make = (tag) => ({
    tagName: tag.toUpperCase(),
    className: '',
    children: [],
    attributes: {},
    _text: '',
    set textContent(value) { this._text = String(value); this.children = []; },
    get textContent() {
      return this._text + this.children.map((c) => (typeof c === 'string' ? c : c.textContent)).join('');
    },
    setAttribute(name, value) { this.attributes[name] = value; },
    append(...nodes) { this.children.push(...nodes); },
  });
  return { createElement: make };
}

/** Every element in the tree, flattened. */
function walk(node, out = []) {
  if (!node || typeof node === 'string') return out;
  out.push(node);
  for (const child of node.children) walk(child, out);
  return out;
}

const find = (root, tag) => walk(root).filter((n) => n.tagName === tag);
const text = (root) => walk(root).map((n) => n._text).join(' ');

// Shaped exactly as search/play_guide.go emits it for the live River Raid item.
const riverRaid = {
  description: 'River Raid is a scrolling shooter designed by Carol Shaw.\n\n'
    + 'The player flies a fighter jet over the River of No Return.',
  notes: 'The 1 key pushes the SELECT switch, and the 2 key pushes the RESET switch. '
    + 'Use the arrow keys to move.',
  notesFor: 'archive',
  controller: 'joystick',
  controlsFor: 'Atari 2600',
  controls: [
    { button: 'FIRE', key: 'X' },
    { button: 'SELECT', key: 'V' },
    { button: 'RESET', key: 'Enter' },
    { button: 'UP', key: '↑' },
    { button: 'DOWN', key: '↓' },
    { button: 'LEFT', key: '←' },
    { button: 'RIGHT', key: '→' },
  ],
  manuals: [{ name: 'river_raid_manual.pdf', url: 'https://archive.org/download/x/m.pdf', sizeBytes: 2400000 }],
};

// --- is there anything to draw -----------------------------------------------

test('an empty guide draws nothing rather than an empty panel', () => {
  for (const nothing of [null, undefined, {}, { description: '' }, { controls: [] }]) {
    assert.equal(hasGuide(nothing), false, `${JSON.stringify(nothing)} counted as a guide`);
    assert.equal(renderGuide(nothing, { doc: fakeDoc() }), null);
  }
});

test('any one of the four pieces is enough to be worth drawing', () => {
  for (const some of [
    { description: 'A game.' },
    { notes: 'Press fire.' },
    { controls: [{ button: 'A', key: 'Z' }] },
    { manuals: [{ name: 'm.pdf', url: 'https://x/m.pdf' }] },
  ]) {
    assert.equal(hasGuide(some), true);
    assert.ok(renderGuide(some, { doc: fakeDoc() }));
  }
});

// --- what it says ------------------------------------------------------------

test('the key map answers "which key is fire" as a list, not a paragraph', () => {
  const panel = renderGuide(riverRaid, { doc: fakeDoc(), route: ROUTE.EMULATORJS });

  const buttons = find(panel, 'DT').map((n) => n._text);
  const keys = find(panel, 'DD').map((n) => n._text);
  assert.deepEqual(buttons, ['FIRE', 'SELECT', 'RESET', 'UP', 'DOWN', 'LEFT', 'RIGHT']);
  assert.equal(keys[0], 'X', 'fire on a 2600 is x in EmulatorJS');
  assert.equal(buttons.length, keys.length);
});

test('the key map is headed with the machine it belongs to', () => {
  const panel = renderGuide(riverRaid, { doc: fakeDoc(), route: ROUTE.EMULATORJS });
  assert.ok(find(panel, 'H4').some((h) => h._text === 'Atari 2600 controls'));
});

test('a control with no key is dropped rather than shown blank', () => {
  const panel = renderGuide({
    controlsFor: 'Atari 2600',
    controls: [{ button: 'FIRE', key: 'X' }, { button: 'COLOR', key: '' }, { button: '', key: 'Q' }],
  }, { doc: fakeDoc() });
  assert.deepEqual(find(panel, 'DT').map((n) => n._text), ['FIRE']);
});

test('the description is rendered as paragraphs, one element each', () => {
  const panel = renderGuide(riverRaid, { doc: fakeDoc() });
  const paras = find(panel, 'P').map((n) => n._text);
  assert.ok(paras.includes('River Raid is a scrolling shooter designed by Carol Shaw.'));
  assert.ok(paras.includes('The player flies a fighter jet over the River of No Return.'));
});

test('a manual is a link that opens away from the player', () => {
  const panel = renderGuide(riverRaid, { doc: fakeDoc() });
  const [link] = find(panel, 'A');
  assert.equal(link.href, 'https://archive.org/download/x/m.pdf');
  assert.equal(link.target, '_blank');
  assert.equal(link.rel, 'noopener noreferrer');
  assert.match(text(panel), /2\.3 MB/);
});

// --- the honesty rule --------------------------------------------------------

// The sharp one. Their notes name THEIR keys, and in our player those keys do
// something else entirely.
test("the Archive's notes are labelled as theirs when our player is running", () => {
  const panel = renderGuide(riverRaid, { doc: fakeDoc(), route: ROUTE.EMULATORJS });
  const body = text(panel);

  assert.match(body, /The 1 key pushes the SELECT switch/, 'their notes are still worth reading');
  assert.match(body, /Written for the Internet Archive's own player/,
    'their keys were presented as though they were ours');
});

test("their notes need no disclaimer when their player is the one running", () => {
  const panel = renderGuide(riverRaid, { doc: fakeDoc(), route: ROUTE.ARCHIVE });
  assert.doesNotMatch(text(panel), /Written for the Internet Archive's own player/);
});

// The mirror image: on their route, OUR key map is still worth showing -- it is
// what switching would give you -- but it must not read as what the frame on
// screen responds to.
test('our key map is marked as ours when their player is running', () => {
  assert.equal(controlsTitle(riverRaid, ROUTE.ARCHIVE), 'Atari 2600 controls in the Yarr.It player');
  assert.equal(controlsTitle(riverRaid, ROUTE.EMULATORJS), 'Atari 2600 controls');

  assert.match(controlsCaveat(ROUTE.ARCHIVE), /own key map/);
  assert.equal(controlsCaveat(ROUTE.EMULATORJS), '', 'there is nothing to disclaim about the truth');

  assert.match(text(renderGuide(riverRaid, { doc: fakeDoc(), route: ROUTE.ARCHIVE })), /own key map/);
});

// On a phone the key map is not the answer, and the thing that IS the answer has
// to be said rather than discovered.
test('a touch player is told about the on-screen pad', () => {
  const ours = text(renderGuide(riverRaid, { doc: fakeDoc(), route: ROUTE.EMULATORJS }));
  assert.match(ours, /on-screen pad/);
  // Their player has no on-screen pad, so promising one there would be a lie.
  const theirs = text(renderGuide(riverRaid, { doc: fakeDoc(), route: ROUTE.ARCHIVE }));
  assert.doesNotMatch(theirs, /on-screen pad/);
});

test("the Archive's controller note is carried through", () => {
  assert.match(text(renderGuide(riverRaid, { doc: fakeDoc() })), /controller as: joystick/);
});

// --- text is text ------------------------------------------------------------

// The description belongs to whoever uploaded the item. The server flattens it
// to plain text; this is the second of the two locks, and it holds because
// nothing in the module assigns innerHTML at all.
test('markup in an item description becomes text, never elements', () => {
  const panel = renderGuide({
    description: '<img src=x onerror="alert(1)"> and <b>bold</b>',
    notes: '<script>alert(2)</script>',
    notesFor: 'archive',
  }, { doc: fakeDoc(), route: ROUTE.EMULATORJS });

  const tags = new Set(walk(panel).map((n) => n.tagName));
  assert.ok(!tags.has('IMG') && !tags.has('SCRIPT') && !tags.has('B'),
    `built elements from item text: ${[...tags].join(',')}`);
  assert.match(text(panel), /alert\(1\)/, 'the text itself is still shown, as text');
});

test('paragraphs splits on the blank line the server emits, not on every newline', () => {
  assert.deepEqual(paragraphs('one\ntwo\n\nthree'), ['one two', 'three']);
  assert.deepEqual(paragraphs(''), []);
  assert.deepEqual(paragraphs(null), []);
  assert.deepEqual(paragraphs('  \n\n  '), []);
});
