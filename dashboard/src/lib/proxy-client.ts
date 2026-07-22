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

const TIMEOUT_MS = 4000;

export interface ProxyMetrics {
  primaryRequestsTotal: number;
  shadowRequestsDispatched: number;
  shadowRequestsDropped: number;
  activeConnections: number;
  avgPrimaryLatencyMs: number;
  avgShadowLatencyMs: number;
}

export interface ProxyConfig {
  sampleRate: number;
  maxBodySizeMB: number;
  shadowEnabled: boolean;
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

  let res: Response;
  try {
    res = await fetch(url, { ...init, cache: 'no-store', signal: AbortSignal.timeout(TIMEOUT_MS) });
  } catch (cause) {
    const timedOut = cause instanceof DOMException && cause.name === 'TimeoutError';
    throw new ProxyError(
      timedOut ? `Proxy did not respond within ${TIMEOUT_MS}ms` : `Cannot reach the proxy at ${proxyUrl()}`,
      { unreachable: true, cause },
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
