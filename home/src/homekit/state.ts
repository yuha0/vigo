import type { AccessoryId, PairingGroupId } from "../provider";
import { isValidPincode, type GroupIdentity } from "./identity";

/** home.json. */
export interface HomeState {
  version: 1;
  groups: Partial<Record<PairingGroupId, GroupIdentity>>;
  enabled: Partial<Record<AccessoryId, boolean>>;
  /** Last motion_timer value other than "off", restored when keep_art_on turns off. */
  lastMotionTimer?: string;
}

const USERNAME = /^[0-9A-F]{2}(:[0-9A-F]{2}){5}$/;
const SETUP_ID = /^[0-9A-Z]{4}$/;

export function parseHomeState(raw: unknown): HomeState {
  const state: HomeState = { version: 1, groups: {}, enabled: {} };
  if (!raw || typeof raw !== "object") return state;
  const r = raw as Record<string, unknown>;
  if (r.groups && typeof r.groups === "object") {
    for (const [id, g] of Object.entries(r.groups as Record<string, unknown>)) {
      if ((id === "tv" || id === "bridge") && isIdentity(g)) state.groups[id] = { username: g.username, setupID: g.setupID, pincode: g.pincode };
    }
  }
  if (r.enabled && typeof r.enabled === "object") {
    for (const [id, v] of Object.entries(r.enabled as Record<string, unknown>)) {
      if (typeof v === "boolean") state.enabled[id as AccessoryId] = v;
    }
  }
  if (typeof r.lastMotionTimer === "string" && r.lastMotionTimer !== "") state.lastMotionTimer = r.lastMotionTimer;
  return state;
}

function isIdentity(v: unknown): v is GroupIdentity {
  if (!v || typeof v !== "object") return false;
  const g = v as Record<string, unknown>;
  return (
    typeof g.username === "string" &&
    USERNAME.test(g.username) &&
    typeof g.setupID === "string" &&
    SETUP_ID.test(g.setupID) &&
    typeof g.pincode === "string" &&
    isValidPincode(g.pincode)
  );
}
