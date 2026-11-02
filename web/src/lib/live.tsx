import { create } from "@bufbuild/protobuf";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { createContext, use, useEffect, useState, type ReactNode } from "react";

import {
  ListSettingsResponseSchema,
  TVService,
  type ListSettingsResponse,
  type Pairing,
  type Status,
  type WatchEventsResponse,
} from "@/gen/vigo/core/v1/core_pb";
import { useClient } from "@/lib/backend";
import { qk, setStatus } from "@/lib/queries";
import { isUnauthenticated, notifySessionLost } from "@/lib/session";

export type LiveState = "connecting" | "live" | "reconnecting";

const LiveContext = createContext<LiveState>("connecting");

export function useLiveState(): LiveState {
  return use(LiveContext);
}

export function applyEvent(qc: QueryClient, msg: WatchEventsResponse) {
  const ev = msg.event;
  switch (ev.case) {
    case "status":
      // The stream is ordered, so its snapshots always win.
      qc.setQueryData(qk.status, ev.value);
      break;
    case "artSelected":
      qc.setQueryData<Status | null>(qk.status, (s) => (s ? { ...s, selectedArt: ev.value } : s));
      break;
    case "artAdded":
    case "artDeleted":
    case "libraryChanged":
      // The library mirrors the TV, so any change to either is a reason to refetch what's shown.
      void qc.invalidateQueries({ queryKey: qk.library });
      break;
    case "settingChanged":
      qc.setQueryData<ListSettingsResponse>(qk.settings, (old) => {
        if (!old) return old;
        const exists = old.settings.some((s) => s.key === ev.value.key);
        const settings = exists ? old.settings.map((s) => (s.key === ev.value.key ? ev.value : s)) : [...old.settings, ev.value];
        return create(ListSettingsResponseSchema, { settings });
      });
      break;
    case "pairing":
      qc.setQueryData<Pairing>(qk.pairing, ev.value);
      break;
  }
}

function anySignal(a: AbortSignal, b: AbortSignal): AbortSignal {
  if (typeof AbortSignal.any === "function") return AbortSignal.any([a, b]);
  const c = new AbortController();
  const abort = () => c.abort();
  a.addEventListener("abort", abort, { once: true });
  b.addEventListener("abort", abort, { once: true });
  return c.signal;
}

const MIN_BACKOFF_MS = 500;
/** visibilitychange and pageshow both fire on one return to the app; they should cause one Refresh. */
const REFRESH_DEBOUNCE_MS = 500;
const MAX_BACKOFF_MS = 30_000;

/**
 * Asks vigo-core to re-read the TV whenever someone starts looking: on load,
 * when the page becomes visible, on a back/forward-cache restore and when the
 * network returns.
 *
 * Keeps one WatchEvents stream open and folds its events into the query cache.
 * Reconnects with jittered exponential backoff, and immediately when the page
 * becomes visible or the network comes back (iOS suspends standalone apps and
 * silently kills their sockets).
 */
export function LiveProvider({ children }: { children: ReactNode }) {
  const client = useClient(TVService);
  const qc = useQueryClient();
  const [state, setState] = useState<LiveState>("connecting");

  useEffect(() => {
    const stop = new AbortController();
    let current: AbortController | null = null;
    let wake: (() => void) | null = null;
    let attempt = 0;
    let connectedOnce = false;

    const sleep = (ms: number) =>
      new Promise<void>((resolve) => {
        const done = () => {
          clearTimeout(timer);
          wake = null;
          resolve();
        };
        const timer = setTimeout(done, ms);
        wake = done;
      });

    const restart = () => {
      if (document.visibilityState !== "visible") return;
      current?.abort();
      wake?.();
    };
    document.addEventListener("visibilitychange", restart);
    window.addEventListener("online", restart);

    void (async () => {
      while (!stop.signal.aborted) {
        current = new AbortController();
        const signal = anySignal(stop.signal, current.signal);
        let first = true;
        try {
          for await (const msg of client.watchEvents({}, { signal })) {
            if (first) {
              first = false;
              attempt = 0;
              setState("live");
              if (connectedOnce) {
                // Deltas may have been missed while disconnected.
                void qc.invalidateQueries({ queryKey: qk.settings });
                void qc.invalidateQueries({ queryKey: qk.library });
                void qc.invalidateQueries({ queryKey: qk.pairing });
              }
              connectedOnce = true;
            }
            applyEvent(qc, msg);
          }
        } catch (err) {
          // The auth gate unmounts this provider, which stops the loop.
          if (isUnauthenticated(err)) {
            notifySessionLost();
            return;
          }
          // Other stream errors are expected (server restarts, network loss); retry below.
        }
        if (stop.signal.aborted) return;
        setState("reconnecting");
        if (current.signal.aborted) continue;
        const base = Math.min(MAX_BACKOFF_MS, MIN_BACKOFF_MS * 2 ** attempt);
        attempt++;
        await sleep(base * (0.75 + Math.random() * 0.5));
      }
    })();

    return () => {
      stop.abort();
      wake?.();
      document.removeEventListener("visibilitychange", restart);
      window.removeEventListener("online", restart);
    };
  }, [client, qc]);

  useEffect(() => {
    let last = Number.NEGATIVE_INFINITY;
    const refresh = () => {
      if (document.visibilityState !== "visible") return;
      const now = Date.now();
      if (now - last < REFRESH_DEBOUNCE_MS) return;
      last = now;
      client
        .refresh({})
        .then((res) => {
          setStatus(qc, res.status);
        })
        .catch((err: unknown) => {
          // The stream and the screens already show connectivity problems.
          if (import.meta.env.DEV) console.debug("Refresh failed", err);
        });
    };
    const onPageShow = (e: PageTransitionEvent) => {
      if (e.persisted) refresh();
    };

    refresh();
    document.addEventListener("visibilitychange", refresh);
    window.addEventListener("pageshow", onPageShow);
    window.addEventListener("online", refresh);
    return () => {
      document.removeEventListener("visibilitychange", refresh);
      window.removeEventListener("pageshow", onPageShow);
      window.removeEventListener("online", refresh);
    };
  }, [client, qc]);

  return <LiveContext value={state}>{children}</LiveContext>;
}
