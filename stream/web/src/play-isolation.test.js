import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isolatedHref, REASON, ROUTE } from './play.js';

/**
 * The offer that answers `needs_isolation`.
 *
 * This refusal is the odd one out: it is not about the item, the core, or
 * anything the visitor owns or could go and find. It is about which document
 * they are standing on, and there is another document on this same site where
 * it does not apply. So the block is answered with an address.
 *
 * The rule these tests exist to hold is that the offer must never appear where
 * it would not help -- an offer that leads to the same refusal is worse than no
 * offer, because somebody follows it.
 */

const refused = (code) => ({
  id: 'msdos_Oregon_Trail_The_1990',
  route: ROUTE.ARCHIVE,
  playable: true,
  reasons: [{ code, detail: 'because.' }],
});

test('a needs_isolation refusal is answered with the isolated page', () => {
  assert.equal(
    isolatedHref(refused(REASON.NEEDS_ISOLATION), { isolated: false }),
    '/play/archive/msdos_Oregon_Trail_The_1990',
  );
});

test('no offer is made on a page that is already isolated', () => {
  // It would be a link to itself, and it would appear at exactly the moment the
  // block had been lifted.
  assert.equal(isolatedHref(refused(REASON.NEEDS_ISOLATION), { isolated: true }), '');
});

test('no offer is made for a refusal isolation would not lift', () => {
  for (const code of [REASON.NEEDS_BIOS, REASON.NO_CORE, REASON.TOO_LARGE, REASON.NOT_FOUND]) {
    assert.equal(isolatedHref(refused(code), { isolated: false }), '', code);
  }
});

test('no offer without an item to make it about', () => {
  assert.equal(isolatedHref(null, { isolated: false }), '');
  assert.equal(isolatedHref({ reasons: [{ code: REASON.NEEDS_ISOLATION }] }, { isolated: false }), '');
});

test('an identifier is escaped on the way into the address', () => {
  const verdict = { ...refused(REASON.NEEDS_ISOLATION), id: 'a b/c' };
  assert.equal(isolatedHref(verdict, { isolated: false }), '/play/archive/a%20b%2Fc');
});
