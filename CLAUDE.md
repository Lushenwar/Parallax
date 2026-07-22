# CLAUDE.md — Shadow Traffic Replicator Proxy

## WORKFLOW: BRANCH + PR ONLY

No direct commits to `main`. Every change goes: `git checkout -b <branch>` → commit → `gh pr create`. A pre-commit hook (`.git/hooks/pre-commit`) enforces this locally by rejecting commits made while on `main`.

## CURRENT STATUS

╔══════════════════════════════════════════════════════════╗
║  BUILD PROGRESS                                 3/5 DONE ║
║  ███████████████░░░░░░░░░  IN DEVELOPMENT                ║
║  Phase 0: Core Reverse Proxy & Request Parsing  [DONE]   ║
║  Phase 1: Async Cloning & Payload Buffering     [DONE]   ║
║  Phase 2: Target Dispatch & Connection Pooling  [DONE]   ║
║  Phase 3: Rate Limiting & Sampling Logic        [TODO]   ║
║  Phase 4: Metrics, Logging & Load Testing       [TODO]   ║
╚══════════════════════════════════════════════════════════╝

Phase: 3 — Rate Limiting & Sampling Logic
Status: Phase 2 complete. Traffic mirrors to the shadow backend fire-and-forget; every request is mirrored (no sampling yet).
Update this as you finish each step.

**Run it:** `PRIMARY_URL=http://127.0.0.1:9000 SHADOW_URL=http://127.0.0.1:9001 go run .`
Listens on `:8080` (`LISTEN_ADDR`). Omit `SHADOW_URL` to run as a plain reverse proxy.

## WHAT THIS FILE IS

This document is the authoritative guide for developing the Shadow Traffic Replicator. Every architectural decision, phase boundary, concurrency model, and engineering constraint defined here is binding. Do not deviate from it without explicit user approval.

---

## PRODUCT DEFINITION

The Shadow Traffic Replicator is a high-performance HTTP(S) proxy designed to safely mirror production traffic to a secondary (shadow) environment without impacting the primary request path. It sits in the critical path of live traffic; therefore, latency, memory safety, and fail-open characteristics are paramount.

### What the Replicator IS:
* A transparent reverse proxy that forwards traffic to a primary backend and returns the response synchronously.
* An asynchronous mirroring engine that clones the incoming request (headers, body, method) and fires it at a shadow backend in a completely non-blocking, fire-and-forget manner.
* A sampling mechanism to mirror a configurable percentage of traffic (e.g., 5%, 50%, 100%) or traffic matching specific headers/routes.
* Highly concurrent and strictly bounded in memory usage (to prevent OOM errors if the shadow backend hangs or slows down).

### What the Replicator IS NOT:
* Not a response comparator. It does not wait for the shadow response to compare it with the primary response.
* Not an API Gateway. It does not handle authentication, authorization, or complex routing (beyond routing to the primary/shadow backends).
* Not a durable message queue. If the shadow target goes down, shadow requests are dropped, not queued on disk.

### How It Works:
1. **Intercept:** The proxy receives an incoming HTTP request.
2. **Buffer (Carefully):** It streams or buffers the request body into memory (up to a strict size limit).
3. **Dispatch Primary:** The request is immediately forwarded to the primary backend.
4. **Clone & Dispatch Shadow:** *Concurrently*, if the request passes the sampling filter, a deep copy of the request is dispatched to the shadow backend on a separate goroutine/thread/coroutine (depending on the implementation language).
5. **Return:** The primary backend's response is streamed back to the client as soon as it arrives. The proxy *never* waits for the shadow backend.
6. **Discard:** The shadow backend's response is immediately discarded.

---

## SCOPE CONSTRAINTS

### Target Domain:
* HTTP/1.1 and HTTP/2 traffic (REST, GraphQL, standard web traffic).
* Stateless execution. The proxy itself holds no persistent state between requests (other than metrics and connection pools).

### Excluded from MVP:
* WebSockets and Server-Sent Events (SSE) mirroring (pass-through to primary only).
* Distributed tracing injection (OpenTelemetry).
* Response body comparison.

--- 


## SYSTEM ARCHITECTURE

                              ┌─────────────────────────┐
                              │                         │
  Client Request ────────────►│  Shadow Replicator      │
                              │                         │
                              └─┬─────────────────────┬─┘
                                │                     │
                         (Synchronous)         (Asynchronous /
                                │               Fire-and-forget)
                                ▼                     │
                    ┌───────────────────────┐         ▼
                    │                       │ ┌───────────────────────┐
                    │   Primary Backend     │ │                       │
                    │   (Production)        │ │   Shadow Backend      │
                    │                       │ │   (Staging/Test)      │
                    └───────────────────────┘ │                       │
                                │             └───────────────────────┘
                                │                     │
  Client Response ◄─────────────┘             (Response Discarded)

