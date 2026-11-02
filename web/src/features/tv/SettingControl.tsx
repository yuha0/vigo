import { TriangleAlert } from "lucide-react";
import { useId, useState } from "react";

import { Segmented } from "@/components/ui/segmented";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Slider } from "@/components/ui/slider";
import { Switch } from "@/components/ui/switch";
import type { Setting } from "@/gen/vigo/core/v1/core_pb";
import { displayValue, settingMeta, type SettingValue } from "@/features/tv/setting-meta";
import { cn } from "@/lib/utils";

export type Unconfirmed = { requested: string };

/** One TV setting, rendered from its kind so settings the UI doesn't know about still work. */
export function SettingControl({
  setting,
  onChange,
  disabled,
  unconfirmed,
}: {
  setting: Setting;
  onChange: (v: SettingValue) => void;
  disabled?: boolean;
  unconfirmed?: Unconfirmed;
}) {
  const meta = settingMeta(setting.key);
  const labelId = useId();
  const descId = useId();
  const [draft, setDraft] = useState<number | null>(null);
  const kind = setting.kind;

  const value =
    kind.case === "range" && draft !== null ? (meta.format ? meta.format(draft) : String(draft)) : displayValue(setting);

  return (
    <div className="py-5 first:pt-1 last:pb-1" data-setting={setting.key} data-kind={kind.case ?? "unknown"}>
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <p id={labelId} className="text-[0.9375rem] font-medium">
            {meta.label}
          </p>
          {meta.description && (
            <p id={descId} className="mt-1 max-w-prose text-sm text-muted-foreground">
              {meta.description}
            </p>
          )}
        </div>
        {kind.case === "toggle" ? (
          <Switch
            aria-labelledby={labelId}
            aria-describedby={meta.description ? descId : undefined}
            checked={kind.value.value}
            disabled={disabled}
            onCheckedChange={(v) => onChange({ case: "boolValue", value: v })}
            className="mt-0.5"
          />
        ) : kind.case === "range" ? (
          <span className="shrink-0 rounded-full bg-muted px-2.5 py-1 text-sm font-medium tabular-nums">{value}</span>
        ) : null}
      </div>

      {kind.case === "range" && (
        <div className="mt-2">
          <Slider
            aria-labelledby={labelId}
            aria-label={meta.label}
            min={kind.value.min}
            max={kind.value.max}
            step={1}
            value={[draft ?? kind.value.value]}
            disabled={disabled}
            onValueChange={([v]) => setDraft(v)}
            onValueCommit={([v]) => {
              // Radix fires onValueChange after onValueCommit for keyboard steps; clear the draft after both.
              queueMicrotask(() => setDraft(null));
              if (v !== kind.value.value) onChange({ case: "intValue", value: v });
            }}
          />
          <div className="flex justify-between text-xs text-muted-foreground">
            <span>{meta.minLabel ?? kind.value.min}</span>
            <span>{meta.maxLabel ?? kind.value.max}</span>
          </div>
        </div>
      )}

      {kind.case === "enum" && <EnumControl setting={setting} labelId={labelId} disabled={disabled} onChange={onChange} />}

      {kind.case === undefined && <p className="mt-2 text-sm text-muted-foreground">This setting has a type vigo can't edit yet.</p>}

      {unconfirmed && (
        <p role="status" className={cn("mt-3 flex gap-2 rounded-xl bg-warning-surface px-3 py-2.5 text-sm text-warning-foreground")}>
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
          <span>
            The TV acknowledged {unconfirmed.requested} but didn't apply it. It still reads {displayValue(setting)}.
          </span>
        </p>
      )}
    </div>
  );
}

function EnumControl({
  setting,
  labelId,
  disabled,
  onChange,
}: {
  setting: Setting;
  labelId: string;
  disabled?: boolean;
  onChange: (v: SettingValue) => void;
}) {
  if (setting.kind.case !== "enum") return null;
  const { value, options } = setting.kind.value;
  const meta = settingMeta(setting.key);
  const label = (o: string) => (meta.option ? meta.option(o) : o);
  const all = options.includes(value) || !value ? options : [value, ...options];
  const compact = all.length <= 4 && all.every((o) => label(o).length <= 12);

  if (compact) {
    return (
      <Segmented
        className="mt-3"
        aria-label={meta.label}
        value={value}
        disabled={disabled}
        onValueChange={(v) => onChange({ case: "enumValue", value: v })}
        options={all.map((o) => ({ value: o, label: label(o) }))}
      />
    );
  }
  return (
    <Select value={value} disabled={disabled} onValueChange={(v) => onChange({ case: "enumValue", value: v })}>
      <SelectTrigger className="mt-3 sm:max-w-xs" aria-labelledby={labelId}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {all.map((o) => (
          <SelectItem key={o} value={o}>
            {label(o)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
