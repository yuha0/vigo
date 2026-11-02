import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { ReactNode } from "react";

import { Card, CardContent } from "@/components/ui/card";
import type { Status } from "@/gen/vigo/core/v1/core_pb";

export function DeviceCard({ status }: { status: Status | null }) {
  const d = status?.device;
  const rows: [string, ReactNode][] = [
    ["Name", d?.name || "—"],
    ["Model", d?.modelName || "—"],
    [
      "Model code",
      d?.model ? (
        <span>
          <span className="font-mono text-[0.8125rem]">{d.model}</span>
          <span className="block text-xs text-muted-foreground">Encodes the Tizen generation; vigo warns if it changes.</span>
        </span>
      ) : (
        "—"
      ),
    ],
    ["Network", d?.networkType ? d.networkType[0].toUpperCase() + d.networkType.slice(1) : "—"],
    ["Storage", d?.flashSizeGb ? `${d.flashSizeGb} GB flash (whole device, not an art quota)` : "—"],
    ["Motion sensor", d ? (d.supportsMotionSensor ? "Yes" : "No") : "—"],
    ["Light sensor", d ? (d.supportsBrightnessSensor ? "Yes" : "No") : "—"],
    [
      "vigo replica",
      status?.replica ? (
        <span>
          <span className="font-mono text-[0.8125rem] break-all">{status.replica}</span>
          <span className="block text-xs text-muted-foreground">{status.leader ? "Leader" : "Standby"}</span>
        </span>
      ) : (
        "—"
      ),
    ],
    ["Last update", status?.updatedAt ? timestampDate(status.updatedAt).toLocaleString() : "—"],
  ];

  return (
    <Card>
      <CardContent>
        <dl className="divide-y divide-border/70">
          {rows.map(([k, v]) => (
            <div key={k} className="grid grid-cols-[minmax(0,8.5rem)_1fr] gap-4 py-3 first:pt-0 last:pb-0 sm:grid-cols-[11rem_1fr]">
              <dt className="text-sm text-muted-foreground">{k}</dt>
              <dd className="min-w-0 text-[0.9375rem]">{v}</dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  );
}
