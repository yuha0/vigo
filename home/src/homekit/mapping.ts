import { Characteristic } from "@homebridge/hap-nodejs";
import type { RemoteButtonName } from "../remote";

export const INPUT_ART = 1;
export const INPUT_TV = 2;

export const MOTION_TIMER_KEY = "motion_timer";
export const MOTION_TIMER_OFF = "off";
export const DEFAULT_MOTION_TIMER = "5";
export const BRIGHTNESS_KEY = "brightness";

const R = Characteristic.RemoteKey;

/**
 * HomeKit RemoteKey values → remote buttons. What a button does is configurable (../remote.ts).
 * The iOS remote draws no button for the other RemoteKey values (EXIT, REWIND, FAST_FORWARD,
 * NEXT_TRACK, PREVIOUS_TRACK); presses of them are accepted and ignored.
 */
export const REMOTE_KEY_BUTTONS: ReadonlyMap<number, RemoteButtonName> = new Map<number, RemoteButtonName>([
  [R.ARROW_UP, "arrow_up"],
  [R.ARROW_DOWN, "arrow_down"],
  [R.ARROW_LEFT, "arrow_left"],
  [R.ARROW_RIGHT, "arrow_right"],
  [R.SELECT, "select"],
  [R.BACK, "back"],
  [R.PLAY_PAUSE, "play_pause"],
  [R.INFORMATION, "information"],
]);

export function volumeButton(selector: number): RemoteButtonName | undefined {
  if (selector === Characteristic.VolumeSelector.INCREMENT) return "volume_up";
  if (selector === Characteristic.VolumeSelector.DECREMENT) return "volume_down";
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
