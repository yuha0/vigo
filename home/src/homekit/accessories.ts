import {
  Accessory,
  Characteristic,
  HAPStatus,
  HapStatusError,
  Service,
  uuid,
  type CharacteristicValue,
} from "@homebridge/hap-nodejs";
import type { Core, SettingValue } from "../core/core";
import { ArtModeState, PowerState, type Setting, type Status } from "../gen/vigo/core/v1/core_pb";
import { errMsg, type Logger } from "../log";
import type { Metrics } from "../metrics";
import type { AccessoryId } from "../provider";
import {
  BRIGHTNESS_KEY,
  INPUT_ART,
  INPUT_TV,
  MOTION_TIMER_KEY,
  MOTION_TIMER_OFF,
  REMOTE_KEYS,
  percentToRange,
  rangeToPercent,
  volumeKey,
} from "./mapping";

export interface AccessoryContext {
  core: Core;
  log: Logger;
  metrics: Metrics;
  /** Last non-off motion_timer; DEFAULT_MOTION_TIMER when none was seen. */
  lastMotionTimer(): string;
  /** How long a write waits for vigo-core before HomeKit is answered anyway. */
  writeWindowMs?: number;
}

export interface BuiltAccessory {
  accessory: Accessory;
  /** Pushes cached core state (or "no response") to subscribed controllers. */
  refresh(): void;
}

const DEFAULT_WRITE_WINDOW_MS = 5000;

function commFailure(): HapStatusError {
  return new HapStatusError(HAPStatus.SERVICE_COMMUNICATION_FAILURE);
}

function requireStatus(ctx: AccessoryContext): Status {
  const s = ctx.core.status;
  if (!s) throw commFailure();
  return s;
}

function requireSetting(ctx: AccessoryContext, key: string): Setting {
  const s = ctx.core.setting(key);
  if (!s) throw commFailure();
  return s;
}

/**
 * Runs a core call for a characteristic write. HAP controllers give up on slow writes, so after
 * the window the write is acknowledged and the call finishes in the background; WatchEvents
 * corrects the characteristic if it fails.
 */
async function write(ctx: AccessoryContext, id: AccessoryId, what: string, call: () => Promise<void>): Promise<void> {
  const started = Date.now();
  const op = call().then(
    () => {
      ctx.metrics.recordWrite(id, "ok");
      ctx.log.info({ accessory: id, write: what, durationMs: Date.now() - started }, "characteristic write");
    },
    (err: unknown) => {
      ctx.metrics.recordWrite(id, "error");
      ctx.log.warn({ accessory: id, write: what, durationMs: Date.now() - started, err: errMsg(err) }, "characteristic write failed");
      throw err;
    },
  );
  let timer: NodeJS.Timeout | undefined;
  const window = new Promise<"timeout">((resolve) => {
    timer = setTimeout(() => resolve("timeout"), ctx.writeWindowMs ?? DEFAULT_WRITE_WINDOW_MS);
  });
  try {
    const r = await Promise.race([op, window]);
    if (r === "timeout") {
      op.catch(() => undefined);
      ctx.log.info({ accessory: id, write: what }, "write still running; answered HomeKit early");
    }
  } catch {
    throw commFailure();
  } finally {
    clearTimeout(timer);
  }
}

function ignored(ctx: AccessoryContext, id: AccessoryId, what: string, reason: string): void {
  ctx.metrics.recordWrite(id, "ignored");
  ctx.log.info({ accessory: id, write: what, reason }, "characteristic write ignored");
}

export function setInfo(acc: Accessory, model: string, serial: string): void {
  acc
    .getService(Service.AccessoryInformation)!
    .setCharacteristic(Characteristic.Manufacturer, "vigo")
    .setCharacteristic(Characteristic.Model, model)
    .setCharacteristic(Characteristic.SerialNumber, serial);
}

