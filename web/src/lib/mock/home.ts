import { clone, create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  AccessoryKind,
  AccessorySchema,
  PairingGroupSchema,
  Protocol,
  ProviderSchema,
  type Accessory,
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

export class MockHome {
  mode: HomeMode;
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
