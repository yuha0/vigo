import { timestampMs } from "@bufbuild/protobuf/wkt";
import { CircleAlert, FolderSync, LoaderCircle, RefreshCw } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import type { Source } from "@/gen/vigo/core/v1/library_pb";
import { useSyncSource } from "@/features/gallery/library-queries";

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

function ago(ms: number): string {
  const s = (ms - Date.now()) / 1000;
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["day", 86_400],
    ["hour", 3_600],
    ["minute", 60],
  ];
  for (const [unit, size] of units) if (Math.abs(s) >= size) return rtf.format(Math.round(s / size), unit);
  return "just now";
}

export function SourcesPanel({ sources }: { sources: Source[] }) {
  const sync = useSyncSource();
  if (!sources.length) {
    return (
      <Card className="p-5 text-[0.9375rem] text-muted-foreground sm:p-6">
        No sources are configured, so only art already on the TV is listed. Add a folder with vigo-core's <code>--source</code> flag.
      </Card>
    );
  }
  return (
    <Card>
      <ul className="divide-y divide-border/70">
        {sources.map((s) => (
          <li key={s.id} className="grid gap-3 px-5 py-4 sm:px-6" data-source={s.id}>
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0">
                <p className="font-mono text-[0.875rem] break-all">{s.display}</p>
                <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                  <Badge variant="outline">{s.kind}</Badge>
                  <Badge variant="outline">{s.writable ? "Writable" : "Read-only"}</Badge>
                  {s.editable && <Badge variant="outline">Crop in place</Badge>}
                </div>
              </div>
              <Button
                variant="outline"
                onClick={() => sync.mutate(s.id, { onSuccess: () => toast.success(`Syncing ${s.display}`) })}
                disabled={s.syncing || (sync.isPending && sync.variables === s.id)}
              >
                {s.syncing ? <LoaderCircle className="animate-spin" /> : <RefreshCw />}
                {s.syncing ? "Syncing…" : "Sync now"}
              </Button>
            </div>
            <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
              <span>
                {s.artworkCount} artwork{s.artworkCount === 1 ? "" : "s"}
              </span>
              <span className="inline-flex items-center gap-1">
                <FolderSync className="size-3.5" />
                {s.lastSyncedAt ? `Synced ${ago(timestampMs(s.lastSyncedAt))}` : "Never synced"}
              </span>
            </p>
            {s.lastError && (
              <p className="flex gap-2 text-sm text-destructive">
                <CircleAlert className="mt-0.5 size-4 shrink-0" />
                {s.lastError}
              </p>
            )}
          </li>
        ))}
      </ul>
    </Card>
  );
}
