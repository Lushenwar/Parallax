import assert from 'node:assert/strict';
import { test } from 'node:test';
import { deriveHealth, freshness } from './health.ts';

const NOW = 1_000_000;

test('first load with nothing yet reads as connecting', () => {
  const h = deriveHealth({ loading: true, error: null, hasData: false, lastUpdatedAt: null, now: NOW });
  assert.equal(h.level, 'connecting');
});

test('a clean poll reads as connected and reports freshness', () => {
  const h = deriveHealth({
    loading: false,
    error: null,
    hasData: true,
    lastUpdatedAt: NOW - 3000,
    now: NOW,
  });
  assert.equal(h.level, 'connected');
  assert.match(h.detail, /3s ago/);
});

test('a failure with no data ever received reads as down', () => {
  const h = deriveHealth({
    loading: false,
    error: new Error('Cannot reach the proxy at http://localhost:8080'),
    hasData: false,
    lastUpdatedAt: null,
    now: NOW,
  });
  assert.equal(h.level, 'down');
  assert.match(h.detail, /Cannot reach the proxy/);
});

test('a failure after a good poll keeps last-known data and says it is frozen', () => {
  const h = deriveHealth({
    loading: false,
    error: new Error('Proxy did not respond within 4000ms'),
    hasData: true,
    lastUpdatedAt: NOW - 12_000,
    now: NOW,
  });
  assert.equal(h.level, 'stale');
  assert.match(h.detail, /did not respond/);
  assert.match(h.detail, /12s ago/);
});

test('freshness scales from seconds to hours', () => {
  assert.equal(freshness(null, NOW), 'never');
  assert.equal(freshness(NOW, NOW), 'just now');
  assert.equal(freshness(NOW - 45_000, NOW), '45s ago');
  assert.equal(freshness(NOW - 90_000, NOW), '1m ago');
  assert.equal(freshness(NOW - 7_200_000, NOW), '2h ago');
});
