import { clone, create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  AccessoryKind,
  KeyOptionSchema,
  RemoteAction,
  RemoteBindingSchema,
  RemoteButton,
  AccessorySchema,
  PairingGroupSchema,
  Protocol,
  ProviderSchema,
  type Accessory,
  type RemoteBinding,
  type PairingGroup,
} from "@/gen/vigo/home/v1/home_pb";
import type { HomeMode } from "@/lib/mock/control";

const CATEGORY = { bridge: 2, television: 31 } as const;

function randomSetupCode(): string {
  const d = Array.from({ length: 8 }, () => Math.floor(Math.random() * 10)).join("");
  return `${d.slice(0, 3)}-${d.slice(3, 5)}-${d.slice(5)}`;
}

/** Builds an X-HM:// setup URI the way HAP-NodeJS does (IP transport flag, base36 payload, setup id). */
export function setupUri(code: string, category: number, setupId: string): string {
  const pin = BigInt(code.replace(/-/g, ""));
  const ipFlag = 2n;
  const payload = (BigInt(category) << 31n) | (ipFlag << 27n) | pin;
  return `X-HM://${payload.toString(36).toUpperCase().padStart(9, "0")}${setupId}`;
}

const verifiedKeys = new Set(["KEY_UP", "KEY_ENTER", "KEY_HOME", "KEY_VOLUP"]);

/** The keys vigo-home allows binding: documented by samsungtvws, never KEY_POWER. */
export const remoteKeys = [
  ["KEY_UP", "Up"],
  ["KEY_DOWN", "Down"],
  ["KEY_LEFT", "Left"],
  ["KEY_RIGHT", "Right"],
  ["KEY_ENTER", "Enter"],
  ["KEY_RETURN", "Return"],
  ["KEY_HOME", "Home"],
  ["KEY_MENU", "Menu"],
  ["KEY_TOOLS", "Tools"],
  ["KEY_GUIDE", "Guide"],
  ["KEY_INFO", "Info"],
  ["KEY_SOURCE", "Source"],
  ["KEY_CH_LIST", "Channel list"],
  ["KEY_CHUP", "Channel up"],
  ["KEY_CHDOWN", "Channel down"],
  ["KEY_VOLUP", "Volume up"],
  ["KEY_VOLDOWN", "Volume down"],
  ["KEY_MUTE", "Mute"],
  ["KEY_RED", "Red"],
  ["KEY_GREEN", "Green"],
  ["KEY_YELLOW", "Yellow"],
  ["KEY_BLUE", "Blue"],
  ...Array.from({ length: 10 }, (_, d) => [`KEY_${d}`, String(d)]),
].map(([key, label]) => create(KeyOptionSchema, { key, label, verified: verifiedKeys.has(key) }));

const defaultKeys: Partial<Record<RemoteButton, string>> = {
  [RemoteButton.ARROW_UP]: "KEY_UP",
  [RemoteButton.ARROW_DOWN]: "KEY_DOWN",
  [RemoteButton.ARROW_LEFT]: "KEY_LEFT",
  [RemoteButton.ARROW_RIGHT]: "KEY_RIGHT",
  [RemoteButton.SELECT]: "KEY_ENTER",
  [RemoteButton.BACK]: "KEY_RETURN",
  [RemoteButton.VOLUME_UP]: "KEY_VOLUP",
  [RemoteButton.VOLUME_DOWN]: "KEY_VOLDOWN",
  [RemoteButton.MUTE]: "KEY_MUTE",
};

const buttonOrder = [
  RemoteButton.POWER_ON,
  RemoteButton.POWER_OFF,
  RemoteButton.ARROW_UP,
  RemoteButton.ARROW_DOWN,
  RemoteButton.ARROW_LEFT,
  RemoteButton.ARROW_RIGHT,
  RemoteButton.SELECT,
  RemoteButton.BACK,
  RemoteButton.PLAY_PAUSE,
  RemoteButton.INFORMATION,
  RemoteButton.VOLUME_UP,
  RemoteButton.VOLUME_DOWN,
  RemoteButton.MUTE,
];

/** Defaults: power-on shows art; arrows, select, back and volume map to their keys; everything else does nothing. */
export function defaultBindings(): RemoteBinding[] {
  return buttonOrder.map((button) => {
    const key = defaultKeys[button];
    const action = button === RemoteButton.POWER_ON ? RemoteAction.SHOW_ART : RemoteAction.NONE;
    return create(RemoteBindingSchema, {
      button,
      target: { target: key ? { case: "key", value: key } : { case: "action", value: action } },
    });
  });
}

export class MockHome {
  mode: HomeMode;
  bindings: RemoteBinding[] = defaultBindings();
  accessories: Accessory[];
  groups: PairingGroup[];

