import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { Characteristic, HAPStatus, Service, type Accessory } from "@homebridge/hap-nodejs";
import { afterEach, describe, expect, it } from "vitest";
import type { Core } from "../core/core";
import { ArtModeState, PowerState, SettingSchema, StatusSchema } from "../gen/vigo/core/v1/core_pb";
import { silentLogger } from "../log";
import { Metrics } from "../metrics";
import { connectedCore, until, type FakeTV } from "../testing/fake-core";
import {
  buildArtBrightness,
  buildArtMode,
  buildKeepArtOn,
  buildTelevision,
  type AccessoryContext,
} from "./accessories";
import { DEFAULT_REMOTE_MAPPING, type RemoteMapping } from "../remote";
import { DEFAULT_MOTION_TIMER, REMOTE_KEY_BUTTONS, percentToRange, rangeToPercent } from "./mapping";

let core: Core | undefined;
afterEach(async () => {
  await core?.stop();
  core = undefined;
});

async function setup(lastMotionTimer?: string) {
  const c = await connectedCore();
  core = c.core;
  const metrics = new Metrics({ defaultMetrics: false });
  let last = lastMotionTimer;
  const remote: RemoteMapping = { ...DEFAULT_REMOTE_MAPPING };
  const ctx: AccessoryContext = {
    core: c.core,
    log: silentLogger(),
    metrics,
    lastMotionTimer: () => last ?? DEFAULT_MOTION_TIMER,
    remoteTarget: (b) => remote[b],
  };
  return { ...c, ctx, metrics, remote, setLast: (v: string) => (last = v) };
}

function char(acc: Accessory, service: typeof Service.Television | typeof Service.Switch, c: typeof Characteristic.On) {
  return acc.getService(service)!.getCharacteristic(c);
}

async function writes(metrics: Metrics, accessory: string, outcome: string): Promise<number> {
  const m = await metrics.characteristicWrites.get();
  return m.values.find((v) => {
    const l = v.labels as Record<string, unknown>;
    return l.accessory === accessory && l.outcome === outcome;
  })?.value ?? 0;
}

function calls(tv: FakeTV, method: string) {
  return tv.calls.filter((c) => c.method === method).map((c) => c.req as Record<string, unknown>);
}

