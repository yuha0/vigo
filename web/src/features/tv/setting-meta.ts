import type { Setting, UpdateSettingRequest } from "@/gen/vigo/core/v1/core_pb";

export type SettingMeta = {
  label: string;
  description?: string;
  /** Formats a range value for display. */
  format?: (v: number) => string;
  minLabel?: string;
  maxLabel?: string;
  /** Formats an enum option for display. */
  option?: (v: string) => string;
  /** A shorter form for the closed control, when `option` is long. */
  optionShort?: (v: string) => string;
};

function minutes(v: string): string {
  if (v === "off") return "Off";
  const n = Number(v);
  if (!Number.isFinite(n)) return v;
  if (n < 60) return `${n} min`;
  const h = n / 60;
  return `${h} hour${h === 1 ? "" : "s"}`;
}

const known: Record<string, SettingMeta> = {
  brightness: {
    label: "Brightness",
    description: "How bright the panel is in Art Mode.",
    minLabel: "Dim",
    maxLabel: "Bright",
  },
  color_temperature: {
    label: "Color temperature",
    description: "Shifts the art toward warmer or cooler tones.",
    format: (v) => (v === 0 ? "Neutral" : v < 0 ? `Warm ${-v}` : `Cool ${v}`),
    minLabel: "Warm",
    maxLabel: "Cool",
  },
  motion_sensitivity: {
    label: "Motion sensitivity",
    description: "How readily the motion sensor notices someone in the room.",
    format: (v) => ({ 1: "Low", 2: "Medium", 3: "High" })[v] ?? String(v),
    minLabel: "Low",
    maxLabel: "High",
  },
  motion_timer: {
    label: "Motion timer",
    description:
      "The timer is what darkens the panel when the room is empty: after this long without motion, the TV goes to sleep. Off keeps art displayed all the time.",
    option: (v) => (v === "off" ? "Off · keep art on" : minutes(v)),
    optionShort: minutes,
  },
  brightness_sensor_setting: {
    label: "Auto brightness",
    description: "Lets the light sensor adjust brightness to the room. It can override brightness you set by hand.",
  },
};

function humanize(key: string): string {
  const words = key.replace(/[_-]+/g, " ").trim();
  return words ? words[0].toUpperCase() + words.slice(1) : key;
}

export function settingMeta(key: string): SettingMeta {
  return known[key] ?? { label: humanize(key) };
}

export type SettingValue = UpdateSettingRequest["value"];

export function displayValue(s: Setting): string {
  const meta = settingMeta(s.key);
  switch (s.kind.case) {
    case "range":
      return meta.format ? meta.format(s.kind.value.value) : String(s.kind.value.value);
    case "enum":
      return meta.option ? meta.option(s.kind.value.value) : s.kind.value.value;
    case "toggle":
      return s.kind.value.value ? "On" : "Off";
    default:
      return "Unknown";
  }
}

export function displayRequested(key: string, v: SettingValue): string {
  const meta = settingMeta(key);
  switch (v.case) {
    case "intValue":
      return meta.format ? meta.format(v.value) : String(v.value);
    case "enumValue":
      return meta.option ? meta.option(v.value) : v.value;
    case "boolValue":
      return v.value ? "On" : "Off";
    default:
      return "";
  }
}

/** Returns the setting with `v` applied, for optimistic updates. */
export function withValue(s: Setting, v: SettingValue): Setting {
  if (s.kind.case === "range" && v.case === "intValue") return { ...s, kind: { case: "range", value: { ...s.kind.value, value: v.value } } };
  if (s.kind.case === "enum" && v.case === "enumValue") return { ...s, kind: { case: "enum", value: { ...s.kind.value, value: v.value } } };
  if (s.kind.case === "toggle" && v.case === "boolValue") return { ...s, kind: { case: "toggle", value: { ...s.kind.value, value: v.value } } };
  return s;
}
