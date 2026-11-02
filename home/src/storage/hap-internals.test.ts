import { readdirSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { Accessory, AccessoryInfo, HAPStorage } from "@homebridge/hap-nodejs";
import { describe, expect, it } from "vitest";
import { silentLogger } from "../log";
import { FakeIntegration } from "../testing/fake-integration";
import { HapStorage, installHapStorage } from "./hap";
import { StateStore } from "./state";

// vigo-home replaces HAP-NodeJS's file storage through private internals. These tests pin down
// every assumption that makes that safe, so a hap-nodejs upgrade that breaks one fails here
// instead of silently losing pairings.

const require = createRequire(import.meta.url);
const hapRoot = dirname(require.resolve("@homebridge/hap-nodejs/package.json"));

function jsFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
    e.isDirectory() ? jsFiles(join(dir, e.name)) : e.name.endsWith(".js") ? [join(dir, e.name)] : [],
  );
}

describe("HAP-NodeJS storage internals", () => {
  it("is the pinned version", () => {
    const pkg = JSON.parse(readFileSync(join(hapRoot, "package.json"), "utf8")) as { version: string };
    expect(pkg.version, "hap-nodejs upgraded: re-verify storage/hap.ts against the new HAPStorage").toBe("2.2.3");
  });

  it("keeps the store in HAPStorage.INSTANCE.localStore and returns it from storage()", () => {
    const instance = (HAPStorage as unknown as { INSTANCE?: object }).INSTANCE;
    expect(instance, "HAPStorage.INSTANCE is gone").toBeInstanceOf(HAPStorage);
    expect(Object.hasOwn(instance!, "localStore"), "HAPStorage#localStore is gone").toBe(true);
    const src = readFileSync(join(hapRoot, "dist/lib/model/HAPStorage.js"), "utf8");
    expect(src, "HAPStorage.storage() no longer returns an existing localStore").toMatch(/storage\(\) \{\s*if \(!this\.localStore\)/);
  });

  it("only ever calls getItem, setItemSync and removeItemSync on the store", () => {
    const used = new Set<string>();
    for (const file of jsFiles(join(hapRoot, "dist"))) {
      for (const m of readFileSync(file, "utf8").matchAll(/HAPStorage\.storage\(\)\.(\w+)/g)) used.add(m[1]!);
    }
    expect([...used].sort()).toEqual(["getItem", "removeItemSync", "setItemSync"]);
  });

  it("persists accessory info through the installed store", async () => {
    const fake = new FakeIntegration();
    const state = await StateStore.load(fake.transport, fake.lease, silentLogger());
    installHapStorage(new HapStorage(state, silentLogger()));
    expect(HAPStorage.storage()).toBeInstanceOf(HapStorage);

    const info = AccessoryInfo.create("0E:11:22:33:44:55");
    info.pincode = "031-45-154";
    info.save();
    expect(await state.flush(2000)).toBe(true);
    expect(JSON.parse(fake.value("hap.AccessoryInfo.0E1122334455.json")!)).toMatchObject({ pincode: "031-45-154" });

    const fresh = await StateStore.load(fake.transport, fake.lease, silentLogger());
    installHapStorage(new HapStorage(fresh, silentLogger()));
    expect(AccessoryInfo.load("0E:11:22:33:44:55")?.pincode).toBe("031-45-154");

    Accessory.cleanupAccessoryData("0E:11:22:33:44:55");
    expect(await fresh.flush(2000)).toBe(true);
    expect([...fake.items.keys()].filter((k) => k.startsWith("hap."))).toEqual([]);
  });
});