describe("television", () => {
  it("reads Active from power == ON", async () => {
    const { tv, ctx } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const active = char(accessory, Service.Television, Characteristic.Active);
    expect(await active.handleGetRequest()).toBe(1);

    tv.status = create(StatusSchema, { ...tv.status, power: PowerState.STANDBY });
    tv.emit({ case: "status", value: tv.status });
    await until(() => core!.status?.power === PowerState.STANDBY);
    expect(await active.handleGetRequest()).toBe(0);
  });

  it("Active=1 calls ShowArt; Active=0 is a logged no-op", async () => {
    const { tv, ctx, metrics } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const active = char(accessory, Service.Television, Characteristic.Active);

    await active.handleSetRequest(1);
    expect(calls(tv, "showArt")).toHaveLength(1);
    expect(await writes(metrics, "television", "ok")).toBe(1);

    await active.handleSetRequest(0);
    expect(tv.calls.filter((c) => c.method !== "showArt")).toHaveLength(0);
    expect(await writes(metrics, "television", "ignored")).toBe(1);
    await until(() => active.value === 1);
  });

  it("maps ActiveIdentifier to art mode both ways", async () => {
    const { tv, ctx } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const id = char(accessory, Service.Television, Characteristic.ActiveIdentifier);
    expect(await id.handleGetRequest()).toBe(1);

    await id.handleSetRequest(2);
    expect(calls(tv, "setArtMode")).toEqual([expect.objectContaining({ on: false })]);
    expect(await id.handleGetRequest()).toBe(2);

    await id.handleSetRequest(1);
    expect(calls(tv, "setArtMode")[1]).toEqual(expect.objectContaining({ on: true }));
    expect(await id.handleGetRequest()).toBe(1);

    await expect(id.handleSetRequest(3)).rejects.toBe(HAPStatus.INVALID_VALUE_IN_REQUEST);
  });

  it("maps the RemoteKey values the iOS remote draws", () => {
    const R = Characteristic.RemoteKey;
    const values = [R.ARROW_UP, R.ARROW_DOWN, R.ARROW_LEFT, R.ARROW_RIGHT, R.SELECT, R.BACK, R.PLAY_PAUSE, R.INFORMATION];
    expect([...REMOTE_KEY_BUTTONS.keys()].sort((a, b) => a - b)).toEqual([...values].sort((a, b) => a - b));
  });

  it("sends the default keys and accepts unbound buttons silently", async () => {
    const { tv, ctx, metrics } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const rk = char(accessory, Service.Television, Characteristic.RemoteKey);
    const R = Characteristic.RemoteKey;
    for (const v of [R.ARROW_UP, R.ARROW_DOWN, R.ARROW_LEFT, R.ARROW_RIGHT, R.SELECT, R.BACK]) await rk.handleSetRequest(v);
    expect(calls(tv, "sendKey").map((r) => r.key)).toEqual(["KEY_UP", "KEY_DOWN", "KEY_LEFT", "KEY_RIGHT", "KEY_ENTER", "KEY_RETURN"]);

    const unbound = [R.INFORMATION, R.EXIT, R.PLAY_PAUSE, R.REWIND, R.FAST_FORWARD, R.NEXT_TRACK, R.PREVIOUS_TRACK];
    for (const v of unbound) await expect(rk.handleSetRequest(v)).resolves.toBeUndefined();
    expect(calls(tv, "sendKey")).toHaveLength(6);
    expect(tv.calls.filter((c) => c.method !== "sendKey")).toHaveLength(0);
    expect(await writes(metrics, "television", "ignored")).toBe(unbound.length);
  });

  it("routes the power button through power_on and power_off", async () => {
    const { tv, ctx, remote } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const active = char(accessory, Service.Television, Characteristic.Active);
    remote.power_on = { key: "KEY_HOME" };
    remote.power_off = { action: "toggle_art_mode" };
    await active.handleSetRequest(1);
    await active.handleSetRequest(0);
    expect(tv.calls.map((c) => c.method)).toEqual(["sendKey", "setArtMode"]);
    expect(calls(tv, "sendKey")[0]).toMatchObject({ key: "KEY_HOME" });
    expect(calls(tv, "setArtMode")[0]).toMatchObject({ on: false });
  });

  it("reports the real power state after a power press, not the requested one", async () => {
    const { tv, ctx } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const active = char(accessory, Service.Television, Characteristic.Active);

    tv.status = create(StatusSchema, { ...tv.status, power: PowerState.STANDBY });
    tv.emit({ case: "status", value: tv.status });
    await until(() => core!.status?.power === PowerState.STANDBY);
    await active.handleSetRequest(1);
    await until(() => active.value === 1);
    expect(calls(tv, "showArt")).toHaveLength(1);

    await active.handleSetRequest(0);
    expect(active.value).toBe(0);
    await until(() => active.value === 1);
  });

  it("follows binding changes immediately", async () => {
    const { tv, ctx, remote } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const rk = char(accessory, Service.Television, Characteristic.RemoteKey);
    remote.information = { key: "KEY_MENU" };
    remote.play_pause = { action: "show_art" };
    remote.arrow_up = { action: "none" };
    await rk.handleSetRequest(Characteristic.RemoteKey.INFORMATION);
    await rk.handleSetRequest(Characteristic.RemoteKey.PLAY_PAUSE);
    await rk.handleSetRequest(Characteristic.RemoteKey.ARROW_UP);
    expect(tv.calls.map((c) => [c.method, (c.req as { key?: string }).key])).toEqual([
      ["sendKey", "KEY_MENU"],
      ["showArt", undefined],
    ]);
  });

  it("toggles art mode from the cached state", async () => {
    const { tv, ctx, remote } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const rk = char(accessory, Service.Television, Characteristic.RemoteKey);
    remote.play_pause = { action: "toggle_art_mode" };
    await rk.handleSetRequest(Characteristic.RemoteKey.PLAY_PAUSE);
    await rk.handleSetRequest(Characteristic.RemoteKey.PLAY_PAUSE);
    expect(calls(tv, "setArtMode").map((r) => r.on)).toEqual([false, true]);
    expect(calls(tv, "getStatus")).toHaveLength(0);
  });

  it("reads GetStatus before toggling when the art mode is unknown", async () => {
    const { tv, ctx, remote } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    tv.status = create(StatusSchema, { ...tv.status, artMode: ArtModeState.UNSPECIFIED });
    tv.emit({ case: "status", value: tv.status });
    await until(() => core!.status?.artMode === ArtModeState.UNSPECIFIED);
    tv.status = create(StatusSchema, { ...tv.status, artMode: ArtModeState.OFF });
    remote.select = { action: "toggle_art_mode" };
    await char(accessory, Service.Television, Characteristic.RemoteKey).handleSetRequest(Characteristic.RemoteKey.SELECT);
    expect(calls(tv, "getStatus")).toHaveLength(1);
    expect(calls(tv, "setArtMode").map((r) => r.on)).toEqual([true]);
  });

  it("routes the speaker through the volume and mute bindings", async () => {
    const { tv, ctx, remote, metrics } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const speaker = accessory.getService(Service.TelevisionSpeaker)!;
    const sel = speaker.getCharacteristic(Characteristic.VolumeSelector);
    await sel.handleSetRequest(Characteristic.VolumeSelector.INCREMENT);
    await sel.handleSetRequest(Characteristic.VolumeSelector.DECREMENT);
    const mute = speaker.getCharacteristic(Characteristic.Mute);
    await mute.handleSetRequest(true);
    expect(await mute.handleGetRequest()).toBe(true);
    expect(calls(tv, "sendKey").map((r) => r.key)).toEqual(["KEY_VOLUP", "KEY_VOLDOWN", "KEY_MUTE"]);
    expect(speaker.getCharacteristic(Characteristic.VolumeControlType).value).toBe(Characteristic.VolumeControlType.RELATIVE);

    remote.volume_up = { key: "KEY_CHUP" };
    remote.mute = { action: "none" };
    await sel.handleSetRequest(Characteristic.VolumeSelector.INCREMENT);
    await mute.handleSetRequest(false);
    expect(calls(tv, "sendKey").map((r) => r.key)).toEqual(["KEY_VOLUP", "KEY_VOLDOWN", "KEY_MUTE", "KEY_CHUP"]);
    expect(await writes(metrics, "television", "ignored")).toBe(1);
  });

  it("publishes art and tv input sources linked to the television", async () => {
    const { ctx } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const tvService = accessory.getService(Service.Television)!;
    const inputs = accessory.services.filter((s) => s.UUID === Service.InputSource.UUID);
    expect(inputs.map((s) => [s.subtype, s.getCharacteristic(Characteristic.Identifier).value])).toEqual([
      ["art", 1],
      ["tv", 2],
    ]);
    for (const s of inputs) expect(tvService.linkedServices).toContain(s);
    expect(tvService.linkedServices).toContain(accessory.getService(Service.TelevisionSpeaker));
  });

  it("fails reads with SERVICE_COMMUNICATION_FAILURE when core is unreachable", async () => {
    const { tv, ctx } = await setup();
    const built = buildTelevision(ctx, "The Frame");
    const active = char(built.accessory, Service.Television, Characteristic.Active);
    tv.fail = new ConnectError("down", Code.Unavailable);
    tv.dropStreams();
    await until(() => !core!.connected);
    await expect(active.handleGetRequest()).rejects.toBe(HAPStatus.SERVICE_COMMUNICATION_FAILURE);
    built.refresh();
    expect(active.statusCode).toBe(HAPStatus.SERVICE_COMMUNICATION_FAILURE);
  });

  it("fails writes with SERVICE_COMMUNICATION_FAILURE when core rejects them", async () => {
    const { tv, ctx, metrics } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    tv.fail = new ConnectError("down", Code.Unavailable);
    await expect(char(accessory, Service.Television, Characteristic.Active).handleSetRequest(1)).rejects.toBe(
      HAPStatus.SERVICE_COMMUNICATION_FAILURE,
    );
    expect(await writes(metrics, "television", "error")).toBe(1);
  });
});

