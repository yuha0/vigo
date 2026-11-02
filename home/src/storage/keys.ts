// State keys must match [-._a-zA-Z0-9]{1,200}. HAP's own keys normally already do, but the
// mapping is total and reversible: "_" is the escape character, so it is escaped too.

export const HOME_KEY = "home.json";
export const HAP_PREFIX = "hap.";

const SAFE = /^[-.a-zA-Z0-9]$/;

export function encodeKey(name: string): string {
  let out = "";
  for (const ch of name) {
    if (SAFE.test(ch)) {
      out += ch;
      continue;
    }
    for (const byte of Buffer.from(ch, "utf8")) out += "_" + byte.toString(16).padStart(2, "0");
  }
  return out;
}

export function decodeKey(key: string): string {
  const bytes: number[] = [];
  for (let i = 0; i < key.length; ) {
    if (key[i] === "_") {
      const hex = key.slice(i + 1, i + 3);
      if (!/^[0-9a-f]{2}$/.test(hex)) throw new Error(`invalid escape in state key ${JSON.stringify(key)}`);
      bytes.push(parseInt(hex, 16));
      i += 3;
    } else {
      bytes.push(key.charCodeAt(i));
      i += 1;
    }
  }
  return Buffer.from(bytes).toString("utf8");
}

export function hapKey(name: string): string {
  return HAP_PREFIX + encodeKey(name);
}

const STATE_KEY = /^[-._a-zA-Z0-9]{1,200}$/;

export function isStateKey(key: string): boolean {
  return STATE_KEY.test(key);
}

export function hapName(key: string): string | undefined {
  return key.startsWith(HAP_PREFIX) ? decodeKey(key.slice(HAP_PREFIX.length)) : undefined;
}
