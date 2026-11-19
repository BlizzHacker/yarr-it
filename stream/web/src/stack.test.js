import { test } from 'node:test';
import assert from 'node:assert/strict';
import { stackRows, requestRows, MODE_LABEL } from './stack.js';

const stack = {
  mode: 'mixed',
  domains: {
    video: {
      mode: 'mixed',
      roles: {
        acquisition: [
          { id: 'radarr', name: 'Radarr' },
          { id: 'yarrit', name: 'Yarr.It engine', engine: 'yarrit' },
        ],
        requests: [{ id: 'seerr', name: 'Seerr' }],
      },
    },
    game: { mode: 'unified', roles: { acquisition: [{ id: 'romarr', name: 'ROMarr', engine: 'romarr' }] } },
  },
};

test('every stack mode the server can send has words', () => {
  for (const m of ['unified', 'bring-your-own', 'mixed', 'empty']) assert.ok(MODE_LABEL[m], m);
});

test('stack rows keep routing order and join health by id', () => {
  const rows = stackRows(stack, [
    { id: 'radarr', health: { state: 'healthy' } },
    { id: 'seerr', health: { state: 'auth_failed' } },
  ]);
  assert.deepEqual(rows.map((r) => r.domain), ['game', 'video']);
  const video = rows[1];
  assert.deepEqual(video.acquire.map((m) => m.id), ['radarr', 'yarrit']);
  assert.equal(video.acquire[0].state, 'healthy');
  assert.equal(video.acquire[1].state, 'unknown', 'no health yet is unknown, not healthy');
  assert.equal(video.acquire[1].engine, 'yarrit');
  assert.equal(video.requests[0].state, 'auth_failed');
  assert.equal(video.label, 'Films & series');
});

test('a pending request a backend will not let us decide explains itself', () => {
  const rows = requestRows({ items: [
    { id: 'seerr:9', title: 'Okami', status: 'pending', decidable: true, providerId: 'seerr' },
    { id: 'ggrequestz:4', title: 'Okami', year: 2006, status: 'pending', decidable: false, providerId: 'ggrequestz' },
    { id: 'ggrequestz:5', title: 'Ico', status: 'available', decidable: false, providerId: 'ggrequestz' },
  ] });
  assert.equal(rows[0].why, '');
  assert.match(rows[1].why, /GG Requestz/);
  assert.equal(rows[1].title, 'Okami (2006)');
  assert.equal(rows[2].why, '', 'only a pending request needs a decision');
  assert.equal(rows[2].statusLabel, 'Available');
});

test('an empty or missing queue is an empty list, not a crash', () => {
  assert.deepEqual(requestRows(undefined), []);
  assert.deepEqual(stackRows(undefined), []);
});
