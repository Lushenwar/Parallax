# Parallax — Shadow Traffic Replicator

An HTTP reverse proxy that mirrors a configurable share of live production traffic to a second
backend, without ever putting that second backend on the client's critical path — plus a Next.js
control plane for watching and retuning it while it runs.

Mirrored responses are compared against what the client was actually served, so the point of the
tool is the diff feed: **which requests the two backends disagreed about, and how.**

* **`/` (Go)** — the proxy engine. Architecture and phase notes: [`PROXY.md`](PROXY.md).
* **`dashboard/` (Next.js)** — the control plane. Plan and notes: [`CLAUDE.md`](CLAUDE.md).

---

## Running it

Three processes. The defaults already line up, so no configuration is needed for a local run.

```bash
# 1 — throwaway primary (:9000) and shadow (:9001) backends
go run ./loadtest/backends

# 2 — the proxy, listening on :8080
PRIMARY_URL=http://127.0.0.1:9000 SHADOW_URL=http://127.0.0.1:9001 \
  DIFF_IGNORE=requestId,servedAt go run .

# 3 — the dashboard, on http://localhost:3000
cd dashboard && npm install && npm run dev
```

Then send traffic at the proxy and watch it in the dashboard:

```bash
curl localhost:8080/checkout
```

The primary answers you; a copy lands at the shadow backend with `X-Shadow-Traffic: true`, and the
counters move. Drag the sample rate slider and the mix changes live.

### Catching a regression

The throwaway backends disagree on purpose: the shadow returns `"total": 99` where the primary
returns `100`. That is what the Response Diffs panel is for.

```console
$ curl -s localhost:8080/api/diffs
{"enabled":true,"matches":0,"mismatches":1,"diffs":[{"at":"...","method":"GET",
 "path":"/checkout","primaryStatus":200,"shadowStatus":200,
 "reasons":["total: primary 100, shadow 99"]}]}
```

Both backends also return a `requestId` and `servedAt` that differ on every call. `DIFF_IGNORE`
is what keeps those out of the feed — drop it from the command above and every request reports
two false differences alongside the real one. That is the central problem with response
comparison, and the ignore list is the knob for it.

Comparison covers status code, `Content-Type`, and the response body. JSON bodies are compared
structurally, so key order and whitespace are not differences; anything else is compared byte for
byte. Paths in the ignore list use `.` for nesting and `*` for any one segment: `items.*.id`.

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
| `DIFF_BUFFER` | `100` | Mismatches kept for the dashboard; `0` disables comparison entirely |
| `DIFF_IGNORE` | *(empty)* | Comma-separated JSON paths whose differences are expected, e.g. `createdAt,items.*.id` |
| `SHADOW_IGNORE_PATHS` | `/health,/healthz,/readyz,/livez,/metrics` | `path.Match` globs never mirrored, e.g. `/internal/*` |
| `SHADOW_TRACE_COOKIE` | *(unset)* | Session cookie to key sampling on when a request carries no trace header |
| `PROXY_API_TOKEN` | *(unset)* | Bearer token required on `/api/*`; unset = no auth |
| `METRICS_PATH` | `/metrics` | expvar endpoint; empty disables |
| `DASHBOARD_ORIGIN` | `http://localhost:3000` | Sole allowed CORS origin for `/api/*` |
| `NEXT_PUBLIC_PROXY_URL` | `http://localhost:8080` | Where the dashboard looks for the proxy |
| `NEXT_PUBLIC_PROXY_TOKEN` | *(unset)* | Token the dashboard sends; must match `PROXY_API_TOKEN` |

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
2. **`/api/config` needs authentication.** Set `PROXY_API_TOKEN` and the matching
   `NEXT_PUBLIC_PROXY_TOKEN`, and every `/api/*` call must present it as
   `Authorization: Bearer <token>`. Unset, there is no auth at all — CORS pins the endpoint to one
   origin, but CORS is a rule browsers agree to follow and curl has never agreed to anything, so
   anything that can reach the port can retune live traffic.

   Note what the token is and is not: `NEXT_PUBLIC_` puts it in the browser bundle, so it
   authenticates *the dashboard* to the proxy, not *the operator* to the dashboard. It closes the
   open port; it does not gate who may open the dashboard. That still wants real auth in front of
   the dashboard itself.

Until then, run it locally.

---

## Known limits

* Comparison holds the primary response in memory until the shadow answers, capped at 1MB. Larger responses are compared up to the cap and flagged as truncated.
* Only status, `Content-Type` and body are compared. Other headers are ignored as a matter of course — they differ between two backends for reasons that have nothing to do with a regression.
* SIGTERM drains in-flight primary requests (15s cap); queued mirrors are still dropped, by design.
* Percentiles are windowed over the last 2048 requests per path, so they answer "how is it now",
  not "how was it this morning". There is no history and no charting — every read is instantaneous.
  `avgPrimaryLatencyMs` and `avgShadowLatencyMs` are still lifetime means, kept for compatibility.
* Proxy overhead is measured as total handler time minus the primary backend's own round trip and
  body read. It is honest about what the process costs in the path; it does not capture kernel-side
  cost, or the extra network hop if the proxy and backend are on different machines.
* Sampling hashes `X-Trace-Id`, `traceparent` (trace-id field), `X-Request-Id`,
  `X-Correlation-Id`, or `SHADOW_TRACE_COOKIE`, so whole flows are mirrored or skipped together.
  Traffic carrying none of those still falls back to a per-request coin flip, and still produces
  partial flows.
* Path filtering is `path.Match`, so `*` does not cross a `/`: `/internal/*` excludes
  `/internal/debug` but not `/internal/a/b`.
* WebSockets and SSE pass through to the primary and are never mirrored.
* `maxBodySizeMB` is reported by the API but is a compile-time constant in the engine.
