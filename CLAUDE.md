# CLAUDE.md — Shadow Traffic Replicator Dashboard (`/dashboard`)

## WORKFLOW: BRANCH + PR ONLY

No direct commits to `main`. Every change goes: `git checkout -b <branch>` → commit → `gh pr create`. A pre-commit hook (`.git/hooks/pre-commit`) enforces this locally by rejecting commits made while on `main`.

## CURRENT STATUS

╔══════════════════════════════════════════════════════════╗
║  DASHBOARD BUILD PROGRESS                       1/4 DONE ║
║  ███████░░░░░░░░░░░░░░░░░░░░░  IN DEVELOPMENT            ║
║  Phase 0: Next.js Setup & Proxy API Client      [DONE]   ║
║  Phase 1: Real-Time Metrics Overview Cards      [TODO]   ║
║  Phase 2: Dynamic Sampling Rate Control Slider  [TODO]   ║
║  Phase 3: Connection & Health Status Monitor    [TODO]   ║
╚══════════════════════════════════════════════════════════╝

Phase: 1 — Real-Time Metrics Overview Cards
Status: Phase 0 complete. Go control plane live; `proxy-client.ts` verified against the running proxy. UI is still the create-next-app default page.
Update this as you finish each step.

**Dashboard checks:** `cd dashboard && npm test && npm run typecheck && npm run lint`

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
       "avgShadowLatencyMs": 85.5
     }
     ```
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

### Trust boundary
`/api/config` mutates a proxy sitting in the live request path. Therefore:
* `sampleRate` must be a number in `[0, 100]`; anything else is rejected with 400.
* Browser access requires CORS. The allowed origin is `DASHBOARD_ORIGIN` (default `http://localhost:3000`), never `*` — a wildcard would let any page a user visits retune production traffic.
* Both fields are optional in the POST body; omitted fields are left unchanged.

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
