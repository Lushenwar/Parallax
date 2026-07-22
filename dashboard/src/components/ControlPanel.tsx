'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { AlertTriangle, Loader2, SlidersHorizontal } from 'lucide-react';
import type { ProxyConfig } from '@/lib/proxy-client';

/** A range input fires onChange on every pixel of a drag. Posting each one
 *  would hammer a proxy that is serving live traffic, so the drag updates
 *  locally and only the settled value is written. */
const COMMIT_DEBOUNCE_MS = 250;

interface ControlPanelProps {
  config: ProxyConfig | null;
  onChange: (patch: Partial<ProxyConfig>) => Promise<ProxyConfig>;
  disabled?: boolean;
}

export function ControlPanel({ config, onChange, disabled = false }: ControlPanelProps) {
  // While the operator is dragging, `draft` wins over the polled value so an
  // in-flight poll cannot yank the slider out from under them.
  const [draft, setDraft] = useState<number | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  const commit = useCallback(
    async (patch: Partial<ProxyConfig>) => {
      setPending(true);
      try {
        await onChange(patch);
        setError(null);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        // Either way, fall back to whatever the proxy actually reports. A
        // rejected write must not leave the UI showing a value the proxy
        // never accepted.
        setDraft(null);
        setPending(false);
      }
    },
    [onChange],
  );

  const handleSlide = (value: number) => {
    setDraft(value);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => void commit({ sampleRate: value }), COMMIT_DEBOUNCE_MS);
  };

  const rate = draft ?? config?.sampleRate ?? 0;
  const enabled = config?.shadowEnabled ?? false;
  const locked = disabled || config === null;

  return (
    <section className="space-y-6 rounded-xl border border-border bg-surface p-6">
      <div className="flex items-center gap-2">
        <SlidersHorizontal className="size-4 text-muted" aria-hidden />
        <h2 className="text-lg font-semibold">Live Traffic Settings</h2>
        {pending && <Loader2 className="size-4 animate-spin text-muted" aria-label="Saving" />}
      </div>

      <div className="space-y-3">
        <label htmlFor="sample-rate" className="flex justify-between text-sm font-medium">
          <span>Shadow Sample Rate</span>
          <span className="tabular font-mono text-accent">{rate.toFixed(1)}%</span>
        </label>
        <input
          id="sample-rate"
          type="range"
          min={0}
          max={100}
          step={0.5}
          value={rate}
          disabled={locked}
          onChange={(e) => handleSlide(parseFloat(e.target.value))}
          className="w-full cursor-pointer accent-accent disabled:cursor-not-allowed disabled:opacity-40"
          aria-describedby="sample-rate-hint"
        />
        <p id="sample-rate-hint" className="text-xs text-muted">
          Portion of live traffic mirrored to the shadow backend. The primary path is unaffected at
          any setting.
        </p>
      </div>

      <div className="flex items-start justify-between gap-4 border-t border-border pt-5">
        <div>
          <label htmlFor="shadow-enabled" className="text-sm font-medium">
            Shadow Mirroring
          </label>
          <p className="text-xs text-muted">
            Off is the kill switch: the primary path keeps serving, nothing is mirrored.
          </p>
        </div>
        <input
          id="shadow-enabled"
          type="checkbox"
          role="switch"
          checked={enabled}
          disabled={locked}
          onChange={(e) => void commit({ shadowEnabled: e.target.checked })}
          className="mt-1 size-5 shrink-0 cursor-pointer accent-accent disabled:cursor-not-allowed disabled:opacity-40"
        />
      </div>

      {error && (
        <p className="flex items-start gap-2 rounded-lg border border-danger/30 bg-danger/10 p-3 text-sm text-danger">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span>{error}</span>
        </p>
      )}
    </section>
  );
}
