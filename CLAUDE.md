# CLAUDE.md — Shadow Traffic Replicator Dashboard (`/dashboard`)

## WORKFLOW: BRANCH + PR ONLY

No direct commits to `main`. Every change goes: `git checkout -b <branch>` → commit → `gh pr create`. A pre-commit hook (`.git/hooks/pre-commit`) enforces this locally by rejecting commits made while on `main`.

## CURRENT STATUS

╔══════════════════════════════════════════════════════════╗
║  DASHBOARD BUILD PROGRESS                       4/4 DONE ║
║  ████████████████████████████  COMPLETE                  ║
║  Phase 0: Next.js Setup & Proxy API Client      [DONE]   ║
║  Phase 1: Real-Time Metrics Overview Cards      [DONE]   ║
║  Phase 2: Dynamic Sampling Rate Control Slider  [DONE]   ║
║  Phase 3: Connection & Health Status Monitor    [DONE]   ║
╚══════════════════════════════════════════════════════════╝

Phase: Complete, plus the comparator (`proxy/diff.go`, `proxy/capture.go`, `/api/diffs`, `DiffFeed.tsx`)
and the runtime observability/safety pass (`proxy/latency.go`, `LatencyPanel.tsx`).
Status: Parallax compares shadow responses against what the client was served and reports the
mismatches — it is a response comparator, not just a mirror. Writes are off by default
(`SHADOW_METHODS`), SIGTERM drains, and CI runs both halves. p50/p95/p99 and measured proxy
overhead are readable at runtime, sampling is trace-coherent, `/health` and friends are excluded
from mirroring, and `/api/*` takes a bearer token when `PROXY_API_TOKEN` is set.
Update this as you finish each step.

**Dashboard checks:** `cd dashboard && npm test && npm run typecheck && npm run lint && npm run build`

### Dashboard source map
| File | Role |
|---|---|
| `src/app/layout.tsx` | Dark shell and navbar |
| `src/app/page.tsx` | Polls metrics + config, owns the write path |
| `src/components/MetricsGrid.tsx` | Six live stat cards |
| `src/components/ControlPanel.tsx` | Sample-rate slider, mirroring kill switch |
| `src/components/HealthStatus.tsx` | Header chip + stale-data banner |
| `src/components/LatencyPanel.tsx` | p50/p95/p99/max per path, plus measured proxy overhead |
| `src/components/DiffFeed.tsx` | Response-mismatch feed |
| `src/lib/proxy-client.ts` | Typed, timeout-bounded fetch wrapper |
| `src/lib/use-poll.ts` | Interval polling with in-flight guard |
| `src/lib/health.ts` | Pure connection-health derivation (unit tested) |

### Deferred
* No history or charting: every number is an instantaneous read, and percentiles are windowed over
  the last 2048 samples per path. Enough to see the tail now, not enough to see this morning.
* Overhead is measured as total handler time minus the backend's round trip and body read. It does
  not capture kernel-side cost or the extra network hop when the proxy and backend are on separate
  machines, so it is a floor on the true cost, not the whole of it.
* Sampling falls back to a per-request coin flip for traffic carrying no trace header and no
  `SHADOW_TRACE_COOKIE`. Such flows are still mirrored in pieces.
* Path filtering is `path.Match`, so `*` stops at a `/`. `/internal/*` does not cover
  `/internal/a/b`; needs explicit patterns or a prefix mode.
* `PROXY_API_TOKEN` is a shared bearer token, and the dashboard's copy ships in the browser bundle.
  It closes the open port; it does not identify who is using the dashboard. Real user auth in front
  of the dashboard is a separate job.
* `maxBodySizeMB` is reported but not editable — it is a compile-time constant in the Go engine.
* Latency windows are per-path, not per-route: one slow endpoint is averaged into the same p99 as
  everything else.

## WHAT THIS FILE IS

This document is the authoritative guide for developing the TypeScript/Next.js dashboard for the Shadow Traffic Replicator. The Go proxy engine is complete (5/5 phases, see `PROXY.md`); your job is to build a high-performance control plane interface that communicates with the Go proxy's JSON API endpoints.

> **Correction to the original brief:** the Go engine did *not* ship `/api/metrics` or `/api/config` — it served expvar JSON at `/metrics` with different field names, and the sample rate was immutable after startup. Phase 0 adds those endpoints to the Go side, because the dashboard's control plane is meaningless without them. `PROXY.md` remains the binding architecture doc for everything below the API surface.

---

## TECH STACK & REQUIREMENTS

* **Framework:** Next.js (App Router, Server & Client Components)
* **Language:** TypeScript (Strict mode enabled)
* **Styling:** Tailwind CSS
* **Icons / Components:** Lucide React (or clean, modular SVG components)
* **State & Data Fetching:** React Hooks (`useEffect`, `useState`) or polling hooks to sync live metrics from the Go proxy API.

---

## GO PROXY API SPECIFICATION (The Backend You Are Interfacing With)

The Go proxy exposes the following internal endpoints for the dashboard to consume:

