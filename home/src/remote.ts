// What each button of the iOS TV remote does. Apple draws the remote (Control Center), so the
// set of buttons is fixed; only their targets can change.

export type RemoteButtonName =
  | "power_on"
  | "power_off"
  | "arrow_up"
  | "arrow_down"
  | "arrow_left"
  | "arrow_right"
  | "select"
  | "back"
  | "play_pause"
  | "information"
  | "volume_up"
  | "volume_down"
  | "mute";

export type RemoteActionName = "none" | "toggle_art_mode" | "show_art";

export type RemoteTargetView = { key: string } | { action: RemoteActionName };

export interface RemoteBindingView {
  button: RemoteButtonName;
  target: RemoteTargetView;
}

export interface KeyOptionView {
  key: string;
}

export interface RemoteMappingView {
  bindings: RemoteBindingView[];
  keys: KeyOptionView[];
}

/**
 * Display order. Every one is drawn by the iOS remote; power_on and power_off are its power
 * button, which HomeKit reports as a write of the Television's Active characteristic.
 */
export const REMOTE_BUTTONS: readonly RemoteButtonName[] = [
  "power_on",
  "power_off",
  "arrow_up",
  "arrow_down",
  "arrow_left",
  "arrow_right",
  "select",
  "back",
  "play_pause",
  "information",
  "volume_up",
  "volume_down",
  "mute",
];

// The keys samsungtvws documents (its shortcuts, plus KEY_<n> digits), KEY_POWER excluded:
// https://github.com/xchwarze/samsung-tv-ws-api/blob/1b7302b1509d62f6c375d8ca401b100ee55670dc/samsungtvws/shortcuts.py
const KEYS: readonly string[] = [
  "KEY_UP",
  "KEY_DOWN",
  "KEY_LEFT",
  "KEY_RIGHT",
  "KEY_ENTER",
  "KEY_RETURN",
  "KEY_HOME",
  "KEY_MENU",
  "KEY_TOOLS",
  "KEY_INFO",
  "KEY_GUIDE",
  "KEY_SOURCE",
  "KEY_CH_LIST",
  "KEY_CHUP",
  "KEY_CHDOWN",
  "KEY_VOLUP",
  "KEY_VOLDOWN",
  "KEY_MUTE",
  "KEY_RED",
  "KEY_GREEN",
  "KEY_YELLOW",
  "KEY_BLUE",
  ...Array.from({ length: 10 }, (_, i) => `KEY_${i}`),
];

export const KEY_OPTIONS: readonly KeyOptionView[] = KEYS.map((key) => ({ key }));

const ALLOWED_KEYS = new Set(KEYS);
const ACTIONS = new Set<string>(["none", "toggle_art_mode", "show_art"]);

export const DEFAULT_REMOTE_MAPPING: Readonly<Record<RemoteButtonName, RemoteTargetView>> = {
  power_on: { action: "show_art" },
  // No power-off method has been verified on this TV.
  power_off: { action: "none" },
  arrow_up: { key: "KEY_UP" },
  arrow_down: { key: "KEY_DOWN" },
  arrow_left: { key: "KEY_LEFT" },
  arrow_right: { key: "KEY_RIGHT" },
  select: { key: "KEY_ENTER" },
  back: { key: "KEY_RETURN" },
  play_pause: { action: "none" },
  information: { action: "none" },
  volume_up: { key: "KEY_VOLUP" },
  volume_down: { key: "KEY_VOLDOWN" },
  mute: { key: "KEY_MUTE" },
};

export function isRemoteButton(b: string): b is RemoteButtonName {
  return (REMOTE_BUTTONS as readonly string[]).includes(b);
}

/** Why the target cannot be bound, or undefined if it can. */
export function targetError(t: unknown): string | undefined {
  if (!t || typeof t !== "object") return "a target needs a key or an action";
  const o = t as Record<string, unknown>;
  if (typeof o.key === "string" && o.action === undefined) {
    if (o.key === "KEY_POWER") {
      return "KEY_POWER cannot be bound: from motion-sleep it powers the TV fully off. Use the show_art action to wake the TV";
    }
    return ALLOWED_KEYS.has(o.key) ? undefined : `key ${JSON.stringify(o.key)} is not in the allowed key list`;
  }
  if (typeof o.action === "string" && o.key === undefined) {
    return ACTIONS.has(o.action) ? undefined : `unknown action ${JSON.stringify(o.action)}`;
  }
  return "a target needs exactly one of a key or an action";
}

export type RemoteMapping = Record<RemoteButtonName, RemoteTargetView>;

/**
 * Reads a stored mapping. Anything missing or invalid falls back to that button's default;
 * entries for buttons that no longer exist are dropped.
 */
export function parseRemoteMapping(raw: unknown): { mapping: RemoteMapping; invalid: string[] } {
  const mapping: RemoteMapping = { ...DEFAULT_REMOTE_MAPPING };
  const invalid: string[] = [];
  const r = raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};
  for (const button of REMOTE_BUTTONS) {
    const t = r[button];
    if (t === undefined) continue;
    if (targetError(t) === undefined) mapping[button] = normalize(t as RemoteTargetView);
    else invalid.push(button);
  }
  return { mapping, invalid };
}

export function normalize(t: RemoteTargetView): RemoteTargetView {
  return "key" in t ? { key: t.key } : { action: t.action };
}

export function bindingsOf(mapping: RemoteMapping): RemoteBindingView[] {
  return REMOTE_BUTTONS.map((button) => ({ button, target: mapping[button] }));
}