describe("keep_art_on", () => {
  it("reads on iff motion_timer is off", async () => {
    const { tv, ctx } = await setup();
    const on = char(buildKeepArtOn(ctx).accessory, Service.Switch, Characteristic.On);
    expect(await on.handleGetRequest()).toBe(false);
    tv.setSetting(create(SettingSchema, { key: "motion_timer", kind: { case: "enum", value: { value: "off", options: [] } } }));
    await until(() => {
      const s = core!.setting("motion_timer");
      return s?.kind.case === "enum" && s.kind.value.value === "off";
    });
    expect(await on.handleGetRequest()).toBe(true);
  });

  it("turns the timer off and restores the remembered value", async () => {
    const { tv, ctx } = await setup("30");
    const on = char(buildKeepArtOn(ctx).accessory, Service.Switch, Characteristic.On);
    await on.handleSetRequest(true);
    await on.handleSetRequest(false);
    expect(calls(tv, "updateSetting")).toEqual([
      expect.objectContaining({ key: "motion_timer", value: { case: "enumValue", value: "off" } }),
      expect.objectContaining({ key: "motion_timer", value: { case: "enumValue", value: "30" } }),
    ]);
  });

  it("restores the default 5 when nothing was remembered", async () => {
    const { tv, ctx } = await setup();
    const on = char(buildKeepArtOn(ctx).accessory, Service.Switch, Characteristic.On);
    await on.handleSetRequest(false);
    expect(calls(tv, "updateSetting")[0]).toEqual(expect.objectContaining({ value: { case: "enumValue", value: "5" } }));
  });
});

