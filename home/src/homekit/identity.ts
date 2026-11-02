import { randomBytes, randomInt } from "node:crypto";

/** What a pairing group publishes as. Generated once and persisted in home.json. */
export interface GroupIdentity {
  /** MAC-style HAP device id, "XX:XX:XX:XX:XX:XX". */
  username: string;
  /** Four characters of [0-9A-Z], embedded in the setup URI. */
  setupID: string;
  /** "XXX-XX-XXX". */
  pincode: string;
}

// HAP spec 5.6.1: these setup codes must not be used.
const DISALLOWED_CODES = new Set([
  ...Array.from({ length: 10 }, (_, d) => String(d).repeat(8)),
  "12345678",
  "87654321",
]);

const SETUP_ID_CHARS = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ";

export function generatePincode(): string {
  for (;;) {
    const digits = String(randomInt(0, 100_000_000)).padStart(8, "0");
    if (!DISALLOWED_CODES.has(digits)) return `${digits.slice(0, 3)}-${digits.slice(3, 5)}-${digits.slice(5)}`;
  }
}

export function isValidPincode(code: string): boolean {
  return /^\d{3}-\d{2}-\d{3}$/.test(code) && !DISALLOWED_CODES.has(code.replaceAll("-", ""));
}

export function generateSetupID(): string {
  let id = "";
  for (let i = 0; i < 4; i++) id += SETUP_ID_CHARS[randomInt(0, SETUP_ID_CHARS.length)];
  return id;
}

export function generateUsername(): string {
  const b = randomBytes(6);
  // Locally administered, unicast: never collides with a real NIC's address.
  b[0] = (b[0]! & 0xfc) | 0x02;
  return [...b].map((x) => x.toString(16).padStart(2, "0").toUpperCase()).join(":");
}

export function generateIdentity(): GroupIdentity {
  return { username: generateUsername(), setupID: generateSetupID(), pincode: generatePincode() };
}

/**
 * The X-HM:// setup payload, computed the same way as HAP-NodeJS's Accessory.setupURI(). That
 * method only works on a published accessory; the bridge group must report its URI while it is
 * not advertised too.
 */
export function setupURI(pincode: string, category: number, setupID: string): string {
  const IP_FLAG = 1 << 28;
  const low = BigInt((parseInt(pincode.replaceAll("-", ""), 10) | IP_FLAG) >>> 0) | (BigInt(category & 1) << 31n);
  const high = BigInt(category >> 1);
  const payload = ((high << 32n) | low).toString(36).toUpperCase().padStart(9, "0");
  return `X-HM://${payload}${setupID}`;
}
