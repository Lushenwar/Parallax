/**
 * Run with `npm test` — Node's built-in test runner, native TypeScript, no
 * test framework dependency.
 */
import assert from 'node:assert/strict';
import { createServer, type Server } from 'node:http';
import { after, before, test } from 'node:test';

let server: Server;
let lastBody = '';
let lastAuth = '';
let requireToken = false;

before(async () => {
  server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (c: Buffer) => chunks.push(c));
    req.on('end', () => {
      lastBody = Buffer.concat(chunks).toString();
      lastAuth = req.headers.authorization ?? '';
      res.setHeader('Content-Type', 'application/json');

      if (requireToken && lastAuth !== 'Bearer s3cret') {
        res.statusCode = 401;
        res.end(JSON.stringify({ error: 'missing or invalid bearer token' }));
        return;
      }

      if (req.url === '/api/metrics') {
        res.end(
          JSON.stringify({
            primaryRequestsTotal: 14250,
            shadowRequestsDispatched: 7125,
            shadowRequestsDropped: 12,
            activeConnections: 45,
            avgPrimaryLatencyMs: 14.2,
            avgShadowLatencyMs: 85.5,
            primaryLatency: { p50: 12.1, p95: 40.5, p99: 96.4, max: 210.0, samples: 2048 },
            shadowLatency: { p50: 80.0, p95: 180.2, p99: 340.9, max: 900.1, samples: 1024 },
            proxyOverhead: { p50: 0.21, p95: 0.9, p99: 2.4, max: 11.7, samples: 2048 },
          }),
        );
        return;
      }
      if (req.url === '/api/config' && req.method === 'POST') {
        const patch: Record<string, unknown> = JSON.parse(lastBody);
        if (typeof patch.sampleRate === 'number' && patch.sampleRate > 100) {
          res.statusCode = 400;
          res.end(JSON.stringify({ error: 'shadow sample rate must be between 0 and 100' }));
          return;
        }
        res.end(JSON.stringify({ sampleRate: patch.sampleRate ?? 50, maxBodySizeMB: 10, shadowEnabled: true }));
        return;
      }
      res.end(JSON.stringify({ sampleRate: 50, maxBodySizeMB: 10, shadowEnabled: true }));
    });
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  if (address === null || typeof address === 'string') throw new Error('no port');
  process.env.NEXT_PUBLIC_PROXY_URL = `http://127.0.0.1:${address.port}`;
});

after(() => server.close());

test('fetchMetrics parses the proxy metrics shape', async () => {
  const { fetchMetrics } = await import('./proxy-client.ts');
  const m = await fetchMetrics();

  assert.equal(m.primaryRequestsTotal, 14250);
  assert.equal(m.shadowRequestsDropped, 12);
  assert.equal(m.avgShadowLatencyMs, 85.5);
  // The tail is the number the proxy is judged on, so it has to survive the trip.
  assert.equal(m.primaryLatency.p99, 96.4);
  assert.equal(m.proxyOverhead.p99, 2.4);
  assert.equal(m.proxyOverhead.samples, 2048);
});

test('fetchConfig parses the proxy config shape', async () => {
  const { fetchConfig } = await import('./proxy-client.ts');
  const c = await fetchConfig();

  assert.equal(c.sampleRate, 50);
  assert.equal(c.maxBodySizeMB, 10);
  assert.equal(c.shadowEnabled, true);
});

test('updateConfig posts a JSON patch and returns the new config', async () => {
  const { updateConfig } = await import('./proxy-client.ts');
  const c = await updateConfig({ sampleRate: 25 });

  assert.equal(JSON.parse(lastBody).sampleRate, 25);
  assert.equal(c.sampleRate, 25);
});

test('a rejected update surfaces the proxy error message, not a generic one', async () => {
  const { updateConfig, ProxyError } = await import('./proxy-client.ts');

  await assert.rejects(
    () => updateConfig({ sampleRate: 999 }),
    (err: unknown) => {
      assert.ok(err instanceof ProxyError);
      assert.equal(err.status, 400);
      assert.equal(err.unreachable, false);
      assert.match(err.message, /between 0 and 100/);
      return true;
    },
  );
});

test('an unreachable proxy is flagged as unreachable, not as a bad response', async () => {
  const previous = process.env.NEXT_PUBLIC_PROXY_URL;
  process.env.NEXT_PUBLIC_PROXY_URL = 'http://127.0.0.1:1'; // nothing listens here

  const { fetchMetrics, ProxyError } = await import('./proxy-client.ts');
  await assert.rejects(
    () => fetchMetrics(),
    (err: unknown) => {
      assert.ok(err instanceof ProxyError);
      assert.equal(err.unreachable, true);
      assert.equal(err.status, null);
      return true;
    },
  );

  process.env.NEXT_PUBLIC_PROXY_URL = previous;
});

test('the control-plane token is sent as a bearer header when configured', async () => {
  process.env.NEXT_PUBLIC_PROXY_TOKEN = 's3cret';
  requireToken = true;

  const { fetchMetrics } = await import('./proxy-client.ts');
  await fetchMetrics();
  assert.equal(lastAuth, 'Bearer s3cret');

  // A wrong token must fail loudly with something an operator can act on, not
  // with a generic "proxy returned 401".
  process.env.NEXT_PUBLIC_PROXY_TOKEN = 'wrong';
  const { ProxyError } = await import('./proxy-client.ts');
  await assert.rejects(
    () => fetchMetrics(),
    (err: unknown) => {
      assert.ok(err instanceof ProxyError);
      assert.equal(err.status, 401);
      assert.match(err.message, /NEXT_PUBLIC_PROXY_TOKEN/);
      return true;
    },
  );

  requireToken = false;
  delete process.env.NEXT_PUBLIC_PROXY_TOKEN;
});
