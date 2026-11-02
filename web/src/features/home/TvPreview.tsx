import {
  CircleHelp,
  CloudOff,
  Link2Off,
  LoaderCircle,
  MonitorPlay,
  Moon,
  Power,
  ServerCog,
  TriangleAlert,
  Tv,
  type LucideIcon,
} from "lucide-react";

import { ArtImage } from "@/components/ArtImage";
import type { Status } from "@/gen/vigo/core/v1/core_pb";
import { useArtApi } from "@/lib/backend";
import { tvSelection, tvState, type TvStateKind } from "@/lib/tv-state";
import { cn } from "@/lib/utils";

type Placeholder = { icon: LucideIcon; caption: string; screen: string; spin?: boolean };

const placeholders: Record<Exclude<TvStateKind, "showing-art">, Placeholder> = {
  loading: { icon: LoaderCircle, caption: "Checking the TV", screen: "bg-neutral-900", spin: true },
  connecting: { icon: LoaderCircle, caption: "Connecting", screen: "bg-neutral-900", spin: true },
  "tv-mode": { icon: Tv, caption: "Something other than art is on screen", screen: "bg-gradient-to-br from-slate-700 via-slate-800 to-neutral-900" },
  "motion-sleep": { icon: Moon, caption: "Asleep", screen: "bg-neutral-950" },
  "powered-off": { icon: Power, caption: "Powered off", screen: "bg-neutral-950" },
  standby: { icon: Moon, caption: "Standby", screen: "bg-neutral-950" },
  unreachable: { icon: CloudOff, caption: "No answer from the TV", screen: "bg-neutral-900" },
  "not-paired": { icon: Link2Off, caption: "Not paired", screen: "bg-neutral-900" },
  "no-art-host": { icon: MonitorPlay, caption: "Art Mode not started", screen: "bg-neutral-900" },
  "standby-replica": { icon: ServerCog, caption: "No TV connection on this replica", screen: "bg-neutral-900" },
  unknown: { icon: TriangleAlert, caption: "Unknown state", screen: "bg-neutral-900" },
};

/**
 * What the TV is showing, as far as vigo can tell. In Art Mode that is the
 * selected artwork's thumbnail, labelled "Selected" rather than "displaying":
 * the TV reports its selection, not whether the panel is lit. Other states get
 * a placeholder of the same size so the page doesn't jump between states.
 */
export function TvPreview({ status, offline = false, className }: { status: Status | null; offline?: boolean; className?: string }) {
  const art = useArtApi();
  const state = tvState(status);
  const sel = tvSelection(status);
  const selectedId = sel?.id ?? "";
  const guess = !!sel && !sel.confirmed;
  const showArt = state.kind === "showing-art" && !!selectedId;
  const p: Placeholder = offline
    ? { icon: CloudOff, caption: "vigo is unreachable", screen: "bg-neutral-900" }
    : state.kind === "showing-art"
      ? { icon: CircleHelp, caption: "No artwork selected", screen: "bg-neutral-900" }
      : placeholders[state.kind];

  return (
    <figure className={cn("m-0", className)} data-testid="tv-preview" data-state={offline ? "offline" : state.kind}>
      <div className="rounded-[0.625rem] bg-[#e9e4da] p-[3.5%] shadow-[0_1px_2px_rgb(0_0_0/0.08),0_18px_40px_-18px_rgb(0_0_0/0.35)] ring-1 ring-black/5 dark:bg-[#2b2925] dark:ring-white/5">
        <div className="relative aspect-video overflow-hidden rounded-[2px] bg-neutral-900 shadow-[inset_0_0_0_1px_rgb(0_0_0/0.25)]">
          {showArt ? (
            <>
              <ArtImage
                key={selectedId}
                src={art.tvThumbnailUrl(selectedId)}
                alt={`${guess ? "Best guess" : "Selected"} artwork ${selectedId}`}
                eager
                className="absolute inset-0 bg-neutral-900"
              />
              {guess ? (
                <span className="absolute top-2.5 left-2.5 rounded-full bg-black/30 px-2.5 py-1 text-[0.6875rem] font-medium text-white/85 ring-1 ring-white/25 backdrop-blur-md">
                  Best guess
                </span>
              ) : (
                <span className="absolute top-2.5 left-2.5 rounded-full bg-black/45 px-2.5 py-1 text-[0.6875rem] font-medium tracking-[0.12em] text-white uppercase backdrop-blur-md">
                  Selected
                </span>
              )}
            </>
          ) : (
            <div className={cn("absolute inset-0 flex flex-col items-center justify-center gap-2.5 text-white/55", p.screen)}>
              <p.icon className={cn("size-7", p.spin && "animate-spin")} strokeWidth={1.5} aria-hidden="true" />
              <span className="px-4 text-center text-sm">{p.caption}</span>
            </div>
          )}
        </div>
      </div>
      <figcaption className="mt-3 min-h-5 px-1 text-sm text-muted-foreground">
        {showArt && guess ? (
          <>
            Best guess · <span className="font-mono text-[0.8125rem]">{selectedId}</span>
            <span className="mt-1 block">
              Best-effort guess from the TV. It may not match what's on screen until you choose an artwork in vigo.
            </span>
          </>
        ) : showArt ? (
          <>
            Selected · <span className="font-mono text-[0.8125rem]">{selectedId}</span>
          </>
        ) : selectedId && state.canShowArt ? (
          <>
            Show art brings back <span className="font-mono text-[0.8125rem]">{selectedId}</span>
            {guess && " (best guess)"}
          </>
        ) : null}
      </figcaption>
    </figure>
  );
}