describe("art_mode", () => {
  it("reads and sets art mode", async () => {
    const { tv, ctx } = await setup();
    const on = char(buildArtMode(ctx).accessory, Service.Switch, Characteristic.On);
    expect(await on.handleGetRequest()).toBe(true);
    await on.handleSetRequest(false);
    expect(calls(tv, "setArtMode")).toEqual([expect.objectContaining({ on: false })]);
    expect(core!.status?.artMode).toBe(ArtModeState.OFF);
    expect(await on.handleGetRequest()).toBe(false);
  });
});

describe("art_brightness", () => {
  it("converts between percent and the setting range with rounding", () => {
    expect(rangeToPercent(4, 0, 10)).toBe(40);
    expect(rangeToPercent(0, 0, 10)).toBe(0);
    expect(rangeToPercent(10, 0, 10)).toBe(100);
    expect(rangeToPercent(1, 0, 3)).toBe(33);
    expect(rangeToPercent(2, 0, 3)).toBe(67);
    expect(rangeToPercent(12, 0, 10)).toBe(100);
    expect(rangeToPercent(5, 5, 5)).toBe(0);
    expect(percentToRange(44, 0, 10)).toBe(4);
    expect(percentToRange(45, 0, 10)).toBe(5);
    expect(percentToRange(100, 0, 10)).toBe(10);
    expect(percentToRange(0, 2, 12)).toBe(2);
    expect(percentToRange(50, 2, 12)).toBe(7);
    expect(percentToRange(150, 0, 10)).toBe(10);
  });

  it("reads and writes brightness through the range", async () => {
    const { tv, ctx } = await setup();
    const bulb = buildArtBrightness(ctx).accessory.getService(Service.Lightbulb)!;
    const b = bulb.getCharacteristic(Characteristic.Brightness);
    expect(await b.handleGetRequest()).toBe(40);
    await b.handleSetRequest(73);
    expect(calls(tv, "updateSetting")).toEqual([
      expect.objectContaining({ key: "brightness", value: { case: "intValue", value: 7 } }),
    ]);
    expect(await b.handleGetRequest()).toBe(70);
    expect(await bulb.getCharacteristic(Characteristic.On).handleGetRequest()).toBe(true);
  });
});