/** Pushes a value, or "no response" when the getter throws. */
function push(c: Characteristic, get: () => CharacteristicValue): void {
  try {
    c.updateValue(get());
  } catch (err) {
    c.updateValue(err instanceof HapStatusError ? err : commFailure());
  }
}

export function accessoryUUID(id: string): string {
  return uuid.generate(`vigo-home:${id}`);
}

export function buildTelevision(ctx: AccessoryContext, name: string): BuiltAccessory {
  const id: AccessoryId = "television";
  const acc = new Accessory(name, accessoryUUID(id));
  setInfo(acc, "The Frame", "television");

  const tv = acc.addService(Service.Television, name);
  tv.setCharacteristic(Characteristic.ConfiguredName, name);
  tv.setCharacteristic(Characteristic.SleepDiscoveryMode, Characteristic.SleepDiscoveryMode.ALWAYS_DISCOVERABLE);
  tv.setPrimaryService(true);

  const active = tv.getCharacteristic(Characteristic.Active);
  const getActive = () =>
    requireStatus(ctx).power === PowerState.ON ? Characteristic.Active.ACTIVE : Characteristic.Active.INACTIVE;
  active.onGet(getActive);
  active.onSet(async (value) => {
    if (value === Characteristic.Active.ACTIVE) {
      await write(ctx, id, "Active=1", () => ctx.core.showArt());
      return;
    }
    // No power-off method has been verified on this TV, so turning the tile off does nothing.
    ignored(ctx, id, "Active=0", "no verified remote power-off");
    setImmediate(() => push(active, getActive));
  });

  const activeId = tv.getCharacteristic(Characteristic.ActiveIdentifier);
  const getActiveId = () => (requireStatus(ctx).artMode === ArtModeState.ON ? INPUT_ART : INPUT_TV);
  activeId.onGet(getActiveId);
  activeId.onSet(async (value) => {
    if (value !== INPUT_ART && value !== INPUT_TV) throw new HapStatusError(HAPStatus.INVALID_VALUE_IN_REQUEST);
    const on = value === INPUT_ART;
    await write(ctx, id, `ActiveIdentifier=${on ? "art" : "tv"}`, () => ctx.core.setArtMode(on));
  });

  tv.getCharacteristic(Characteristic.RemoteKey).onSet(async (value) => {
    const key = REMOTE_KEYS.get(value as number);
    if (!key) {
      ignored(ctx, id, `RemoteKey=${JSON.stringify(value)}`, "no mapping");
      return;
    }
    await write(ctx, id, `RemoteKey=${key}`, () => ctx.core.sendKey(key));
  });

  const speaker = acc.addService(Service.TelevisionSpeaker, `${name} Speaker`);
  speaker.setCharacteristic(Characteristic.VolumeControlType, Characteristic.VolumeControlType.RELATIVE);
  // The TV does not report mute, so this only remembers what HomeKit last asked for.
  let muted = false;
  speaker
    .getCharacteristic(Characteristic.Mute)
    .onGet(() => muted)
    .onSet(async (value) => {
      await write(ctx, id, "Mute", () => ctx.core.sendKey("KEY_MUTE"));
      muted = value as boolean;
    });
  speaker.getCharacteristic(Characteristic.VolumeSelector).onSet(async (value) => {
    const key = volumeKey(value as number);
    if (!key) throw new HapStatusError(HAPStatus.INVALID_VALUE_IN_REQUEST);
    await write(ctx, id, `VolumeSelector=${key}`, () => ctx.core.sendKey(key));
  });
  tv.addLinkedService(speaker);

  for (const [subtype, identifier, label, type] of [
    ["art", INPUT_ART, "Art Mode", Characteristic.InputSourceType.OTHER],
    ["tv", INPUT_TV, "TV", Characteristic.InputSourceType.TUNER],
  ] as const) {
    const input = acc.addService(Service.InputSource, label, subtype);
    input
      .setCharacteristic(Characteristic.Identifier, identifier)
      .setCharacteristic(Characteristic.ConfiguredName, label)
      .setCharacteristic(Characteristic.IsConfigured, Characteristic.IsConfigured.CONFIGURED)
      .setCharacteristic(Characteristic.InputSourceType, type)
      .setCharacteristic(Characteristic.CurrentVisibilityState, Characteristic.CurrentVisibilityState.SHOWN);
    tv.addLinkedService(input);
  }

  return {
    accessory: acc,
    refresh: () => {
      push(active, getActive);
      push(activeId, getActiveId);
    },
  };
}

