'use client';

import { useCallback, useEffect, useRef, useState } from 'react';

export interface PollState<T> {
  /** Last successful payload. Kept across failures so the UI can show
   *  last-known numbers rather than blanking out when the proxy blips. */
  data: T | null;
  error: Error | null;
  loading: boolean;
  /** Epoch ms of the last success — how stale `data` is. */
  lastUpdatedAt: number | null;
}

export interface Poll<T> extends PollState<T> {
  /** Force a poll now, e.g. after a write, without waiting for the interval. */
  refresh: () => void;
  /** Overwrite `data` locally, for a write whose response is the new state. */
  set: (data: T) => void;
}

/**
 * Polls `fetcher` every `intervalMs`.
 *
 * ponytail: setInterval plus an in-flight guard, not a data-fetching library.
 * Two endpoints and one refresh rate do not justify a cache layer. Reach for
 * SWR/React Query if this ever grows revalidation or cross-component sharing.
 */
export function usePoll<T>(fetcher: () => Promise<T>, intervalMs: number): Poll<T> {
  const [state, setState] = useState<PollState<T>>({
    data: null,
    error: null,
    loading: true,
    lastUpdatedAt: null,
  });

  // Held in a ref so an inline arrow fetcher does not restart the interval on
  // every render.
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  const cancelledRef = useRef(false);
  const inFlightRef = useRef(false);

  const tick = useCallback(async () => {
    // A proxy slower than the interval must not stack up overlapping requests.
    if (inFlightRef.current) return;
    inFlightRef.current = true;
    try {
      const data = await fetcherRef.current();
      if (!cancelledRef.current) {
        setState({ data, error: null, loading: false, lastUpdatedAt: Date.now() });
      }
    } catch (err) {
      if (!cancelledRef.current) {
        // Keep the previous data; only the error and loading flag change.
        setState((prev) => ({ ...prev, error: toError(err), loading: false }));
      }
    } finally {
      inFlightRef.current = false;
    }
  }, []);

  useEffect(() => {
    cancelledRef.current = false;
    void tick();
    const id = setInterval(() => void tick(), intervalMs);
    return () => {
      cancelledRef.current = true;
      clearInterval(id);
    };
  }, [intervalMs, tick]);

  const set = useCallback((data: T) => {
    setState({ data, error: null, loading: false, lastUpdatedAt: Date.now() });
  }, []);

  return { ...state, refresh: () => void tick(), set };
}

function toError(err: unknown): Error {
  return err instanceof Error ? err : new Error(String(err));
}