---

<GenerateWidget title="Shadow Traffic Architecture Flow" height="700px">
{
  "widgetSpec": {
    "id": "shadow-traffic-flow",
    "height": "700px",
    "prompt": "Objective: Visualize how an HTTP proxy handles real-time traffic splitting and asynchronous shadowing.\nData State: initialValues: { sampleRate: 50, latencyPrimary: 50, latencyShadow: 200 }.\nStrategy: Standard Layout.\nLibraries: D3.js or Anime.js for flow animation.\nInputs:\n- Shadow Sample Rate % (Slider: 0-100)\n- Shadow Backend Latency (Slider: Fast to Hanging)\nBehavior: Show an incoming stream of requests hitting a proxy node. Route all requests down a 'Primary' path. Route a percentage (based on the slider) down a 'Shadow' path. Animate the requests. Crucially, show that if the 'Shadow' latency slider is increased, the Shadow path backs up, but the Primary path continues flowing smoothly (demonstrating the non-blocking architecture). When the shadow path backs up too much, visually indicate 'dropped' requests to signify bounded queue limits."
  }
}
</GenerateWidget>

### 1. The Core Proxy Engine
* Must use an event-driven or highly concurrent networking library.
* **Timeout Hierarchy:**
  * `Primary Timeout`: Strict (e.g., 30s). The client gets a 504 if the primary fails.
  * `Shadow Timeout`: Aggressive (e.g., 5s). Drop the shadow request fast if the test environment is slow.

### 2. Request Cloning (The Hard Part)
* **Body Buffering:** HTTP request bodies are streams. To send them twice, they must be read into memory.
* **Constraint:** Enforce a strict `MAX_BODY_SIZE` (e.g., 10MB). Requests larger than this are streamed to the primary, but *not* mirrored to the shadow (to protect proxy RAM).
* **Header Manipulation:** The shadow request must rewrite the `Host` header. It should ideally inject a `X-Shadow-Traffic: true` header to prevent infinite loops if misconfigured.

### 3. Concurrency and Bounded Queues
* The shadow dispatch mechanism must use a bounded queue or worker pool.
* **Backpressure Rule:** If the shadow backend cannot keep up and the queue fills, *drop the shadow request immediately*. Never block the primary request thread waiting for shadow capacity.

---

## DANGER ZONES — TRAPS TO AVOID

1. **The "Wait for Shadow" Trap:** If your code accidentally `await`s the shadow request before returning the primary response, you have broken production. Shadow dispatch must be strictly backgrounded.
2. **The OOM Trap:** Buffering every 500MB video upload in memory to shadow it will crash the proxy. Implement and test strict body size limits.
3. **The Connection Pool Exhaustion Trap:** The shadow backend will likely be slower than production. If you don't limit shadow connections, you will exhaust ephemeral ports or file descriptors on the proxy, taking down the primary traffic flow. Separate the connection pools for Primary and Shadow.
4. **The Infinite Loop Trap:** If the shadow backend URL is accidentally set to the proxy itself, it will amplify traffic infinitely. Inject a specific header (e.g., `X-Shadow-Request: true`) and drop incoming requests that already have it.
5. **The Stream Consumption Trap:** If you read the request stream to send to the shadow, you must recreate the stream or buffer to send to the primary. You cannot read a TCP socket twice.

---

## IMPLEMENTATION PHASES

### PHASE 0: CORE REVERSE PROXY & REQUEST PARSING
**Exit Criterion:** The proxy listens on a port, receives HTTP GET and POST requests, forwards them to a configured primary URL, and streams the response back. No shadowing yet.

* Step 0A: Basic HTTP server setup.
* Step 0B: HTTP client for the primary backend.
* Step 0C: Streaming proxy logic (headers, body, status codes passed through).

### PHASE 1: ASYNC CLONING & PAYLOAD BUFFERING
**Exit Criterion:** The proxy successfully reads request bodies into a bounded buffer. If the body is under the limit, it creates an exact clone of the request object in memory. If over the limit, it streams to primary without cloning.

* Step 1A: Implement `MAX_BODY_SIZE` constraint and buffered reader.
* Step 1B: Header cloning logic (stripping hop-by-hop headers, rewriting `Host`).
* Step 1C: Unit tests verifying the buffer handles chunks correctly and aborts cloning on size limits.

