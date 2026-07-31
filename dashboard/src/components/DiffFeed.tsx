import { CheckCircle2, GitCompareArrows, TriangleAlert } from 'lucide-react';
import type { Diff, DiffReport } from '@/lib/proxy-client';

// Fixed locale and UTC, for the same reason MetricsGrid pins its formatters:
// the page server-renders once, and a timezone-dependent string would mismatch
// on hydration.
const time = new Intl.DateTimeFormat('en-GB', {
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  timeZone: 'UTC',
});
const count = new Intl.NumberFormat('en-US');

function formatTime(iso: string): string {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? '—' : `${time.format(at)} UTC`;
}

function statusTone(status: number): string {
  if (status >= 500) return 'text-warn';
  if (status >= 400) return 'text-foreground';
  return 'text-muted';
}

function DiffRow({ diff }: { diff: Diff }) {
  const statusDiffers = diff.primaryStatus !== diff.shadowStatus;

  return (
    <li className="space-y-2 rounded-lg border border-border bg-surface p-4">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="rounded bg-border px-1.5 py-0.5 text-xs font-semibold tracking-wide">
          {diff.method}
        </span>
        <span className="tabular font-mono text-sm break-all">{diff.path}</span>
        <span className="ml-auto text-xs text-muted tabular">{formatTime(diff.at)}</span>
      </div>

      <p className="text-xs text-muted">
        primary{' '}
        <span className={`tabular font-semibold ${statusTone(diff.primaryStatus)}`}>
          {diff.primaryStatus}
        </span>
        {' → '}shadow{' '}
        <span
          className={`tabular font-semibold ${statusDiffers ? 'text-warn' : statusTone(diff.shadowStatus)}`}
        >
          {diff.shadowStatus}
        </span>
      </p>

      <ul className="space-y-1">
        {diff.reasons.map((reason, i) => (
          <li key={i} className="font-mono text-xs break-all text-foreground">
            <span className="text-warn" aria-hidden>
              ·{' '}
            </span>
            {reason}
          </li>
        ))}
      </ul>
    </li>
  );
}

export function DiffFeed({ report, stale }: { report: DiffReport | null; stale: boolean }) {
  const total = (report?.matches ?? 0) + (report?.mismatches ?? 0);
  const rate = total > 0 ? (((report?.mismatches ?? 0) / total) * 100).toFixed(1) : '0.0';

  return (
    <section
      aria-label="Response comparison feed"
      className={`space-y-4 rounded-xl border border-border bg-surface p-5 transition-opacity ${
        stale ? 'opacity-50' : ''
      }`}
    >
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="space-y-1">
          <h2 className="flex items-center gap-2 text-lg font-semibold">
            <GitCompareArrows className="size-4 text-muted" aria-hidden />
            Response Diffs
          </h2>
          <p className="text-sm text-muted">
            Where the shadow backend disagreed with what the client was served.
          </p>
        </div>
        {report?.enabled && (
          <p className="text-sm text-muted tabular">
            <span className="font-semibold text-foreground">{count.format(report.mismatches)}</span>{' '}
            mismatched of {count.format(total)} compared ({rate}%)
          </p>
        )}
      </div>

      {!report ? (
        <p className="py-8 text-center text-sm text-muted">Loading comparisons…</p>
      ) : !report.enabled ? (
        <p className="rounded-lg border border-border p-4 text-sm text-muted">
          Comparison is off — the proxy is mirroring traffic but discarding shadow responses. Set{' '}
          <code className="font-mono text-xs">DIFF_BUFFER</code> above 0 and restart to turn it on.
        </p>
      ) : report.diffs.length === 0 ? (
        <p className="flex items-center justify-center gap-2 py-8 text-center text-sm text-muted">
          <CheckCircle2 className="size-4" aria-hidden />
          {total > 0
            ? `No disagreements across ${count.format(total)} compared responses.`
            : 'No responses compared yet — send some mirrored traffic.'}
        </p>
      ) : (
        <>
          <p className="flex items-center gap-2 text-xs text-warn">
            <TriangleAlert className="size-3.5" aria-hidden />
            Showing the {report.diffs.length} most recent, newest first.
          </p>
          <ul className="space-y-3">
            {report.diffs.map((diff, i) => (
              <DiffRow key={`${diff.at}-${i}`} diff={diff} />
            ))}
          </ul>
        </>
      )}
    </section>
  );
}
