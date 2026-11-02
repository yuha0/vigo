import { Check, Link2, LoaderCircle, RotateCcw, TriangleAlert } from "lucide-react";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { PairingState, type Pairing } from "@/gen/vigo/core/v1/core_pb";
import { useCancelPairing, useConfig, usePairing, useStartPairing } from "@/features/tv/queries";

function useNow(active: boolean, intervalMs = 250): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(t);
  }, [active, intervalMs]);
  return now;
}

export function PairingCard() {
  const pairing = usePairing();
  const config = useConfig();
  const start = useStartPairing();
  const cancel = useCancelPairing();
  const p = pairing.data ?? null;
  const state = p?.state ?? PairingState.UNSPECIFIED;
  const clientName = config.data?.clientName || "vigo";

  const prevState = useRef(state);
  useEffect(() => {
    if (prevState.current === PairingState.WAITING_FOR_ALLOW && state === PairingState.PAIRED) {
      toast.success("Paired with the TV", { description: "vigo can control the TV now." });
    }
    prevState.current = state;
  }, [state]);

  if (pairing.isPending) {
    return (
      <Card>
        <CardContent>
          <Skeleton className="h-16 w-full" />
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardContent>
        {state === PairingState.WAITING_FOR_ALLOW && p ? (
          <Waiting pairing={p} clientName={clientName} onCancel={() => cancel.mutate()} cancelling={cancel.isPending} />
        ) : (
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div className="flex min-w-0 gap-4">
              <StateIcon state={state} />
              <div className="min-w-0">
                <p className="font-medium">
                  {state === PairingState.PAIRED ? "Paired" : state === PairingState.FAILED ? "Pairing didn't finish" : "Not paired"}
                </p>
                <p className="mt-1 max-w-prose text-sm text-muted-foreground">
                  {state === PairingState.PAIRED
                    ? "vigo has an access token for this TV. Pair again only if the TV has forgotten vigo; the current token is kept until the new pairing succeeds."
                    : state === PairingState.FAILED
                      ? p?.message || "The TV didn't confirm the pairing."
                      : `Pairing asks the TV to trust "${clientName}". You'll need the TV's remote nearby to press Allow.`}
                </p>
              </div>
            </div>
            <Button
              variant={state === PairingState.PAIRED ? "outline" : "default"}
              onClick={() => start.mutate()}
              disabled={start.isPending}
            >
              {start.isPending ? <LoaderCircle className="animate-spin" /> : state === PairingState.PAIRED ? <RotateCcw /> : <Link2 />}
              {state === PairingState.PAIRED ? "Pair again" : state === PairingState.FAILED ? "Try again" : "Start pairing"}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function StateIcon({ state }: { state: PairingState }) {
  if (state === PairingState.PAIRED) {
    return (
      <span className="flex size-11 shrink-0 items-center justify-center rounded-full bg-success/12 text-success">
        <Check className="size-5" />
      </span>
    );
  }
  if (state === PairingState.FAILED) {
    return (
      <span className="flex size-11 shrink-0 items-center justify-center rounded-full bg-destructive/10 text-destructive">
        <TriangleAlert className="size-5" />
      </span>
    );
  }
  return (
    <span className="flex size-11 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
      <Link2 className="size-5" />
    </span>
  );
}

function Waiting({ pairing, clientName, onCancel, cancelling }: { pairing: Pairing; clientName: string; onCancel: () => void; cancelling: boolean }) {
  const now = useNow(true);
  const deadline = pairing.deadline ? timestampMs(pairing.deadline) : null;
  const [total] = useState(() => (deadline ? Math.max(1000, deadline - Date.now()) : 30_000));
  const remaining = deadline ? Math.max(0, deadline - now) : null;
  const seconds = remaining === null ? null : Math.ceil(remaining / 1000);
  const fraction = remaining === null ? 1 : remaining / total;
  const r = 34;
  const c = 2 * Math.PI * r;

  return (
    <div className="flex flex-col items-center gap-5 py-4 text-center sm:flex-row sm:items-center sm:text-left" aria-live="polite">
      <div className="relative size-24 shrink-0">
        <svg viewBox="0 0 80 80" className="size-24 -rotate-90" aria-hidden="true">
          <circle cx="40" cy="40" r={r} fill="none" className="stroke-muted" strokeWidth="5" />
          <circle
            cx="40"
            cy="40"
            r={r}
            fill="none"
            className="stroke-brass transition-[stroke-dashoffset] duration-300 ease-linear"
            strokeWidth="5"
            strokeLinecap="round"
            strokeDasharray={c}
            strokeDashoffset={c * (1 - fraction)}
          />
        </svg>
        <span className="absolute inset-0 flex flex-col items-center justify-center">
          <span className="font-display text-3xl tabular-nums" aria-label={seconds === null ? undefined : `${seconds} seconds left`}>
            {seconds ?? "…"}
          </span>
        </span>
      </div>
      <div className="min-w-0 flex-1">
        <p className="font-display text-2xl">Press Allow on the TV</p>
        <p className="mt-1.5 max-w-prose text-[0.9375rem] text-muted-foreground">
          A prompt from “{clientName}” is on the TV screen. Use the TV's remote to choose <strong className="text-foreground">Allow</strong>.
          This page updates as soon as the TV answers.
        </p>
      </div>
      <Button variant="ghost" onClick={onCancel} disabled={cancelling}>
        Cancel
      </Button>
    </div>
  );
}
