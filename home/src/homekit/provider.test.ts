import { create } from "@bufbuild/protobuf";
import { Categories, type Accessory, type PublishInfo } from "@homebridge/hap-nodejs";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Core } from "../core/core";
import { SettingSchema } from "../gen/vigo/core/v1/core_pb";
import { silentLogger } from "../log";
import { Metrics } from "../metrics";
import { memoryHomeStore } from "../storage/home";
import { connectedCore } from "../testing/fake-core";
import type { HapHost } from "./host";
import { HomeKitProvider } from "./provider";
import type { HomeState } from "./state";

class FakeHost implements HapHost {
  published = new Map<Accessory, PublishInfo>();
  log: string[] = [];
  paired = new Set<string>();
  forgotten: string[] = [];

  publish(acc: Accessory, info: PublishInfo) {
    this.published.set(acc, info);
    this.log.push(`publish ${info.port}`);
    return Promise.resolve();
  }
  unpublish(acc: Accessory) {
    this.log.push(`unpublish ${this.published.get(acc)?.port}`);
    this.published.delete(acc);
    return Promise.resolve();
  }
  destroy(acc: Accessory) {
    const info = this.published.get(acc);
    this.log.push(`destroy ${info?.port}`);
    this.published.delete(acc);
    return Promise.resolve();
  }
  forget(username: string) {
    this.forgotten.push(username);
    this.paired.delete(username);
  }
  isPaired(username: string) {
    return this.paired.has(username);
  }
  byCategory(c: Categories): [Accessory, PublishInfo] | undefined {
    return [...this.published].find(([, i]) => i.category === c);
  }
}

let core: Core;
let host: FakeHost;
let store: ReturnType<typeof memoryHomeStore>;
let provider: HomeKitProvider;

beforeEach(async () => {
  ({ core } = await connectedCore());
  host = new FakeHost();
  store = memoryHomeStore();
  provider = new HomeKitProvider({
    core,
    store,
    log: silentLogger(),
    metrics: new Metrics({ defaultMetrics: false }),
    host,
    tvName: "The Frame",
    hapPortTv: 51830,
    hapPortBridge: 51831,
  });
});

afterEach(async () => {
  await provider.stop();
  await core.stop();
});

function homeJson(): Promise<HomeState> {
  return Promise.resolve(store.saved as HomeState);
}

function bridged(): string[] {
  const b = host.byCategory(Categories.BRIDGE)?.[0];
  return (b?.bridgedAccessories ?? []).map((a) => a.displayName).sort();
}

