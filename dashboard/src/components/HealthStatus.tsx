'use client';

import { useSyncExternalStore } from 'react';
import { CircleAlert, CircleCheck, CircleSlash, LoaderCircle } from 'lucide-react';
import { deriveHealth, type HealthInput, type HealthLevel } from '@/lib/health';

/**
 * A once-per-second clock as an external store.
 *
 * The age reading has to advance on its own: when the proxy is down there are
 * no successful polls to re-render on, and a frozen age during an outage is
 * exactly the wrong thing to show. useSyncExternalStore rather than an effect
 * so the snapshot is cached (no cascading render) and SSR gets its own
 * snapshot instead of a hydration mismatch.
 */
let clock = 0;

function subscribeToClock(onChange: () => void): () => void {
  clock = Date.now();
  const id = setInterval(() => {
    clock = Date.now();
    onChange();
  }, 1000);
  return () => clearInterval(id);
}

function useNow(): number {
  return useSyncExternalStore(
    subscribeToClock,
    () => clock,
    () => 0, // server render: no wall clock, so no mismatch to hydrate against
  );
}

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
  const now = useNow();
  const health = deriveHealth({ ...props, now: now || (props.lastUpdatedAt ?? 0) });
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
  // No live clock here: the banner's level and copy do not depend on elapsed
  // time, only the chip's freshness reading does. Keeps render pure.
  const health = deriveHealth({ ...props, now: props.lastUpdatedAt ?? 0 });
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
