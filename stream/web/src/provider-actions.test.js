import test from 'node:test';
import assert from 'node:assert/strict';
import { offsiteVerb } from './provider-actions.js';

test('each provider capability gets an explicit honest verb', () => {
  assert.equal(offsiteVerb('play'), 'PLAY THERE');
  assert.equal(offsiteVerb('download'), 'DOWNLOAD');
  assert.equal(offsiteVerb('open'), 'OPEN PAGE');
});

test('an unknown action never becomes a download promise', () => {
  assert.equal(offsiteVerb(''), 'OPEN PAGE');
  assert.equal(offsiteVerb('future-capability'), 'OPEN PAGE');
});
