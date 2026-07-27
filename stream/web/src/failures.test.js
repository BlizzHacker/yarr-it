import { test } from 'node:test';
import assert from 'node:assert/strict';
import { PlaybackError, FAILURE, describe, nextTiers } from './failures.js';
import { TIER } from './source.js';

test('mixed content and cors both escalate to gateway then relay', () => {
  assert.deepEqual(nextTiers(FAILURE.MIXED_CONTENT), [TIER.GATEWAY, TIER.RELAY]);
  assert.deepEqual(nextTiers(FAILURE.CORS_BLOCKED), [TIER.GATEWAY, TIER.RELAY]);
});

test('a dead stream is terminal - no tier can fix it', () => {
  assert.deepEqual(nextTiers(FAILURE.DEAD_STREAM), []);
  assert.deepEqual(nextTiers(FAILURE.UNSUPPORTED_CODEC), []);
});

test('an exhausted budget is terminal but points at the gateway', () => {
  assert.deepEqual(nextTiers(FAILURE.BUDGET_EXHAUSTED), []);
  assert.match(describe(FAILURE.BUDGET_EXHAUSTED), /gateway/i);
});

test('every failure code has a description', () => {
  for (const code of Object.values(FAILURE)) {
    assert.equal(typeof describe(code), 'string');
    assert.ok(describe(code).length > 0, `${code} has no description`);
  }
});

test('PlaybackError carries its code', () => {
  const err = new PlaybackError(FAILURE.CORS_BLOCKED);
  assert.equal(err.code, FAILURE.CORS_BLOCKED);
  assert.ok(err instanceof Error);
});
