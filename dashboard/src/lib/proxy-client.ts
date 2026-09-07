/**
 * Typed fetch wrapper for the Go proxy's control-plane API.
 *
 * Every call is bounded by a timeout: the dashboard polls a proxy that sits in
 * a live request path, and a hung fetch would silently freeze the UI on stale
 * numbers rather than telling the operator the link is down.
 */

// Read lazily so tests can point it elsewhere. Next still inlines the literal
// `process.env.NEXT_PUBLIC_PROXY_URL` at build time for the browser bundle.
const proxyUrl = () => process.env.NEXT_PUBLIC_PROXY_URL || 'http://localhost:8080';

// Sent as a bearer token when the proxy is started with PROXY_API_TOKEN.
//
// NEXT_PUBLIC_ means the browser bundle contains it, so this authenticates the
// dashboard to the proxy, not the operator to the dashboard: it stops anything
// else that can reach the port from retuning live traffic, and stops nothing a
// person with the dashboard open cannot already do. Put real user auth in front
// of the dashboard if that distinction matters.
const proxyToken = () => process.env.NEXT_PUBLIC_PROXY_TOKEN || '';

const TIMEOUT_MS = 4000;

/** Windowed latency percentiles in milliseconds, over the proxy's most recent
 *  samples. `samples` is how many observations are behind them — a p99 over 6
 *  requests is not a p99, and the UI says so rather than showing a number. */
export interface Latency {
  p50: number;
  p95: number;
  p99: number;
  max: number;
  samples: number;
}

export interface ProxyMetrics {
  primaryRequestsTotal: number;
  shadowRequestsDispatched: number;
  shadowRequestsDropped: number;
  activeConnections: number;
  avgPrimaryLatencyMs: number;
  avgShadowLatencyMs: number;
  primaryLatency: Latency;
  shadowLatency: Latency;
  /** Time spent inside the proxy rather than waiting on the primary backend —
   *  the cost of having Parallax in the request path, measured per request. */
  proxyOverhead: Latency;
}

export interface ProxyConfig {
  sampleRate: number;
  maxBodySizeMB: number;
  shadowEnabled: boolean;
}

/** One recorded disagreement between the primary and shadow backends. */
export interface Diff {
  at: string;
  method: string;
  path: string;
  primaryStatus: number;
  shadowStatus: number;
  reasons: string[];
}

/** The comparison view. `enabled: false` means the proxy is mirroring without
 *  comparing — worth distinguishing from "comparing and finding nothing",
 *  because the two look identical from a count of zero. */
export interface DiffReport {
  enabled: boolean;
  matches: number;
  mismatches: number;
  diffs: Diff[];
}

/** Anything that went wrong talking to the proxy. `unreachable` separates
 *  "the proxy is down" from "the proxy said no", which the health monitor
 *  renders differently. */
export class ProxyError extends Error {
  readonly status: number | null;
  readonly unreachable: boolean;

  constructor(message: string, options: { status?: number; unreachable?: boolean; cause?: unknown } = {}) {
    super(message, { cause: options.cause });
    this.name = 'ProxyError';
    this.status = options.status ?? null;
    this.unreachable = options.unreachable ?? false;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const url = `${proxyUrl()}${path}`;

  const token = proxyToken();
  const headers = new Headers(init?.headers);
  if (token) {
    headers.set('Authorization', `Bearer ${token}`);
  }

  let res: Response;
  try {
    res = await fetch(url, {
      ...init,
      headers,
      cache: 'no-store',
      signal: AbortSignal.timeout(TIMEOUT_MS),
    });
  } catch (cause) {
    const timedOut = cause instanceof DOMException && cause.name === 'TimeoutError';
    throw new ProxyError(
      timedOut ? `Proxy did not respond within ${TIMEOUT_MS}ms` : `Cannot reach the proxy at ${proxyUrl()}`,
      { unreachable: true, cause },
    );
  }

  if (res.status === 401) {
    throw new ProxyError(
      "Proxy rejected the control-plane token. Set NEXT_PUBLIC_PROXY_TOKEN to match the proxy's PROXY_API_TOKEN.",
      { status: 401 },
    );
  }
  if (!res.ok) {
    throw new ProxyError(await errorMessage(res), { status: res.status });
  }
  return (await res.json()) as T;
}

/** The Go API returns `{"error": "..."}`; fall back to the status line if it
 *  ever returns something else (a gateway in between, say). */
async function errorMessage(res: Response): Promise<string> {
  try {
    const body: unknown = await res.json();
    if (body && typeof body === 'object' && 'error' in body && typeof body.error === 'string') {
      return body.error;
    }
  } catch {
    // fall through
  }
  return `Proxy returned ${res.status} ${res.statusText}`.trim();
}

export function fetchMetrics(): Promise<ProxyMetrics> {
  return request<ProxyMetrics>('/api/metrics');
}

export function fetchDiffs(): Promise<DiffReport> {
  return request<DiffReport>('/api/diffs');
}

export function fetchConfig(): Promise<ProxyConfig> {
  return request<ProxyConfig>('/api/config');
}

export function updateConfig(config: Partial<ProxyConfig>): Promise<ProxyConfig> {
  return request<ProxyConfig>('/api/config', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(config),
  });
}
