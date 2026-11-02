import { HAPStorage } from "@homebridge/hap-nodejs";
import { errMsg, type Logger } from "../log";
import { hapKey, hapName } from "./keys";
import type { StateStore } from "./state";

/**
 * The storage HAP-NodeJS persists pairings and identifiers into, backed by vigo-core.
 * HAP-NodeJS 2.2.3 only calls getItem, setItemSync and removeItemSync on HAPStorage.storage(),
 * with JSON-serialisable values, and expects reads of what it just wrote to be immediate.
 */
export class HapStorage {
  private readonly data = new Map<string, unknown>();

  constructor(
    private readonly state: StateStore,
    private readonly log: Logger,
  ) {
    for (const [key, value] of state.entries()) {
      const name = hapName(key);
      if (name === undefined) continue;
      try {
        this.data.set(name, JSON.parse(Buffer.from(value).toString("utf8")));
      } catch (err) {
        // HAP's own file store also reads an unparseable file as missing.
        log.error({ key, err: errMsg(err) }, "ignoring unparseable HAP state");
      }
    }
  }

  getItem(key: string): unknown {
    return this.data.get(key);
  }

  setItemSync(key: string, value: unknown): void {
    const json = JSON.stringify(value);
    this.write(this.state.set(hapKey(key), Buffer.from(json, "utf8")), key);
    this.data.set(key, value);
  }

  removeItemSync(key: string): void {
    this.write(this.state.delete(hapKey(key)), key);
    this.data.delete(key);
  }

  private write(p: Promise<void>, key: string): void {
    p.catch((err: unknown) => this.log.error({ key, err: errMsg(err) }, "persisting HAP state failed"));
  }
}

interface HapStorageInternals {
  INSTANCE?: { localStore?: unknown };
}

/**
 * Makes HAP-NodeJS use `storage` instead of its file store. HAPStorage has no public hook for
 * this, so it sets the private `HAPStorage.INSTANCE.localStore`, which `storage()` returns once
 * set. Must run before any HAP object reads or writes storage. hap-nodejs is pinned to 2.2.3, and
 * storage/hap-internals.test.ts fails if these internals change.
 */
export function installHapStorage(storage: HapStorage): void {
  const instance = (HAPStorage as unknown as HapStorageInternals).INSTANCE;
  if (!instance || !Object.hasOwn(instance, "localStore")) {
    throw new Error("HAP-NodeJS internals changed: HAPStorage.INSTANCE.localStore not found");
  }
  instance.localStore = storage;
}
