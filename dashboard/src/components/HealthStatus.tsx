'use client';

import { useEffect, useState } from 'react';
import { CircleAlert, CircleCheck, CircleSlash, LoaderCircle } from 'lucide-react';
import { deriveHealth, type HealthInput, type HealthLevel } from '@/lib/health';

const TONE: Record<HealthLevel, { chip: string; icon: React.ReactNode }> = {
  connecting: {
    chip: 'bg-muted/10 text-muted border-muted/20',
    icon: <LoaderCircle className="size-3.5 animate-spin" aria-hidden />,
  },
  connected: {
    chip: 'bg-accent/10 text-accent border-accent/20',
    icon: <CircleCheck className="size-3.5" aria-hidden />,
  },
  stale: {
    chip: 'bg-warn/10 text-warn border-warn/20',
    icon: <CircleAlert className="size-3.5" aria-hidden />,
  },
  down: {
    chip: 'bg-danger/10 text-danger border-danger/20',
    icon: <CircleSlash className="size-3.5" aria-hidden />,
  },
};

export function HealthStatus(props: Omit<HealthInput, 'now'>) {
  // `now` has to advance on its own: when the proxy is down there are no
  // successful polls to re-render on, and a frozen "12s ago" during an outage
  // is exactly the wrong thing to show.
  const [now, setNow] = useState<number | null>(null);
  useEffect(() => {
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  // Until mounted, `now` is null and the server and client agree on the same
  // markup — a Date.now() during SSR would mismatch on hydration.
  const health = deriveHealth({ ...props, now: now ?? props.lastUpdatedAt ?? 0 });
  const tone = TONE[health.level];

  return (
    <div
      role="status"
      aria-live="polite"
      className={`inline-flex items-center gap-2 rounded-full border px-3 py-1 text-xs font-medium ${tone.chip}`}
      title={health.detail}
    >
      {tone.icon}
      <span>{health.label}</span>
      <span className="tabular font-normal opacity-70">{health.detail}</span>
    </div>
  );
}

/** Full-width banner for the case where the operator must not mistake stale
 *  numbers for live ones. */
export function HealthBanner(props: Omit<HealthInput, 'now'>) {
  const health = deriveHealth({ ...props, now: Date.now() });
  if (health.level === 'connected' || health.level === 'connecting') return null;

  const danger = health.level === 'down';
  return (
    <div
      role="alert"
      className={`flex items-start gap-3 rounded-xl border p-4 text-sm ${
        danger ? 'border-danger/30 bg-danger/10 text-danger' : 'border-warn/30 bg-warn/10 text-warn'
      }`}
    >
      {danger ? (
        <CircleSlash className="mt-0.5 size-4 shrink-0" aria-hidden />
      ) : (
        <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
      )}
      <div>
        <p className="font-semibold">{health.label}</p>
        <p className="opacity-90">
          {danger
            ? health.detail
            : 'The counters below are the last values the proxy reported, not live traffic.'}
        </p>
      </div>
    </div>
  );
}
