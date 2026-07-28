import { test } from 'node:test';
import assert from 'node:assert/strict';
import { PlaybackError, FAILURE, describe, nextTiers, DESCRIPTIONS, ESCALATION } from './failures.js';
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

test('every failure code has a real entry in the descriptions and escalation maps', () => {
  for (const code of Object.values(FAILURE)) {
    assert.ok(
      Object.prototype.hasOwnProperty.call(DESCRIPTIONS, code),
      `${code} is missing from DESCRIPTIONS`
    );
    assert.ok(
      Object.prototype.hasOwnProperty.call(ESCALATION, code),
      `${code} is missing from ESCALATION`
    );
  }
});

test('PlaybackError carries its code', () => {
  const err = new PlaybackError(FAILURE.CORS_BLOCKED);
  assert.equal(err.code, FAILURE.CORS_BLOCKED);
  assert.ok(err instanceof Error);
});

test('PlaybackError message equals the description when no detail is given', () => {
  const err = new PlaybackError(FAILURE.DEAD_STREAM);
  assert.equal(err.message, describe(FAILURE.DEAD_STREAM));
});

test('PlaybackError message includes the detail when one is given', () => {
  const err = new PlaybackError(FAILURE.DEAD_STREAM, 'timed out after 5s');
  assert.ok(err.message.includes(describe(FAILURE.DEAD_STREAM)));
  assert.ok(err.message.includes('timed out after 5s'));
  assert.notEqual(err.message, describe(FAILURE.DEAD_STREAM));
});

test('PlaybackError conforms to the Error subclass contract', () => {
  const err = new PlaybackError(FAILURE.CORS_BLOCKED);
  assert.equal(err.name, 'PlaybackError');
  assert.ok(err instanceof PlaybackError);
  assert.equal(typeof err.stack, 'string');
  assert.ok(err.stack.length > 0);
});
