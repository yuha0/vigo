import { Characteristic } from "@homebridge/hap-nodejs";

export const INPUT_ART = 1;
export const INPUT_TV = 2;

export const MOTION_TIMER_KEY = "motion_timer";
export const MOTION_TIMER_OFF = "off";
export const DEFAULT_MOTION_TIMER = "5";
export const BRIGHTNESS_KEY = "brightness";

const R = Characteristic.RemoteKey;

/** HomeKit RemoteKey → Samsung key, as specified in docs/design.md. */
export const REMOTE_KEYS: ReadonlyMap<number, string> = new Map([
  [R.ARROW_UP, "KEY_UP"],
  [R.ARROW_DOWN, "KEY_DOWN"],
  [R.ARROW_LEFT, "KEY_LEFT"],
  [R.ARROW_RIGHT, "KEY_RIGHT"],
  [R.SELECT, "KEY_ENTER"],
  [R.BACK, "KEY_RETURN"],
  [R.EXIT, "KEY_EXIT"],
  [R.PLAY_PAUSE, "KEY_PLAY"],
]);

/**
 * Accepted but sends nothing. Apple's remote always shows the (i) button, so pressing it must
 * not surface as an error.
 */
export const SILENT_REMOTE_KEYS: ReadonlySet<number> = new Set([R.INFORMATION]);

export function volumeKey(selector: number): string | undefined {
  if (selector === Characteristic.VolumeSelector.INCREMENT) return "KEY_VOLUP";
  if (selector === Characteristic.VolumeSelector.DECREMENT) return "KEY_VOLDOWN";
  return undefined;
}

/** A TV range value as a HomeKit percentage. */
export function rangeToPercent(value: number, min: number, max: number): number {
  if (max <= min) return 0;
  const clamped = Math.min(max, Math.max(min, value));
  return Math.round(((clamped - min) / (max - min)) * 100);
}

/** A HomeKit percentage as the nearest TV range value. */
export function percentToRange(percent: number, min: number, max: number): number {
  if (max <= min) return min;
  const p = Math.min(100, Math.max(0, percent));
  return Math.round(min + (p / 100) * (max - min));
}
