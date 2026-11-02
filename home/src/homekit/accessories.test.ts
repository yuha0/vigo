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
import { DEFAULT_MOTION_TIMER, REMOTE_KEYS, percentToRange, rangeToPercent } from "./mapping";

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
  const ctx: AccessoryContext = {
    core: c.core,
    log: silentLogger(),
    metrics,
    lastMotionTimer: () => last ?? DEFAULT_MOTION_TIMER,
  };
  return { ...c, ctx, metrics, setLast: (v: string) => (last = v) };
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

  it("maps the RemoteKeys in the design, accepts INFORMATION silently and ignores the rest", async () => {
    const { tv, ctx, metrics } = await setup();
    const { accessory } = buildTelevision(ctx, "The Frame");
    const rk = char(accessory, Service.Television, Characteristic.RemoteKey);
    const R = Characteristic.RemoteKey;
    const expected: [number, string][] = [
      [R.ARROW_UP, "KEY_UP"],
      [R.ARROW_DOWN, "KEY_DOWN"],
      [R.ARROW_LEFT, "KEY_LEFT"],
      [R.ARROW_RIGHT, "KEY_RIGHT"],
      [R.SELECT, "KEY_ENTER"],
      [R.BACK, "KEY_RETURN"],
      [R.EXIT, "KEY_EXIT"],
      [R.PLAY_PAUSE, "KEY_PLAY"],
    ];
    expect([...REMOTE_KEYS.entries()].sort()).toEqual([...expected].sort());
    for (const [hk] of expected) await rk.handleSetRequest(hk);
    expect(calls(tv, "sendKey").map((r) => r.key)).toEqual(expected.map(([, k]) => k));

    await rk.handleSetRequest(R.REWIND);
    expect(calls(tv, "sendKey")).toHaveLength(expected.length);
    expect(await writes(metrics, "television", "ignored")).toBe(1);

    // The (i) button: accepted without an error, and nothing reaches the TV.
    await expect(rk.handleSetRequest(R.INFORMATION)).resolves.toBeUndefined();
    expect(calls(tv, "sendKey")).toHaveLength(expected.length);
    expect(await writes(metrics, "television", "ignored")).toBe(2);
  });

  it("maps the speaker to volume and mute keys", async () => {
    const { tv, ctx } = await setup();
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
