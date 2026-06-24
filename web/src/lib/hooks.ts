// Lightweight data-fetching hooks. Deliberately tiny — no react-query — to keep
// the embedded bundle small.

import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError } from "@/api/client";
import { notifySessionExpired } from "@/auth/expiry";

export interface AsyncState<T> {
  data: T | null;
  error: ApiError | null;
  loading: boolean;
  /** Re-run the loader. */
  reload: () => void;
}

/**
 * Runs `loader` on mount and whenever a dependency changes, exposing
 * loading/error/data plus a manual reload. The loader receives an AbortSignal.
 */
export function useAsync<T>(
  loader: (signal: AbortSignal) => Promise<T>,
  deps: ReadonlyArray<unknown> = [],
): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);
  const loaderRef = useRef(loader);
  loaderRef.current = loader;

  useEffect(() => {
    const ctrl = new AbortController();
    let active = true;
    setLoading(true);
    setError(null);
    loaderRef
      .current(ctrl.signal)
      .then((result) => {
        if (active) {
          setData(result);
          setLoading(false);
        }
      })
      .catch((err: unknown) => {
        if (!active || ctrl.signal.aborted) return;
        const apiErr = err instanceof ApiError ? err : toApiError(err);
        setError(apiErr);
        setLoading(false);
        // A 401 means the session expired/was revoked: trip the SessionProvider
        // so the operator is routed back to /login instead of getting stuck.
        if (apiErr.isAuth) notifySessionExpired();
      });
    return () => {
      active = false;
      ctrl.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick, ...deps]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { data, error, loading, reload };
}

/**
 * Polls `loader` on an interval. Pauses while the tab is hidden to avoid wasted
 * work; resumes (and fetches immediately) when it becomes visible again.
 */
export function usePolling<T>(
  loader: (signal: AbortSignal) => Promise<T>,
  intervalMs: number,
  enabled = true,
): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(true);
  const loaderRef = useRef(loader);
  loaderRef.current = loader;
  const [tick, setTick] = useState(0);

  const reload = useCallback(() => setTick((t) => t + 1), []);

  useEffect(() => {
    if (!enabled) return;
    let active = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let ctrl = new AbortController();

    const run = async () => {
      ctrl = new AbortController();
      try {
        const result = await loaderRef.current(ctrl.signal);
        if (active) {
          setData(result);
          setError(null);
          setLoading(false);
        }
      } catch (err) {
        if (active && !ctrl.signal.aborted) {
          const apiErr = err instanceof ApiError ? err : toApiError(err);
          setError(apiErr);
          setLoading(false);
          // A 401 means the session expired/was revoked: halt this poller (the
          // session is dead — re-asking just 401s again; `active=false` makes the
          // finally below skip re-scheduling) and trip the SessionProvider so the
          // operator is routed back to /login instead of getting stuck.
          if (apiErr.isAuth) {
            active = false;
            notifySessionExpired();
          }
        }
      } finally {
        if (active && document.visibilityState !== "hidden") {
          timer = setTimeout(run, intervalMs);
        }
      }
    };

    const onVisibility = () => {
      if (document.visibilityState === "visible") {
        if (timer) clearTimeout(timer);
        ctrl.abort();
        run();
      }
    };

    run();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      active = false;
      if (timer) clearTimeout(timer);
      ctrl.abort();
      document.removeEventListener("visibilitychange", onVisibility);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intervalMs, enabled, tick]);

  return { data, error, loading, reload };
}

function toApiError(err: unknown): ApiError {
  return new ApiError(0, {
    code: "internal",
    message: err instanceof Error ? err.message : String(err),
  });
}
