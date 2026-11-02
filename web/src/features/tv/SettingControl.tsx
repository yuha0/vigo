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

  // A segmented control can be too wide to sit beside the label on a phone; it may wrap, still right-aligned.
  const segmented = kind.case === "enum" && isCompact(setting);

  const value =
    kind.case === "range" && draft !== null ? (meta.format ? meta.format(draft) : String(draft)) : displayValue(setting);

  return (
    <div className="py-5 first:pt-1 last:pb-1" data-setting={setting.key} data-kind={kind.case ?? "unknown"}>
      {/* Every kind shares one layout: the label on the left with its control or value on the right, then the description underneath. */}
      <div className={cn("flex items-center justify-between gap-x-4 gap-y-3", segmented && "flex-wrap")}>
        <div className={cn("min-w-0 flex-1", segmented && "basis-48")}>
          <p id={labelId} className="text-[0.9375rem] font-medium">
            {meta.label}
          </p>
        </div>
        <div data-slot="setting-control" className="ml-auto flex shrink-0 justify-end">
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
        ) : kind.case === "enum" ? (
          <EnumControl setting={setting} labelId={labelId} disabled={disabled} onChange={onChange} />
        ) : null}
        </div>
      </div>

      {meta.description && (
        <p id={descId} className="mt-1.5 max-w-prose text-sm text-muted-foreground">
          {meta.description}
        </p>
      )}

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

function enumOptions(setting: Setting): string[] {
  if (setting.kind.case !== "enum") return [];
  const { value, options } = setting.kind.value;
  return options.includes(value) || !value ? options : [value, ...options];
}

function isCompact(setting: Setting): boolean {
  const meta = settingMeta(setting.key);
  const label = (o: string) => (meta.option ? meta.option(o) : o);
  const all = enumOptions(setting);
  return all.length <= 4 && all.every((o) => label(o).length <= 12);
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
  const { value } = setting.kind.value;
  const meta = settingMeta(setting.key);
  const label = (o: string) => (meta.option ? meta.option(o) : o);
  const all = enumOptions(setting);

  if (isCompact(setting)) {
    return (
      <Segmented
        className="w-auto"
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
      {/* Hugs its value, so the label beside it keeps one line on a phone. */}
      <SelectTrigger className="w-auto gap-2 px-3.5 whitespace-nowrap" aria-labelledby={labelId}>
        <SelectValue>{(meta.optionShort ?? label)(value)}</SelectValue>
      </SelectTrigger>
      <SelectContent align="end" className="w-auto min-w-44">
        {all.map((o) => (
          <SelectItem key={o} value={o}>
            {label(o)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
