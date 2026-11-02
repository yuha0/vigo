import { LoaderCircle, Sparkles } from "lucide-react";
import { Link } from "react-router";

import { LiveIndicator } from "@/components/LiveIndicator";
import { Page, PageHeader } from "@/components/Page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { WarningsBanner } from "@/components/WarningsBanner";
import { TvPreview } from "@/features/home/TvPreview";
import { describeError } from "@/lib/errors";
import { useShowArt, useStatus } from "@/lib/queries";
import { confirmedSelection, tvState, type TvStateKind } from "@/lib/tv-state";
import { cn } from "@/lib/utils";

const dot: Partial<Record<TvStateKind, string>> = {
  "showing-art": "bg-success",
  "tv-mode": "bg-success",
  unreachable: "bg-destructive",
  "not-paired": "bg-warning",
  "no-art-host": "bg-warning",
  unknown: "bg-warning",
};

/** Home looks the same in every TV state: the TV card with its preview, actions and warnings. */
export function HomeScreen() {
  const { data, error, refetch } = useStatus();
  const status = data ?? null;
  const state = tvState(status);
  const showArt = useShowArt();
  const loadError = !status && error ? describeError(error) : null;

  return (
    <Page>
      <PageHeader eyebrow="Home" title={status?.device?.name || "The Frame"} />

      {status && <WarningsBanner warnings={status.warnings} className="mb-6" />}

      <Card className="overflow-hidden animate-fade-up" data-testid="tv-card" data-state={state.kind}>
        <div className="grid gap-6 p-5 sm:p-7 md:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)] md:items-center md:gap-9">
          <TvPreview status={status} offline={!!loadError} />

          <div className="flex flex-col gap-5" aria-live="polite">
            <div>
              <p className="flex items-center gap-2 text-xs font-medium tracking-[0.14em] text-muted-foreground uppercase">
                <span className={cn("size-2 rounded-full bg-muted-foreground/50", dot[state.kind])} aria-hidden="true" />
                TV
              </p>
              <h2 className="mt-2 font-display text-3xl font-normal">{loadError ? "Can't reach vigo" : state.title}</h2>
              <p className="mt-2.5 max-w-prose text-[0.9375rem] leading-relaxed text-muted-foreground">
                {loadError ? (loadError.description ?? "vigo isn't answering. Check your connection.") : state.body}
              </p>
            </div>

            <div className="flex flex-wrap gap-2">
              {state.canShowArt && state.kind !== "showing-art" && (
                <Button size="lg" onClick={() => showArt.mutate("")} disabled={showArt.isPending}>
                  {showArt.isPending ? <LoaderCircle className="animate-spin" /> : <Sparkles />}
                  {state.kind === "tv-mode" ? "Switch to art" : "Show art"}
                </Button>
              )}
              {state.kind === "showing-art" && (
                <Button size="lg" variant={confirmedSelection(status) ? "default" : "outline"} asChild>
                  <Link to="/gallery">Choose artwork</Link>
                </Button>
              )}
              {state.kind !== "showing-art" && state.controllable && !confirmedSelection(status) && (
                <Button size="lg" variant="outline" asChild>
                  <Link to="/gallery">Choose artwork</Link>
                </Button>
              )}
              {state.kind === "not-paired" && (
                <Button size="lg" asChild>
                  <Link to="/tv#pairing">Pair with the TV</Link>
                </Button>
              )}
              {loadError && (
                <Button size="lg" variant="outline" onClick={() => void refetch()}>
                  Try again
                </Button>
              )}
              {state.kind !== "loading" && state.kind !== "not-paired" && (
                <Button size="lg" variant="ghost" asChild>
                  <Link to="/tv">TV settings</Link>
                </Button>
              )}
            </div>

            <LiveIndicator className="self-start" />
          </div>
        </div>
      </Card>
    </Page>
  );
}
