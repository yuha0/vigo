import { LoaderCircle, Sparkles } from "lucide-react";
import { useId } from "react";

import { LiveIndicator } from "@/components/LiveIndicator";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { ArtModeState, PowerState, type Status } from "@/gen/vigo/core/v1/core_pb";
import { useSetArtMode, useShowArt } from "@/lib/queries";
import type { TvState } from "@/lib/tv-state";

export function ArtModeCard({ status, state }: { status: Status | null; state: TvState }) {
  const showArt = useShowArt();
  const setArtMode = useSetArtMode();
  const switchId = useId();
  const awake = status?.power === PowerState.ON;
  const on = status?.artMode === ArtModeState.ON;
  const canToggle = state.controllable && awake;

  return (
    <Card>
      <CardContent className="grid gap-5">
        <div className="flex items-center justify-between gap-3 rounded-xl bg-muted/60 px-4 py-3">
          <div className="min-w-0">
            <p className="text-xs text-muted-foreground">TV is</p>
            <p className="font-medium">{state.title}</p>
          </div>
          <LiveIndicator />
        </div>

        <div className="flex items-start justify-between gap-4">
          <div>
            <label htmlFor={switchId} className="text-[0.9375rem] font-medium">
              Art Mode
            </label>
            <p className="mt-1 text-sm text-muted-foreground">
              {canToggle
                ? "Switch between Art Mode and TV mode."
                : state.controllable
                  ? "The TV is in standby. Use Show art to wake it."
                  : "Available once the TV is connected."}
            </p>
          </div>
          <Switch
            id={switchId}
            checked={on}
            disabled={!canToggle || setArtMode.isPending}
            onCheckedChange={(v) => setArtMode.mutate(v)}
            className="mt-0.5"
          />
        </div>

        <div className="flex flex-wrap items-center justify-between gap-4 border-t border-border/70 pt-5">
          <p className="max-w-sm text-sm text-muted-foreground">
            Show art wakes the TV the right way for its current state and puts the selected artwork in Art Mode.
          </p>
          <Button onClick={() => showArt.mutate("")} disabled={!state.canShowArt || showArt.isPending}>
            {showArt.isPending ? <LoaderCircle className="animate-spin" /> : <Sparkles />}
            Show art
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
