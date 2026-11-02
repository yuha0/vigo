// The protocol-neutral surface HomeService is served from. HomeKit implements it with HAP-NodeJS;
// Matter is a stub. Views are plain objects so a provider never depends on the RPC layer.

export type ProtocolName = "homekit" | "matter";

export type AccessoryId = "television" | "keep_art_on" | "art_mode" | "art_brightness";
export type PairingGroupId = "tv" | "bridge";
export type AccessoryKindName = "television" | "switch" | "lightbulb";

export interface AccessoryView {
  id: AccessoryId;
  name: string;
  description: string;
  kind: AccessoryKindName;
  enabled: boolean;
  canDisable: boolean;
  pairingGroupId: PairingGroupId;
}

export interface PairingGroupView {
  id: PairingGroupId;
  name: string;
  advertised: boolean;
  paired: boolean;
  setupCode: string;
  setupUri: string;
  accessoryIds: AccessoryId[];
}

export interface Provider {
  readonly protocol: ProtocolName;
  readonly implemented: boolean;
  start(): Promise<void>;
  stop(): Promise<void>;
  listAccessories(): AccessoryView[];
  setAccessoryEnabled(id: string, enabled: boolean): Promise<AccessoryView>;
  listPairingGroups(): PairingGroupView[];
  resetPairing(groupId: string): Promise<PairingGroupView>;
}

export type ProviderErrorCode = "not_found" | "invalid_argument" | "unimplemented" | "unavailable" | "internal";

export class ProviderError extends Error {
  constructor(
    readonly code: ProviderErrorCode,
    message: string,
  ) {
    super(message);
    this.name = "ProviderError";
  }
}

export const ACCESSORY_CATALOG: readonly Omit<AccessoryView, "enabled">[] = [
  {
    id: "television",
    name: "Television",
    description: "The Frame as a HomeKit TV: power on shows art, inputs switch Art Mode, and the Control Center remote.",
    kind: "television",
    canDisable: false,
    pairingGroupId: "tv",
  },
  {
    id: "keep_art_on",
    name: "Keep Art On",
    description: "On turns the motion timer off so the panel stays lit; off restores the previous timer.",
    kind: "switch",
    canDisable: true,
    pairingGroupId: "bridge",
  },
  {
    id: "art_mode",
    name: "Art Mode",
    description: "Turns Art Mode on or off.",
    kind: "switch",
    canDisable: true,
    pairingGroupId: "bridge",
  },
  {
    id: "art_brightness",
    name: "Art Brightness",
    description: "Art Mode brightness as a dimmable light.",
    kind: "lightbulb",
    canDisable: true,
    pairingGroupId: "bridge",
  },
];

export const PAIRING_GROUP_NAMES: Record<PairingGroupId, string> = {
  tv: "Television",
  bridge: "Accessories",
};

export function isAccessoryId(id: string): id is AccessoryId {
  return ACCESSORY_CATALOG.some((a) => a.id === id);
}

export function isPairingGroupId(id: string): id is PairingGroupId {
  return id === "tv" || id === "bridge";
}