export function buildKeepArtOn(ctx: AccessoryContext): BuiltAccessory {
  const id: AccessoryId = "keep_art_on";
  const acc = new Accessory("Keep Art On", accessoryUUID(id));
  setInfo(acc, "Keep Art On", id);
  const on = acc.addService(Service.Switch, "Keep Art On").getCharacteristic(Characteristic.On);
  const get = () => {
    const s = requireSetting(ctx, MOTION_TIMER_KEY);
    return s.kind.case === "enum" && s.kind.value.value === MOTION_TIMER_OFF;
  };
  on.onGet(get);
  on.onSet(async (value) => {
    const target = value ? MOTION_TIMER_OFF : ctx.lastMotionTimer();
    const v: SettingValue = { case: "enumValue", value: target };
    await write(ctx, id, `motion_timer=${target}`, async () => {
      await ctx.core.updateSetting(MOTION_TIMER_KEY, v);
    });
  });
  return { accessory: acc, refresh: () => push(on, get) };
}

export function buildArtMode(ctx: AccessoryContext): BuiltAccessory {
  const id: AccessoryId = "art_mode";
  const acc = new Accessory("Art Mode", accessoryUUID(id));
  setInfo(acc, "Art Mode", id);
  const on = acc.addService(Service.Switch, "Art Mode").getCharacteristic(Characteristic.On);
  const get = () => requireStatus(ctx).artMode === ArtModeState.ON;
  on.onGet(get);
  on.onSet(async (value) => {
    await write(ctx, id, `art_mode=${JSON.stringify(value)}`, () => ctx.core.setArtMode(value as boolean));
  });
  return { accessory: acc, refresh: () => push(on, get) };
}

export function buildArtBrightness(ctx: AccessoryContext): BuiltAccessory {
  const id: AccessoryId = "art_brightness";
  const acc = new Accessory("Art Brightness", accessoryUUID(id));
  setInfo(acc, "Art Brightness", id);
  const bulb = acc.addService(Service.Lightbulb, "Art Brightness");

  const range = () => {
    const s = requireSetting(ctx, BRIGHTNESS_KEY);
    if (s.kind.case !== "range") throw commFailure();
    return s.kind.value;
  };

  // Lightbulb requires On. Brightness is the only control, so the bulb always reads as on.
  const on = bulb.getCharacteristic(Characteristic.On);
  const getOn = () => (range(), true);
  on.onGet(getOn);
  on.onSet((value) => {
    if (!value) {
      ignored(ctx, id, "On=false", "brightness-only accessory");
      setImmediate(() => push(on, getOn));
    }
  });

  const brightness = bulb.getCharacteristic(Characteristic.Brightness);
  const getBrightness = () => {
    const r = range();
    return rangeToPercent(r.value, r.min, r.max);
  };
  brightness.onGet(getBrightness);
  brightness.onSet(async (value) => {
    const r = range();
    const target = percentToRange(value as number, r.min, r.max);
    await write(ctx, id, `brightness=${target}`, async () => {
      await ctx.core.updateSetting(BRIGHTNESS_KEY, { case: "intValue", value: target });
    });
  });

  return {
    accessory: acc,
    refresh: () => {
      push(on, getOn);
      push(brightness, getBrightness);
    },
  };
}

export const BRIDGED_BUILDERS: Record<Exclude<AccessoryId, "television">, (ctx: AccessoryContext) => BuiltAccessory> = {
  keep_art_on: buildKeepArtOn,
  art_mode: buildArtMode,
  art_brightness: buildArtBrightness,
};

