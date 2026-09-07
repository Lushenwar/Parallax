import { Timer } from 'lucide-react';
import type { Latency, ProxyMetrics } from '@/lib/proxy-client';

const ms = new Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const count = new Intl.NumberFormat('en-US');

// Below this many samples a p99 is one request wearing a percentile's name, so
// the row says how thin the data is instead of printing a confident number.
const MIN_SAMPLES = 20;

const EMPTY: Latency = { p50: 0, p95: 0, p99: 0, max: 0, samples: 0 };

interface RowProps {
  label: string;
  hint: string;
  latency: Latency;
}

function Row({ label, hint, latency }: RowProps) {
  const thin = latency.samples < MIN_SAMPLES;

  return (
    <tr className="border-t border-border align-top">
      <th scope="row" className="py-3 pr-4 text-left font-medium">
        {label}
        <span className="block text-xs font-normal text-muted">{hint}</span>
      </th>
      {thin ? (
        <td colSpan={4} className="py-3 text-sm text-muted">
          {latency.samples === 0
            ? 'No samples yet'
            : `Only ${latency.samples} samples — not enough for a percentile`}
        </td>
      ) : (
        <>
          <td className="tabular py-3 text-right">{ms.format(latency.p50)}</td>
          <td className="tabular py-3 text-right">{ms.format(latency.p95)}</td>
          <td className="tabular py-3 text-right font-semibold">{ms.format(latency.p99)}</td>
          <td className="tabular py-3 text-right text-muted">{ms.format(latency.max)}</td>
        </>
      )}
      <td className="tabular py-3 pl-4 text-right text-xs text-muted">
        {count.format(latency.samples)}
      </td>
    </tr>
  );
}

/**
 * The tail, which is the only latency view that decides anything.
 *
 * The averages on the cards above are lifetime means: they are the same number
 * whether every request took 12ms or one in a thousand took four seconds. A
 * proxy in the request path is judged on the second case.
 */
export function LatencyPanel({ metrics, stale }: { metrics: ProxyMetrics | null; stale: boolean }) {
  const primary = metrics?.primaryLatency ?? EMPTY;
  const shadow = metrics?.shadowLatency ?? EMPTY;
  const overhead = metrics?.proxyOverhead ?? EMPTY;

  return (
    <section
      aria-label="Latency percentiles"
      className={`rounded-xl border border-border bg-surface p-5 transition-opacity ${
        stale ? 'opacity-50' : ''
      }`}
    >
      <div className="flex items-center justify-between">
        <div>
          <h2 className="font-semibold">Latency Percentiles</h2>
          <p className="text-sm text-muted">
            Windowed over the most recent requests, not since startup.
          </p>
        </div>
        <span className="text-muted" aria-hidden>
          <Timer className="size-4" />
        </span>
      </div>

      <div className="mt-4 overflow-x-auto">
        <table className="w-full min-w-[34rem] text-sm">
          <caption className="sr-only">
            Latency percentiles in milliseconds for the primary path, the shadow path, and proxy
            overhead
          </caption>
          <thead>
            <tr className="text-xs uppercase tracking-wide text-muted">
              <th scope="col" className="pb-2 text-left font-medium">
                Path
              </th>
              <th scope="col" className="pb-2 text-right font-medium">
                p50
              </th>
              <th scope="col" className="pb-2 text-right font-medium">
                p95
              </th>
              <th scope="col" className="pb-2 text-right font-medium">
                p99
              </th>
              <th scope="col" className="pb-2 text-right font-medium">
                max
              </th>
              <th scope="col" className="pb-2 pl-4 text-right font-medium">
                samples
              </th>
            </tr>
          </thead>
          <tbody>
            <Row label="Primary" hint="What the client waits for, end to end" latency={primary} />
            <Row label="Shadow" hint="Off the critical path; slowness here costs nothing" latency={shadow} />
            <Row
              label="Proxy overhead"
              hint="Total minus the backend's own time — the cost of being in the path"
              latency={overhead}
            />
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-xs text-muted">All figures in milliseconds.</p>
    </section>
  );
}
