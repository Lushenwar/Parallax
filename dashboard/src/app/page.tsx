'use client';

import { MetricsGrid } from '@/components/MetricsGrid';
import { fetchMetrics } from '@/lib/proxy-client';
import { usePoll } from '@/lib/use-poll';

const POLL_MS = 2000;

export default function DashboardPage() {
  const metrics = usePoll(fetchMetrics, POLL_MS);

  return (
    <main className="mx-auto max-w-7xl space-y-8 p-6">
      <header className="flex flex-wrap items-end justify-between gap-4 border-b border-border pb-4">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Traffic Overview</h1>
          <p className="text-sm text-muted">
            Live counters from the proxy, refreshed every {POLL_MS / 1000}s.
          </p>
        </div>
      </header>

      {metrics.loading ? (
        <p className="py-16 text-center text-muted">Connecting to Shadow Proxy Engine…</p>
      ) : (
        <MetricsGrid metrics={metrics.data} stale={metrics.error !== null} />
      )}
    </main>
  );
}
