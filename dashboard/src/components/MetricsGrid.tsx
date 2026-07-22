import {
  ActivitySquare,
  ArrowDownUp,
  Copy,
  Gauge,
  Timer,
  TrashIcon,
} from 'lucide-react';
import type { ProxyMetrics } from '@/lib/proxy-client';

// Fixed locale: the page is client-rendered but still server-rendered once, and
// a locale-dependent separator would mismatch and trigger a hydration error.
const count = new Intl.NumberFormat('en-US');
const ms = new Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });

interface StatProps {
  label: string;
  value: string;
  hint: string;
  icon: React.ReactNode;
  tone?: 'default' | 'warn';
  stale?: boolean;
}

function Stat({ label, value, hint, icon, tone = 'default', stale = false }: StatProps) {
  return (
    <div
      className={`space-y-3 rounded-xl border border-border bg-surface p-5 transition-opacity ${
        stale ? 'opacity-50' : ''
      }`}
    >
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted">{label}</p>
        <span className="text-muted" aria-hidden>
          {icon}
        </span>
      </div>
      <p
        className={`tabular text-3xl font-semibold ${tone === 'warn' ? 'text-warn' : 'text-foreground'}`}
      >
        {value}
      </p>
      <p className="text-xs text-muted">{hint}</p>
    </div>
  );
}

export function MetricsGrid({ metrics, stale }: { metrics: ProxyMetrics | null; stale: boolean }) {
  // Placeholder zeros only appear before the very first poll lands.
  const m: ProxyMetrics = metrics ?? {
    primaryRequestsTotal: 0,
    shadowRequestsDispatched: 0,
    shadowRequestsDropped: 0,
    activeConnections: 0,
    avgPrimaryLatencyMs: 0,
    avgShadowLatencyMs: 0,
  };

  const mirroredPct =
    m.primaryRequestsTotal > 0
      ? ((m.shadowRequestsDispatched / m.primaryRequestsTotal) * 100).toFixed(1)
      : '0.0';

  return (
    <section aria-label="Live proxy metrics" className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
      <Stat
        label="Primary Throughput"
        value={count.format(m.primaryRequestsTotal)}
        hint="Requests served on the client-facing path"
        icon={<ArrowDownUp className="size-4" />}
        stale={stale}
      />
      <Stat
        label="Shadow Dispatched"
        value={count.format(m.shadowRequestsDispatched)}
        hint={`${mirroredPct}% of primary traffic mirrored`}
        icon={<Copy className="size-4" />}
        stale={stale}
      />
      <Stat
        label="Dropped Requests"
        value={count.format(m.shadowRequestsDropped)}
        hint="Mirrors shed when the bounded queue was full"
        icon={<TrashIcon className="size-4" />}
        tone={m.shadowRequestsDropped > 0 ? 'warn' : 'default'}
        stale={stale}
      />
      <Stat
        label="Active Connections"
        value={count.format(m.activeConnections)}
        hint="Requests in flight on the primary path right now"
        icon={<ActivitySquare className="size-4" />}
        stale={stale}
      />
      <Stat
        label="Avg Primary Latency"
        value={`${ms.format(m.avgPrimaryLatencyMs)} ms`}
        hint="What the client actually waits for"
        icon={<Timer className="size-4" />}
        stale={stale}
      />
      <Stat
        label="Avg Shadow Latency"
        value={`${ms.format(m.avgShadowLatencyMs)} ms`}
        hint="Never on the client's critical path"
        icon={<Gauge className="size-4" />}
        stale={stale}
      />
    </section>
  );
}