describe("HomeKitProvider", () => {
  it("publishes only the television when nothing optional is enabled", async () => {
    await provider.start();
    expect(host.log).toEqual(["publish 51830"]);
    const [, info] = host.byCategory(Categories.TELEVISION)!;
    const state = await homeJson();
    expect(info).toMatchObject({ username: state.groups.tv!.username, pincode: state.groups.tv!.pincode, setupID: state.groups.tv!.setupID });
    expect(state.groups.bridge).toBeDefined();
    expect(state.groups.bridge!.username).not.toBe(state.groups.tv!.username);

    const groups = provider.listPairingGroups();
    expect(groups.map((g) => [g.id, g.advertised])).toEqual([
      ["tv", true],
      ["bridge", false],
    ]);
    expect(groups[0]!.setupUri).toMatch(/^X-HM:\/\/[0-9A-Z]{9}[0-9A-Z]{4}$/);
    expect(groups[0]!.setupUri.endsWith(state.groups.tv!.setupID)).toBe(true);
    expect(groups[1]!.setupCode).toBe(state.groups.bridge!.pincode);
  });

  it("keeps identities across restarts", async () => {
    await provider.start();
    const first = await homeJson();
    await provider.stop();
    const again = new HomeKitProvider({
      core,
      store,
      log: silentLogger(),
      metrics: new Metrics({ defaultMetrics: false }),
      host,
      tvName: "The Frame",
      hapPortTv: 51830,
      hapPortBridge: 51831,
    });
    await again.start();
    expect((await homeJson()).groups).toEqual(first.groups);
    await again.stop();
  });

  it("publishes the bridge on first enable and unpublishes it on last disable", async () => {
    await provider.start();
    host.log.length = 0;

    await provider.setAccessoryEnabled("art_mode", true);
    expect(host.log).toEqual(["publish 51831"]);
    expect(bridged()).toEqual(["Art Mode"]);

    await provider.setAccessoryEnabled("keep_art_on", true);
    await provider.setAccessoryEnabled("art_brightness", true);
    expect(host.log).toEqual(["publish 51831"]);
    expect(bridged()).toEqual(["Art Brightness", "Art Mode", "Keep Art On"]);
    expect(provider.listPairingGroups()[1]).toMatchObject({
      advertised: true,
      accessoryIds: ["keep_art_on", "art_mode", "art_brightness"],
    });

    await provider.setAccessoryEnabled("art_mode", false);
    expect(bridged()).toEqual(["Art Brightness", "Keep Art On"]);
    await provider.setAccessoryEnabled("keep_art_on", false);
    await provider.setAccessoryEnabled("art_brightness", false);
    expect(host.log).toEqual(["publish 51831", "unpublish 51831"]);
    expect(host.byCategory(Categories.BRIDGE)).toBeUndefined();
    expect(provider.listPairingGroups()[1]).toMatchObject({ advertised: false, accessoryIds: [] });
    expect((await homeJson()).enabled).toEqual({ art_mode: false, keep_art_on: false, art_brightness: false });

    await provider.setAccessoryEnabled("art_brightness", true);
    expect(host.log.at(-1)).toBe("publish 51831");
    expect(bridged()).toEqual(["Art Brightness"]);
  });

  it("restores enabled accessories on start", async () => {
    await store.saveHome({ version: 1, groups: {}, enabled: { keep_art_on: true } });
    await provider.start();
    expect(host.log).toEqual(["publish 51830", "publish 51831"]);
    expect(bridged()).toEqual(["Keep Art On"]);
  });

  it("rejects disabling the television and unknown ids", async () => {
    await provider.start();
    await expect(provider.setAccessoryEnabled("television", false)).rejects.toMatchObject({ code: "invalid_argument" });
    await expect(provider.setAccessoryEnabled("nope", true)).rejects.toMatchObject({ code: "not_found" });
    expect((await provider.setAccessoryEnabled("television", true)).enabled).toBe(true);
  });

  it("resets pairing with a new identity and republishes", async () => {
    await provider.start();
    const before = (await homeJson()).groups.tv!;
    host.paired.add(before.username);
    expect(provider.listPairingGroups()[0]!.paired).toBe(true);

    const group = await provider.resetPairing("tv");
    const after = (await homeJson()).groups.tv!;
    expect(after.username).not.toBe(before.username);
    expect(after.pincode).not.toBe(before.pincode);
    expect(host.forgotten).toContain(before.username);
    expect(host.log).toEqual(["publish 51830", "destroy 51830", "publish 51830"]);
    expect(group).toMatchObject({ id: "tv", paired: false, advertised: true, setupCode: after.pincode });
    expect(host.byCategory(Categories.TELEVISION)![1].username).toBe(after.username);
  });

  it("resets an unadvertised bridge without publishing it", async () => {
    await provider.start();
    const before = (await homeJson()).groups.bridge!;
    const group = await provider.resetPairing("bridge");
    expect(host.forgotten).toEqual([before.username]);
    expect(group.advertised).toBe(false);
    expect(group.setupCode).not.toBe(before.pincode);
    expect(host.log).toEqual(["publish 51830"]);
  });

  it("remembers the last non-off motion_timer in home.json", async () => {
    await provider.start();
    const set = (value: string) =>
      core.emit("setting", create(SettingSchema, { key: "motion_timer", kind: { case: "enum", value: { value, options: [] } } }));
    set("30");
    set("off");
    await provider.setAccessoryEnabled("art_mode", true);
    expect((await homeJson()).lastMotionTimer).toBe("30");
  });

  it("unpublishes everything on stop", async () => {
    await expect(provider.setAccessoryEnabled("art_mode", true)).rejects.toMatchObject({ code: "unavailable" });
    await provider.start();
    await provider.setAccessoryEnabled("art_mode", true);
    await provider.stop();
    expect(host.published.size).toBe(0);
  });
});

