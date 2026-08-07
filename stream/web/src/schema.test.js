/**
 * The web client asserted against the same conformance fixture as the Go
 * server.
 *
 * This is the test that did not exist when the vocabulary drifted. Both sides
 * now read one schema file and are held to one fixture, so a change that suits
 * only one language fails the other's suite.
 */

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import {
  canonicalDomain, sameDomain, domainOfType, verbFor, labelFor,
  allDomains, hasRole, hasCapability, DOMAINS,
} from './schema.js';

const here = dirname(fileURLToPath(import.meta.url));
const fixture = JSON.parse(
  readFileSync(join(here, '../../search/schema_conformance.json'), 'utf8'),
);

test('every name resolves the way the Go server resolves it', () => {
  for (const [input, want] of fixture.resolves) {
    assert.equal(canonicalDomain(input), want, `canonicalDomain(${JSON.stringify(input)})`);
  }
});

test('an unknown name filters nothing rather than everything', () => {
  for (const input of fixture.unknown) {
    assert.equal(canonicalDomain(input), '', `canonicalDomain(${JSON.stringify(input)})`);
  }
});

test('spellings of the same domain compare equal', () => {
  for (const [a, b] of fixture.sameDomain) {
    assert.ok(sameDomain(a, b), `${a} and ${b} should match`);
  }
});

test('domains that must stay separate do', () => {
  for (const [a, b] of fixture.distinct) {
    assert.ok(!sameDomain(a, b), `${a} and ${b} must not match`);
  }
});

test('a media type knows its own domain', () => {
  assert.equal(domainOfType('episode'), 'video');
  assert.equal(domainOfType('audiobook'), 'literature');
  assert.equal(domainOfType('issue'), 'comic');
  assert.equal(domainOfType('scheduled_channel'), 'video');
  assert.equal(domainOfType('nonsense'), '');
});

test('every domain carries a verb and a label for the UI', () => {
  for (const id of allDomains()) {
    assert.ok(verbFor(id), `${id} has no verb`);
    assert.ok(labelFor(id), `${id} has no label`);
  }
  // The verb is what stops books being described as something you watch.
  assert.equal(verbFor('movies'), 'watch');
  assert.equal(verbFor('audiobook'), 'read');
  assert.equal(verbFor('games'), 'play');
});

test('capabilities are asked about, never assumed', () => {
  const radarr = { roles: ['discovery', 'acquisition', 'library'], capabilities: ['health', 'search', 'request'] };
  assert.ok(hasRole(radarr, 'acquisition'));
  // The whole point: Radarr has no guide, and asking must be cheap and safe.
  assert.ok(!hasRole(radarr, 'linear-tv'));
  assert.ok(!hasCapability(radarr, 'guide'));
  // A malformed or missing provider must answer false, not throw, or one bad
  // entry in settings takes down the screen that would let you fix it.
  assert.ok(!hasRole(undefined, 'library'));
  assert.ok(!hasCapability({}, 'search'));
});

test('the schema the client reads is the one the server embeds', () => {
  const served = JSON.parse(
    readFileSync(join(here, '../../search/schema.json'), 'utf8'),
  );
  assert.deepEqual(Object.keys(served.domains).sort(), Object.keys(DOMAINS).sort());
});
