import { RefreshCw } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { SettingControl, type Unconfirmed } from "@/features/tv/SettingControl";
import { displayRequested, settingMeta, type SettingValue } from "@/features/tv/setting-meta";
import { useSettings, useUpdateSetting } from "@/features/tv/queries";
import { describeError } from "@/lib/errors";
import type { TvState } from "@/lib/tv-state";

export function SettingsCard({ state }: { state: TvState }) {
  const settings = useSettings(state.controllable);
  const update = useUpdateSetting();
  const [unconfirmed, setUnconfirmed] = useState<Record<string, Unconfirmed>>({});

  const change = (key: string, value: SettingValue) => {
    update.mutate(
      { key, value },
      {
        onSuccess: (res) => {
          setUnconfirmed((u) => {
            const { [key]: _, ...rest } = u;
            return res.confirmed ? rest : { ...rest, [key]: { requested: displayRequested(key, value) } };
          });
          if (!res.confirmed) {
            toast.warning(`${settingMeta(key).label} didn't change`, {
              description: "The TV acknowledged the request but kept its previous value.",
            });
          }
        },
      },
    );
  };

  let body;
  if (!state.controllable) {
    body = <p className="text-[0.9375rem] text-muted-foreground">Settings are available once the TV is connected. {state.title}.</p>;
  } else if (settings.isPending) {
    body = (
      <div className="grid gap-6 py-2">
        {[0, 1, 2].map((i) => (
          <div key={i} className="grid gap-3">
            <Skeleton className="h-4 w-32" />
            <Skeleton className="h-2 w-full" />
          </div>
        ))}
      </div>
    );
  } else if (settings.isError && !settings.data) {
    const d = describeError(settings.error);
    body = (
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[0.9375rem] text-muted-foreground">{d?.title ?? "Couldn't load settings."} {d?.description}</p>
        <Button variant="outline" onClick={() => void settings.refetch()}>
          <RefreshCw /> Retry
        </Button>
      </div>
    );
  } else if (!settings.data.settings.length) {
    body = <p className="text-[0.9375rem] text-muted-foreground">The TV didn't report any Art Mode settings.</p>;
  } else {
    body = (
      <div className="divide-y divide-border/70">
        {settings.data.settings.map((s) => (
          <SettingControl key={s.key} setting={s} unconfirmed={unconfirmed[s.key]} onChange={(v) => change(s.key, v)} />
        ))}
      </div>
    );
  }

  return (
    <Card>
      <CardContent>{body}</CardContent>
    </Card>
  );
}