1. **`GET /api/metrics`**
   * *Returns JSON:*
     ```json
     {
       "primaryRequestsTotal": 14250,
       "shadowRequestsDispatched": 7125,
       "shadowRequestsDropped": 12,
       "activeConnections": 45,
       "avgPrimaryLatencyMs": 14.2,
       "avgShadowLatencyMs": 85.5,
       "primaryLatency": { "p50": 12.1, "p95": 40.5, "p99": 96.4, "max": 210.0, "samples": 2048 },
       "shadowLatency":  { "p50": 80.0, "p95": 180.2, "p99": 340.9, "max": 900.1, "samples": 1024 },
       "proxyOverhead":  { "p50": 0.21, "p95": 0.9, "p99": 2.4, "max": 11.7, "samples": 2048 }
     }
     ```
   * `avg*` are lifetime means kept for compatibility. The `*Latency` objects are windowed over the
     most recent samples and are the numbers to judge the proxy on; `samples` says how much data is
     behind them, because a p99 over six requests is one request wearing a percentile's name.
   * `proxyOverhead` is total handler time minus the primary backend's own round trip and body
     read — what Parallax costs to have in the request path, measured on live traffic.
2. **`GET /api/config`**
   * *Returns JSON:*
     ```json
     {
       "sampleRate": 50.0,
       "maxBodySizeMB": 10,
       "shadowEnabled": true
     }
     ```
3. **`POST /api/config`**
   * *Accepts JSON body to update settings dynamically:*
     ```json
     {
       "sampleRate": 25.0,
       "shadowEnabled": true
     }
     ```

4. **`GET /api/diffs`**
   * *Returns JSON:*
     ```json
     {
       "enabled": true,
       "matches": 812,
       "mismatches": 3,
       "diffs": [
         {
           "at": "2026-07-30T19:40:13Z",
           "method": "GET",
           "path": "/checkout",
           "primaryStatus": 200,
           "shadowStatus": 200,
           "reasons": ["total: primary 100, shadow 99"]
         }
       ]
     }
     ```
   * `enabled: false` means comparison is off (`DIFF_BUFFER=0`) — distinct from "on and finding nothing".

### Trust boundary
`/api/config` mutates a proxy sitting in the live request path. Therefore:
* `sampleRate` must be a number in `[0, 100]`; anything else is rejected with 400.
* Browser access requires CORS. The allowed origin is `DASHBOARD_ORIGIN` (default `http://localhost:3000`), never `*` — a wildcard would let any page a user visits retune production traffic.
* Both fields are optional in the POST body; omitted fields are left unchanged.
* When `PROXY_API_TOKEN` is set, every `/api/*` request must carry
  `Authorization: Bearer <token>` or gets a 401. Preflight is answered before the check, since a
  browser cannot attach the header to an `OPTIONS`. Unset, the check is off — that is the local
  default and is not safe on a public port.

---

## DASHBOARD UI ARCHITECTURE

```text
dashboard/
├── src/
│   ├── app/
│   │   ├── layout.tsx         # Root layout with dark theme & navbar
│   │   ├── page.tsx           # Main control room dashboard
│   │   └── api/               # Next.js route handlers (optional BFF proxy to Go backend)
│   ├── components/
│   │   ├── MetricsGrid.tsx    # Throughput & latency stat cards
│   │   ├── ControlPanel.tsx   # Sliders & toggles for sample rates
│   │   └── HealthStatus.tsx   # Proxy & backend connection indicators
│   └── lib/
│       └── proxy-client.ts    # Fetch wrapper for Go backend APIs
├── package.json
└── tsconfig.json
```

---

## IMPLEMENTATION PHASES

### PHASE 0: NEXT.JS SETUP & PROXY API CLIENT
**Exit Criterion:** Next.js project is initialized with TypeScript and Tailwind, and `proxy-client.ts` successfully fetches data from the Go backend.

* Step 0A: Go side — `/api/metrics`, `GET|POST /api/config`, CORS, atomic live config.
* Step 0B: Next.js scaffold (TypeScript strict, Tailwind, App Router).
* Step 0C: `src/lib/proxy-client.ts` typed fetch wrapper.

### PHASE 1: REAL-TIME METRICS OVERVIEW CARDS
**Exit Criterion:** The UI polls `/api/metrics` every 2 seconds and displays live numbers for primary throughput, shadow dispatch counts, and dropped requests.

### PHASE 2: DYNAMIC SAMPLING RATE CONTROL SLIDER
**Exit Criterion:** A range slider lets the user adjust the sample rate in real time, posting changes back to the Go proxy via `/api/config`.

### PHASE 3: CONNECTION & HEALTH STATUS MONITOR
**Exit Criterion:** Gracefully handle proxy connection drops or API timeouts with clear visual indicators in the header.

---

## RUNNING BOTH HALVES

```bash
# Terminal 1 — the proxy
PRIMARY_URL=http://127.0.0.1:9000 SHADOW_URL=http://127.0.0.1:9001 go run .

# Terminal 2 — the dashboard
cd dashboard && npm run dev     # http://localhost:3000
```

`NEXT_PUBLIC_PROXY_URL` points the dashboard at the proxy (default `http://localhost:8080`).