  constructor(mode: HomeMode) {
    this.mode = mode;
    this.accessories = [
      create(AccessorySchema, {
        id: "television",
        name: "The Frame",
        description: "TV tile and Control Center remote. Turning it on shows art.",
        kind: AccessoryKind.TELEVISION,
        enabled: true,
        canDisable: false,
        pairingGroupId: "tv",
      }),
      create(AccessorySchema, {
        id: "keep_art_on",
        name: "Keep Art On",
        description: "On sets the motion timer to off, so art stays displayed.",
        kind: AccessoryKind.SWITCH,
        enabled: true,
        canDisable: true,
        pairingGroupId: "bridge",
      }),
      create(AccessorySchema, {
        id: "art_mode",
        name: "Art Mode",
        description: "Switches between Art Mode and TV mode.",
        kind: AccessoryKind.SWITCH,
        enabled: false,
        canDisable: true,
        pairingGroupId: "bridge",
      }),
      create(AccessorySchema, {
        id: "art_brightness",
        name: "Art Brightness",
        description: "Art Mode brightness as a dimmable light.",
        kind: AccessoryKind.LIGHTBULB,
        enabled: true,
        canDisable: true,
        pairingGroupId: "bridge",
      }),
    ];
    const tvCode = "318-42-907";
    const bridgeCode = "274-90-615";
    this.groups = [
      create(PairingGroupSchema, {
        id: "tv",
        name: "Television",
        advertised: true,
        paired: false,
        setupCode: tvCode,
        setupUri: setupUri(tvCode, CATEGORY.television, "VGTV"),
        accessoryIds: ["television"],
      }),
      create(PairingGroupSchema, {
        id: "bridge",
        name: "Accessories",
        advertised: true,
        paired: true,
        setupCode: bridgeCode,
        setupUri: setupUri(bridgeCode, CATEGORY.bridge, "VGBR"),
        accessoryIds: ["keep_art_on", "art_mode", "art_brightness"],
      }),
    ];
  }

  private check() {
    if (this.mode === "offline") throw new ConnectError("vigo-home is not reachable", Code.Unavailable);
  }

  provider() {
    this.check();
    return create(ProviderSchema, {
      protocol: this.mode === "matter" ? Protocol.MATTER : Protocol.HOMEKIT,
      implemented: this.mode !== "matter",
      leader: true,
      replica: "vigo-home-5d8b7f9c6-qj2wz",
      coreConnected: true,
    });
  }

  listAccessories() {
    this.check();
    if (this.mode === "matter") return [];
    return this.accessories.map((a) => clone(AccessorySchema, a));
  }

  listGroups() {
    this.check();
    if (this.mode === "matter") return [];
    return this.groups.map((g) => clone(PairingGroupSchema, g));
  }

  setEnabled(id: string, enabled: boolean) {
    this.check();
    const a = this.accessories.find((x) => x.id === id);
    if (!a) throw new ConnectError(`no accessory ${id}`, Code.NotFound);
    if (!a.canDisable && !enabled) throw new ConnectError(`${a.name} is always on`, Code.FailedPrecondition);
    a.enabled = enabled;
    const bridge = this.groups.find((g) => g.id === "bridge");
    if (bridge) {
      bridge.advertised = this.accessories.some((x) => x.pairingGroupId === "bridge" && x.enabled);
    }
    return clone(AccessorySchema, a);
  }

  remoteMapping() {
    this.check();
    return { bindings: this.bindings.map((b) => clone(RemoteBindingSchema, b)), keys: remoteKeys.map((k) => clone(KeyOptionSchema, k)) };
  }

  updateRemoteMapping(changes: RemoteBinding[]) {
    this.check();
    for (const c of changes) {
      if (!this.bindings.some((b) => b.button === c.button)) throw new ConnectError("unknown remote button", Code.InvalidArgument);
      const t = c.target?.target;
      if (t?.case === "key") {
        if (t.value === "KEY_POWER") throw new ConnectError("KEY_POWER can't be bound; use Show art", Code.InvalidArgument);
        if (!remoteKeys.some((k) => k.key === t.value)) throw new ConnectError(`${t.value} isn't a key vigo can send`, Code.InvalidArgument);
      } else if (t?.case !== "action" || t.value === RemoteAction.UNSPECIFIED) {
        throw new ConnectError("choose a key or an action", Code.InvalidArgument);
      }
    }
    this.bindings = this.bindings.map((b) => {
      const c = changes.find((x) => x.button === b.button);
      return c ? create(RemoteBindingSchema, { button: b.button, target: c.target }) : b;
    });
    return this.bindings.map((b) => clone(RemoteBindingSchema, b));
  }

  resetRemoteMapping() {
    this.check();
    this.bindings = defaultBindings();
    return this.bindings.map((b) => clone(RemoteBindingSchema, b));
  }

  reset(groupId: string) {
    this.check();
    const g = this.groups.find((x) => x.id === groupId);
    if (!g) throw new ConnectError(`no pairing group ${groupId}`, Code.NotFound);
    g.paired = false;
    g.setupCode = randomSetupCode();
    g.setupUri = setupUri(g.setupCode, groupId === "tv" ? CATEGORY.television : CATEGORY.bridge, groupId === "tv" ? "VGTV" : "VGBR");
    return clone(PairingGroupSchema, g);
  }
}
