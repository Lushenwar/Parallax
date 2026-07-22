'use client';

import { useCallback } from 'react';
import { ControlPanel } from '@/components/ControlPanel';
import { HealthBanner, HealthStatus } from '@/components/HealthStatus';
import { MetricsGrid } from '@/components/MetricsGrid';
import { fetchConfig, fetchMetrics, updateConfig, type ProxyConfig } from '@/lib/proxy-client';
import { usePoll } from '@/lib/use-poll';

const POLL_MS = 2000;

export default function DashboardPage() {
  const metrics = usePoll(fetchMetrics, POLL_MS);
  const config = usePoll(fetchConfig, POLL_MS);

  const setConfig = config.set;
  const handleConfigChange = useCallback(
    async (patch: Partial<ProxyConfig>) => {
      // The POST response is the proxy's authoritative new state, so adopt it
      // directly instead of waiting for the next poll to catch up.
      const updated = await updateConfig(patch);
      setConfig(updated);
      return updated;
    },
    [setConfig],
  );

  // Metrics is the health signal: it polls on the same interval and is the
  // read the operator is actually watching.
  const health = {
    loading: metrics.loading,
    error: metrics.error,
    hasData: metrics.data !== null,
    lastUpdatedAt: metrics.lastUpdatedAt,
  };

  return (
    <main className="mx-auto max-w-7xl space-y-8 p-6">
      <header className="flex flex-wrap items-end justify-between gap-4 border-b border-border pb-4">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Traffic Overview</h1>
          <p className="text-sm text-muted">
            Live counters from the proxy, refreshed every {POLL_MS / 1000}s.
          </p>
        </div>
        <HealthStatus {...health} />
      </header>

      <HealthBanner {...health} />

      {metrics.loading && metrics.data === null ? (
        <p className="py-16 text-center text-muted">Connecting to Shadow Proxy Engine…</p>
      ) : (
        <MetricsGrid metrics={metrics.data} stale={metrics.error !== null} />
      )}

      <ControlPanel
        config={config.data}
        onChange={handleConfigChange}
        disabled={config.error !== null}
      />
    </main>
  );
}