### PHASE 2: TARGET DISPATCH & CONNECTION POOLING
**Exit Criterion:** Cloned requests are dispatched to the shadow backend URL in the background. The primary response is returned to the client immediately, regardless of the shadow backend's status (even if shadow is offline).

* Step 2A: Set up the secondary HTTP client with aggressive timeouts and a strict connection pool limit.
* Step 2B: Implement the fire-and-forget worker mechanism for shadow dispatch.
* Step 2C: Add the `X-Shadow-Traffic` header to cloned requests.

### PHASE 3: RATE LIMITING & SAMPLING LOGIC
**Exit Criterion:** The proxy accepts configuration for a sampling rate (0-100%). Traffic is statistically distributed. If a worker queue is full, shadow requests are dropped silently.

* Step 3A: Implement random sampling logic.
* Step 3B: Implement bounded queue for the background workers. Drop logic on queue full.

### PHASE 4: METRICS, LOGGING & LOAD TESTING
**Exit Criterion:** The proxy exposes a `/metrics` endpoint (or logs) showing primary vs. shadow throughput, dropped shadow requests, and error rates. The proxy survives a stress test where the shadow backend is artificially paused.

* Step 4A: Instrument primary latency, shadow drops, and active connection counts.
* Step 4B: Write a load test script using tools like `hey` or `k6` to verify primary latency

---

## GO IMPLEMENTATION BLUEPRINTS

### 1. Request Buffering & Size Enforcement (`proxy/buffer.go`)
To prevent OOM errors, HTTP request bodies must be capped before cloning:

```go
package proxy

import (
  "bytes"
  "errors"
  "io"
  "net/http"
)

var ErrPayloadTooLarge = errors.New("request payload exceeds maximum shadow size limit")

const MaxBodySize = 10 * 1024 * 1024 // 10MB limit for shadow cloning

func BufferAndCloneBody(r *http.Request) ([]byte, error) {
  if r.Body == nil {
    return nil, nil
  }
  defer r.Body.Close()

  // Read up to limit + 1 to detect if body is too large
  limitedReader := io.LimitReader(r.Body, MaxBodySize+1)
  bodyBytes, err := io.ReadAll(limitedReader)
  if err != nil {
    return nil, err
  }
  if int64(len(bodyBytes)) > MaxBodySize {
    return nil, ErrPayloadTooLarge
  }
  return bodyBytes, nil
}

// RestoreBody resets r.Body so the primary backend can read it
func RestoreBody(r *http.Request, bodyBytes []byte) {
  if bodyBytes != nil {
    r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
  }
}
```

### 2. The Asynchronous Shadow Dispatcher (`proxy/shadow.go`)
Shadow requests must run on separate goroutines via a non-blocking execution path. They must **never** block the primary HTTP response cycle.

```go
package proxy

import (
  "bytes"
  "context"
  "io"
  "net/http"
  "time"
)

func DispatchShadowAsync(client *http.Client, req *http.Request, bodyBytes []byte, sampleRate float64) {
  // Sampling check placeholder
  if sampleRate <= 0 {
    return
  }

  // Clone request safely for background execution
  shadowReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), io.NopCloser(bytes.NewReader(bodyBytes)))
  if err != nil {
    return
  }

  // Copy headers
  for k, vv := range req.Header {
    for _, v := range vv {
      shadowReq.Header.Add(k, v)
    }
  }
  shadowReq.Header.Set("X-Shadow-Traffic", "true")

  // Fire-and-forget goroutine with strict context timeout
  go func() {
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()

    shadowReq = shadowReq.WithContext(ctx)
    resp, err := client.Do(shadowReq)
    if err != nil {
      return // Fail silently, do not affect primary flow
    }
    defer resp.Body.Close()
    io.Copy(io.Discard, resp.Body) // Drain connection to return to pool
  }()
}
```

### 3. Isolated Connection Pools (`proxy/transport.go`)
Primary and shadow traffic must use completely separate `http.Transport` instances to prevent a slow shadow backend from starving primary client connections.

```go
package proxy

import (
  "net/http"
  "time"
)

var PrimaryTransport = &http.Transport{
  MaxIdleConns:        1000,
  MaxIdleConnsPerHost: 100,
  IdleConnTimeout:     90 * time.Second,
}

var ShadowTransport = &http.Transport{
  MaxIdleConns:        200,
  MaxIdleConnsPerHost: 20,
  IdleConnTimeout:     10 * time.Second, // Drop idle connections faster
}
```