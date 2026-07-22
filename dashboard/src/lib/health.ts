/**
 * Connection health derived from poll state.
 *
 * Kept free of React and JSX so it is a pure function the test runner can
 * import directly — the branching here is what an operator trusts during an
 * incident, so it gets a real test rather than a visual check.
 */

export type HealthLevel = 'connecting' | 'connected' | 'stale' | 'down';

export interface Health {
  level: HealthLevel;
  label: string;
  detail: string;
}

export interface HealthInput {
  loading: boolean;
  error: Error | null;
  hasData: boolean;
  lastUpdatedAt: number | null;
  now: number;
}

export function deriveHealth({ loading, error, hasData, lastUpdatedAt, now }: HealthInput): Health {
  if (loading && !hasData) {
    return { level: 'connecting', label: 'Connecting', detail: 'Reaching the proxy…' };
  }
  if (!error) {
    return { level: 'connected', label: 'Proxy Connected', detail: freshness(lastUpdatedAt, now) };
  }
  // Never seen a good response: there is nothing to show but the failure.
  if (!hasData) {
    return { level: 'down', label: 'Proxy Unreachable', detail: error.message };
  }
  // Last-known numbers are still on screen; say plainly that they are frozen.
  return {
    level: 'stale',
    label: 'Reconnecting',
    detail: `${error.message} — showing data from ${freshness(lastUpdatedAt, now)}`,
  };
}

export function freshness(lastUpdatedAt: number | null, now: number): string {
  if (lastUpdatedAt === null) return 'never';

  const seconds = Math.max(0, Math.round((now - lastUpdatedAt) / 1000));
  if (seconds < 2) return 'just now';
  if (seconds < 60) return `${seconds}s ago`;

  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  return `${Math.floor(minutes / 60)}h ago`;
}
