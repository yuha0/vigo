import { Code, ConnectError, createClient, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { AccessoryKind, HomeService, Protocol, RemoteAction, RemoteButton } from "../gen/vigo/home/v1/home_pb";
import { MatterProvider } from "../matter/provider";
import {
  ACCESSORY_CATALOG,
  ProviderError,
  type AccessoryView,
  type PairingGroupView,
  type Provider,
} from "../provider";
import {
  DEFAULT_REMOTE_MAPPING,
  KEY_OPTIONS,
  bindingsOf,
  isRemoteButton,
  normalize,
  targetError,
  type RemoteBindingView,
  type RemoteMapping,
  type RemoteMappingView,
  type RemoteTargetView,
} from "../remote";
import { homeRoutes, type HomeRuntime } from "./home-service";

class StubProvider implements Provider {
  readonly protocol = "homekit" as const;
  readonly implemented = true;
  enabled = new Set<string>(["television"]);
  resets: string[] = [];
  start = () => Promise.resolve();
  stop = () => Promise.resolve();
  listAccessories(): AccessoryView[] {
    return ACCESSORY_CATALOG.map((a) => ({ ...a, enabled: this.enabled.has(a.id) }));
  }
  setAccessoryEnabled(id: string, enabled: boolean): Promise<AccessoryView> {
    const a = this.listAccessories().find((x) => x.id === id);
    if (!a) return Promise.reject(new ProviderError("not_found", "nope"));
    if (!a.canDisable && !enabled) return Promise.reject(new ProviderError("invalid_argument", "always on"));
    if (enabled) this.enabled.add(id);
    else this.enabled.delete(id);
    return Promise.resolve({ ...a, enabled });
  }
  listPairingGroups(): PairingGroupView[] {
    return [
      { id: "tv", name: "Television", advertised: true, paired: true, setupCode: "111-22-333", setupUri: "X-HM://0000000001ABCD", accessoryIds: ["television"] },
      { id: "bridge", name: "Accessories", advertised: false, paired: false, setupCode: "222-33-444", setupUri: "X-HM://0000000002WXYZ", accessoryIds: [] },
    ];
  }
  remote: RemoteMapping = { ...DEFAULT_REMOTE_MAPPING };
  getRemoteMapping(): RemoteMappingView {
    return { bindings: bindingsOf(this.remote), keys: [...KEY_OPTIONS] };
  }
  updateRemoteMapping(bindings: { button: string; target: unknown }[]): Promise<RemoteBindingView[]> {
    for (const b of bindings) {
      if (!isRemoteButton(b.button)) return Promise.reject(new ProviderError("invalid_argument", "button"));
      const err = targetError(b.target);
      if (err) return Promise.reject(new ProviderError("invalid_argument", err));
      this.remote[b.button] = normalize(b.target as RemoteTargetView);
    }
    return Promise.resolve(bindingsOf(this.remote));
  }
  resetRemoteMapping(): Promise<RemoteBindingView[]> {
    this.remote = { ...DEFAULT_REMOTE_MAPPING };
    return Promise.resolve(bindingsOf(this.remote));
  }
  resetPairing(groupId: string): Promise<PairingGroupView> {
    this.resets.push(groupId);
    const g = this.listPairingGroups().find((x) => x.id === groupId);
    return g ? Promise.resolve({ ...g, paired: false, setupCode: "999-88-777" }) : Promise.reject(new ProviderError("not_found", "nope"));
  }
}

function client(provider: Provider | undefined, overrides: Partial<HomeRuntime> = {}) {
  const rt: HomeRuntime = {
    protocol: provider?.protocol ?? "homekit",
    implemented: provider?.implemented ?? true,
    replica: "vigo-home-0",
    provider: () => provider,
    leader: () => provider !== undefined,
    coreConnected: () => true,
    ...overrides,
  };
  return createClient(HomeService, createRouterTransport(homeRoutes(rt)));
}

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (err) {
    return ConnectError.from(err).code;
  }
}

