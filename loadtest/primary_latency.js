// k6 load test: does the primary path hold its latency budget while the proxy
// is mirroring? Run it twice — once with SHADOW_URL unset, once with a shadow
// backend that is slow or paused. The two p95s should be indistinguishable.
//
//   k6 run loadtest/primary_latency.js
//   k6 run -e TARGET=http://proxy:8080 -e RATE=1000 -e P95_MS=25 loadtest/primary_latency.js
//
// Same shape with `hey`, if you only need a smoke check:
//   hey -z 60s -q 500 -c 100 -m POST -T application/json -d '{"qty":1}' http://127.0.0.1:8080/orders

import http from 'k6/http';
import { check } from 'k6';

const TARGET = __ENV.TARGET || 'http://127.0.0.1:8080';
const RATE = Number(__ENV.RATE || 500);
const DURATION = __ENV.DURATION || '60s';
const P95_MS = Number(__ENV.P95_MS || 50);

export const options = {
  scenarios: {
    steady: {
      // Arrival-rate, not VU-based: latency regressions show up as queueing
      // instead of quietly lowering throughput.
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 100,
      maxVUs: 1000,
    },
  },
  thresholds: {
    // The whole point: the client never pays for the shadow backend.
    http_req_duration: [`p(95)<${P95_MS}`],
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

export default function () {
  const res = http.post(`${TARGET}/orders`, JSON.stringify({ qty: 1, iter: __ITER }), {
    headers: { 'Content-Type': 'application/json' },
  });
  check(res, {
    'primary answered 2xx': (r) => r.status >= 200 && r.status < 300,
    'not a loop-guard rejection': (r) => r.status !== 508,
  });
}