describe("HomeKitProvider remote mapping", () => {
  function newProvider() {
    return new HomeKitProvider({
      core,
      store,
      log: silentLogger(),
      metrics: new Metrics({ defaultMetrics: false }),
      host,
      tvName: "The Frame",
      hapPortTv: 51830,
      hapPortBridge: 51831,
    });
  }

  it("starts with the defaults, in display order", async () => {
    await provider.start();
    const m = provider.getRemoteMapping();
    expect(m.bindings.map((b) => [b.button, b.target])).toEqual([
      ["power_on", { action: "show_art" }],
      ["power_off", { action: "none" }],
      ["arrow_up", { key: "KEY_UP" }],
      ["arrow_down", { key: "KEY_DOWN" }],
      ["arrow_left", { key: "KEY_LEFT" }],
      ["arrow_right", { key: "KEY_RIGHT" }],
      ["select", { key: "KEY_ENTER" }],
      ["back", { key: "KEY_RETURN" }],
      ["play_pause", { action: "none" }],
      ["information", { action: "none" }],
      ["volume_up", { key: "KEY_VOLUP" }],
      ["volume_down", { key: "KEY_VOLDOWN" }],
      ["mute", { key: "KEY_MUTE" }],
    ]);
    expect(m.keys.filter((k) => k.verified).map((k) => k.key).sort()).toEqual(["KEY_ENTER", "KEY_HOME", "KEY_UP", "KEY_VOLUP"]);
    expect(m.keys.map((k) => k.key).sort()).toEqual(
      [
        "KEY_UP", "KEY_DOWN", "KEY_LEFT", "KEY_RIGHT",
        "KEY_ENTER", "KEY_RETURN", "KEY_HOME", "KEY_MENU", "KEY_TOOLS", "KEY_INFO", "KEY_GUIDE", "KEY_SOURCE", "KEY_CH_LIST",
        "KEY_CHUP", "KEY_CHDOWN", "KEY_VOLUP", "KEY_VOLDOWN", "KEY_MUTE",
        "KEY_RED", "KEY_GREEN", "KEY_YELLOW", "KEY_BLUE",
        ...Array.from({ length: 10 }, (_, i) => `KEY_${i}`),
      ].sort(),
    );
  });

  it("updates only the given buttons, persists, and survives a restart", async () => {
    await provider.start();
    const res = await provider.updateRemoteMapping([
      { button: "information", target: { key: "KEY_MENU" } },
      { button: "play_pause", target: { action: "toggle_art_mode" } },
    ]);
    const byButton = Object.fromEntries(res.map((b) => [b.button, b.target]));
    expect(byButton).toMatchObject({ information: { key: "KEY_MENU" }, play_pause: { action: "toggle_art_mode" }, select: { key: "KEY_ENTER" } });
    expect((store.saved as HomeState).remote).toMatchObject({ information: { key: "KEY_MENU" } });

    await provider.stop();
    provider = newProvider();
    await provider.start();
    const again = Object.fromEntries(provider.getRemoteMapping().bindings.map((b) => [b.button, b.target]));
    expect(again).toEqual(byButton);
  });

  it("rejects KEY_POWER, unknown keys, unknown buttons and empty targets, all or nothing", async () => {
    await provider.start();
    await expect(provider.updateRemoteMapping([{ button: "select", target: { key: "KEY_POWER" } }])).rejects.toMatchObject({
      code: "invalid_argument",
      message: expect.stringContaining("motion-sleep") as unknown,
    });
    await expect(provider.updateRemoteMapping([{ button: "select", target: { key: "KEY_PLAY" } }])).rejects.toMatchObject({ code: "invalid_argument" });
    await expect(provider.updateRemoteMapping([{ button: "power", target: { key: "KEY_UP" } }])).rejects.toMatchObject({ code: "invalid_argument" });
    await expect(provider.updateRemoteMapping([{ button: "exit", target: { key: "KEY_UP" } }])).rejects.toMatchObject({ code: "invalid_argument" });
    await expect(provider.updateRemoteMapping([{ button: "power_off", target: { key: "KEY_POWER" } }])).rejects.toMatchObject({ code: "invalid_argument" });
    await expect(provider.updateRemoteMapping([{ button: "select", target: undefined }])).rejects.toMatchObject({ code: "invalid_argument" });
    await expect(
      provider.updateRemoteMapping([
        { button: "information", target: { key: "KEY_MENU" } },
        { button: "select", target: { action: "reboot" } },
      ]),
    ).rejects.toMatchObject({ code: "invalid_argument" });
    expect(provider.getRemoteMapping().bindings.find((b) => b.button === "information")!.target).toEqual({ action: "none" });
  });

  it("resets to the defaults", async () => {
    await provider.start();
    await provider.updateRemoteMapping([{ button: "arrow_up", target: { key: "KEY_CHUP" } }]);
    const res = await provider.resetRemoteMapping();
    expect(res.find((b) => b.button === "arrow_up")!.target).toEqual({ key: "KEY_UP" });
    await provider.stop();
    provider = newProvider();
    await provider.start();
    expect(provider.getRemoteMapping().bindings.find((b) => b.button === "arrow_up")!.target).toEqual({ key: "KEY_UP" });
  });

  it("falls back to the default per button for invalid stored bindings", async () => {
    await store.saveHome({
      version: 1,
      groups: {},
      enabled: {},
      remote: {
        information: { key: "KEY_MENU" },
        select: { key: "KEY_POWER" },
        back: "garbage",
        nonsense: { key: "KEY_UP" },
        exit: { key: "KEY_HOME" },
        rewind: { action: "none" },
      },
    });
    await provider.start();
    const m = Object.fromEntries(provider.getRemoteMapping().bindings.map((b) => [b.button, b.target]));
    expect(m).toMatchObject({ information: { key: "KEY_MENU" }, select: { key: "KEY_ENTER" }, back: { key: "KEY_RETURN" } });
    expect(Object.keys(m)).not.toContain("exit");
    expect(Object.keys(m)).toHaveLength(13);
  });

  it("refuses mutations before start", async () => {
    await expect(provider.resetRemoteMapping()).rejects.toMatchObject({ code: "unavailable" });
  });
});