describe("HomeService", () => {
  it("GetProvider reports protocol, leadership and core connectivity", async () => {
    const res = await client(new StubProvider()).getProvider({});
    expect(res.provider).toMatchObject({ protocol: Protocol.HOMEKIT, implemented: true, leader: true, replica: "vigo-home-0", coreConnected: true });
  });

  it("ListAccessories maps every accessory", async () => {
    const res = await client(new StubProvider()).listAccessories({});
    expect(res.accessories.map((a) => [a.id, a.kind, a.enabled, a.canDisable, a.pairingGroupId])).toEqual([
      ["television", AccessoryKind.TELEVISION, true, false, "tv"],
      ["keep_art_on", AccessoryKind.SWITCH, false, true, "bridge"],
      ["art_mode", AccessoryKind.SWITCH, false, true, "bridge"],
      ["art_brightness", AccessoryKind.LIGHTBULB, false, true, "bridge"],
    ]);
  });

  it("SetAccessoryEnabled returns the updated accessory and maps provider errors", async () => {
    const c = client(new StubProvider());
    expect((await c.setAccessoryEnabled({ id: "art_mode", enabled: true })).accessory).toMatchObject({ id: "art_mode", enabled: true });
    expect(await code(c.setAccessoryEnabled({ id: "nope", enabled: true }))).toBe(Code.NotFound);
    expect(await code(c.setAccessoryEnabled({ id: "television", enabled: false }))).toBe(Code.InvalidArgument);
  });

  it("ListPairingGroups and ResetPairing", async () => {
    const p = new StubProvider();
    const c = client(p);
    const groups = (await c.listPairingGroups({})).groups;
    expect(groups.map((g) => [g.id, g.advertised, g.paired, g.setupCode, g.setupUri])).toEqual([
      ["tv", true, true, "111-22-333", "X-HM://0000000001ABCD"],
      ["bridge", false, false, "222-33-444", "X-HM://0000000002WXYZ"],
    ]);
    const reset = await c.resetPairing({ groupId: "tv" });
    expect(reset.group).toMatchObject({ id: "tv", paired: false, setupCode: "999-88-777" });
    expect(p.resets).toEqual(["tv"]);
    expect(await code(c.resetPairing({ groupId: "nope" }))).toBe(Code.NotFound);
  });

  it("serves the remote mapping with proto enums", async () => {
    const c = client(new StubProvider());
    const m = await c.getRemoteMapping({});
    expect(m.bindings).toHaveLength(13);
    expect(m.bindings[0]).toMatchObject({ button: RemoteButton.POWER_ON, target: { target: { case: "action", value: RemoteAction.SHOW_ART } } });
    expect(m.bindings[2]).toMatchObject({ button: RemoteButton.ARROW_UP, target: { target: { case: "key", value: "KEY_UP" } } });
    expect(m.bindings.find((b) => b.button === RemoteButton.INFORMATION)?.target?.target).toEqual({ case: "action", value: RemoteAction.NONE });
    expect(m.keys).toHaveLength(32);
    expect(m.keys[0]).toEqual(expect.objectContaining({ key: "KEY_UP" }));
    expect(Object.keys(m.keys[0]!).filter((k) => !k.startsWith("$"))).toEqual(["key"]);

    const up = await c.updateRemoteMapping({
      bindings: [{ button: RemoteButton.PLAY_PAUSE, target: { target: { case: "action", value: RemoteAction.TOGGLE_ART_MODE } } }],
    });
    expect(up.bindings.find((b) => b.button === RemoteButton.PLAY_PAUSE)?.target?.target).toEqual({ case: "action", value: RemoteAction.TOGGLE_ART_MODE });
    expect(up.bindings.find((b) => b.button === RemoteButton.SELECT)?.target?.target).toEqual({ case: "key", value: "KEY_ENTER" });

    const reset = await c.resetRemoteMapping({});
    expect(reset.bindings.find((b) => b.button === RemoteButton.PLAY_PAUSE)?.target?.target).toEqual({ case: "action", value: RemoteAction.NONE });
  });

  it("rejects invalid remote bindings with invalid_argument", async () => {
    const c = client(new StubProvider());
    const bind = (button: RemoteButton, target: { case: "key"; value: string } | { case: "action"; value: RemoteAction } | { case: undefined }) =>
      c.updateRemoteMapping({ bindings: [{ button, target: { target: target } }] });
    expect(await code(bind(RemoteButton.SELECT, { case: "key", value: "KEY_POWER" }))).toBe(Code.InvalidArgument);
    expect(await code(bind(RemoteButton.SELECT, { case: "key", value: "KEY_NOPE" }))).toBe(Code.InvalidArgument);
    expect(await code(bind(RemoteButton.UNSPECIFIED, { case: "key", value: "KEY_UP" }))).toBe(Code.InvalidArgument);
    expect(await code(bind(9 as RemoteButton, { case: "key", value: "KEY_UP" }))).toBe(Code.InvalidArgument);
    expect(await code(bind(RemoteButton.SELECT, { case: "action", value: RemoteAction.UNSPECIFIED }))).toBe(Code.InvalidArgument);
    expect(await code(bind(RemoteButton.SELECT, { case: undefined }))).toBe(Code.InvalidArgument);
  });

  it("answers GetProvider on standby and refuses everything else", async () => {
    const c = client(undefined, { coreConnected: () => false });
    expect((await c.getProvider({})).provider).toMatchObject({ leader: false, coreConnected: false });
    expect(await code(c.listAccessories({}))).toBe(Code.Unavailable);
    expect(await code(c.listPairingGroups({}))).toBe(Code.Unavailable);
    expect(await code(c.setAccessoryEnabled({ id: "art_mode", enabled: true }))).toBe(Code.Unavailable);
    expect(await code(c.resetPairing({ groupId: "tv" }))).toBe(Code.Unavailable);
    expect(await code(c.getRemoteMapping({}))).toBe(Code.Unavailable);
    expect(await code(c.updateRemoteMapping({}))).toBe(Code.Unavailable);
    expect(await code(c.resetRemoteMapping({}))).toBe(Code.Unavailable);
  });

  it("serves the matter stub as unimplemented", async () => {
    const c = client(new MatterProvider());
    expect((await c.getProvider({})).provider).toMatchObject({ protocol: Protocol.MATTER, implemented: false });
    expect((await c.listAccessories({})).accessories).toEqual([]);
    expect(await code(c.setAccessoryEnabled({ id: "art_mode", enabled: true }))).toBe(Code.Unimplemented);
    expect(await code(c.resetPairing({ groupId: "tv" }))).toBe(Code.Unimplemented);
  });
});
