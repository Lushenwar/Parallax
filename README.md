# Parallax — Shadow Traffic Replicator

An HTTP reverse proxy that mirrors a configurable share of live production traffic to a second
backend, without ever putting that second backend on the client's critical path — plus a Next.js
control plane for watching and retuning it while it runs.

**Scope:** this is a mirror, not a response comparator. The shadow response is drained and
discarded unread, so Parallax will not tell you that the two backends disagree. Comparison is the
next thing to build; see [Known limits](#known-limits).

* **`/` (Go)** — the proxy engine. Architecture and phase notes: [`PROXY.md`](PROXY.md).
* **`dashboard/` (Next.js)** — the control plane. Plan and notes: [`CLAUDE.md`](CLAUDE.md).

---

## Running it

Three processes. The defaults already line up, so no configuration is needed for a local run.

```bash
# 1 — throwaway primary (:9000) and shadow (:9001) backends
go run ./loadtest/backends

# 2 — the proxy, listening on :8080
PRIMARY_URL=http://127.0.0.1:9000 SHADOW_URL=http://127.0.0.1:9001 go run .

# 3 — the dashboard, on http://localhost:3000
cd dashboard && npm install && npm run dev
```

Then send traffic at the proxy and watch it in the dashboard:

```bash
curl -X POST localhost:8080/orders -H 'Content-Type: application/json' -d '{"qty":2}'
```

The primary answers you; a copy lands at the shadow backend with `X-Shadow-Traffic: true`, and the
counters move. Drag the sample rate slider and the mix changes live.

### Configuration

| Env var | Default | Meaning |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | Address the proxy listens on |
| `PRIMARY_URL` | *(required)* | Production backend |
| `SHADOW_URL` | *(unset)* | Shadow backend; unset = plain reverse proxy |
| `SHADOW_SAMPLE_RATE` | `100` | Percent of traffic to mirror, 0–100 (also settable live from the dashboard) |
| `SHADOW_METHODS` | `GET,HEAD` | Methods eligible for mirroring; `*` mirrors all. **See the warning below before widening this.** |
| `SHADOW_QUEUE_SIZE` | `1024` | Bounded dispatch queue depth; full = drop |
| `SHADOW_WORKERS` | `64` | Goroutines draining the queue |
| `METRICS_PATH` | `/metrics` | expvar endpoint; empty disables |
| `DASHBOARD_ORIGIN` | `http://localhost:3000` | Sole allowed CORS origin for `/api/*` |
| `NEXT_PUBLIC_PROXY_URL` | `http://localhost:8080` | Where the dashboard looks for the proxy |

#### Mirroring writes

A mirrored request is a real request. Parallax defaults to `GET,HEAD` because
mirroring `POST /charge` makes the shadow backend attempt a second charge, and
mirroring `DELETE /users/42` deletes a second row.

Only set `SHADOW_METHODS` wider once the shadow environment shares **nothing**
with production — no payment sandbox, mailer, SMS gateway, third-party API key,
queue, or database. Parallax cannot check that for you; it only refuses to
assume it. Requests outside the allowlist reach the primary untouched and are
counted in `shadow_skipped_method_total`.

### Checks

```bash
go test ./proxy/...                                    # includes the paused-shadow stress test
cd dashboard && npm test && npm run typecheck && npm run lint
k6 run loadtest/primary_latency.js                     # optional: primary latency budget
```

---

## Deployment

There is no deploy step yet, on purpose.

The dashboard polls the proxy **from the browser**, so a hosted dashboard is only useful if the
machine viewing it can reach a running proxy. And the proxy itself cannot go on a serverless host:
it is a long-lived process holding connection pools and background workers, which is the opposite
shape of a request-scoped function.

Two things have to be true before hosting this somewhere makes sense:

1. **The proxy needs a reachable HTTPS endpoint.** An HTTPS page fetching `http://localhost:8080`
   is blocked outright in Safari and only tolerated in Chrome/Firefox because they treat
   `localhost` as trustworthy. `DASHBOARD_ORIGIN` would also have to name the hosted origin.
2. **`/api/config` needs authentication.** It has none. CORS pins it to a single origin, but that
   is not an access control — anything that can reach the port can set the sample rate or flip the
   mirroring kill switch on live traffic. Fine on a private port; not fine on a public one.

Until then, run it locally.

---

## Known limits

* Latency is a lifetime running mean, not windowed — a spike will not show up as one.
* SIGTERM drains in-flight primary requests (15s cap); queued mirrors are still dropped, by design.
* No percentiles. Latency is a lifetime mean, so p95/p99 — the numbers that say whether a proxy in the request path is acceptable — cannot be read from a running process.
* No measured overhead figure: the load tests show mirroring does not degrade the primary, which is not the same as proxy-vs-direct overhead.
* No path filtering; the method allowlist is global, so `/health` is mirrored like anything else.
* Sampling is an independent per-request coin flip, not reproducible per trace ID. For a stateful shadow this means partial flows: a mirrored `POST /cart/add` whose `POST /login` was never mirrored will 401.
* WebSockets and SSE pass through to the primary and are never mirrored.
* `maxBodySizeMB` is reported by the API but is a compile-time constant in the engine.
