import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { StateItemSchema } from "../gen/vigo/integration/v1/integration_pb";
import { silentLogger } from "../log";
import { FakeIntegration } from "../testing/fake-integration";
import { HapStorage } from "./hap";
import { homeStore } from "./home";
import { decodeKey, encodeKey, hapKey, hapName, isStateKey } from "./keys";
import { StateStore } from "./state";

const enc = (s: string) => Buffer.from(s, "utf8");
const opts = { minBackoffMs: 1, maxBackoffMs: 5 };

async function load(fake: FakeIntegration) {
  return StateStore.load(fake.transport, fake.lease, silentLogger(), opts);
}

describe("state key mapping", () => {
  it("keeps HAP's own key names readable", () => {
    expect(hapKey("AccessoryInfo.0E1234567890.json")).toBe("hap.AccessoryInfo.0E1234567890.json");
    expect(hapName("hap.IdentifierCache.0E1234567890.json")).toBe("IdentifierCache.0E1234567890.json");
    expect(hapName("home.json")).toBeUndefined();
  });

  it("maps any other character reversibly into the allowed set", () => {
    for (const name of ["a_b", "a b", "ü.json", "x:y/z", "__", "_5f", "日本", "a%2F", "-._"]) {
      const key = encodeKey(name);
      expect(isStateKey(key)).toBe(true);
      expect(decodeKey(key)).toBe(name);
    }
    expect(encodeKey("a_b")).toBe("a_5fb");
    expect(() => decodeKey("a_zz")).toThrow();
    expect(isStateKey("x".repeat(201))).toBe(false);
    expect(isStateKey("")).toBe(false);
  });
});

describe("StateStore", () => {
  it("loads everything from ListState", async () => {
    const fake = new FakeIntegration();
    fake.items.set("home.json", create(StateItemSchema, { key: "home.json", value: enc('{"a":1}'), version: 3n }));
    const s = await load(fake);
    expect(Buffer.from(s.get("home.json")!).toString()).toBe('{"a":1}');
  });

  it("writes through with the lease and resolves once stored", async () => {
    const fake = new FakeIntegration();
    const s = await load(fake);
    await s.set("a", enc("1"));
    expect(fake.value("a")).toBe("1");
    expect(fake.calls.at(-1)).toEqual({ method: "putState", key: "a", lease: "lease-1" });
    await s.delete("a");
    expect(fake.items.has("a")).toBe(false);
  });

  it("serves reads from memory before the write lands", async () => {
    const fake = new FakeIntegration();
    const s = await load(fake);
    let release!: () => void;
    fake.gate = new Promise((r) => (release = r));
    const p = s.set("a", enc("1"));
    expect(Buffer.from(s.get("a")!).toString()).toBe("1");
    expect(fake.value("a")).toBeUndefined();
    release();
    await p;
    expect(fake.value("a")).toBe("1");
  });

  it("keeps per-key order and collapses bursts to the latest value", async () => {
    const fake = new FakeIntegration();
    const s = await load(fake);
    let release!: () => void;
    fake.gate = new Promise((r) => (release = r));
    const writes = [s.set("k", enc("1")), s.set("k", enc("2")), s.delete("k"), s.set("k", enc("3"))];
    release();
    await Promise.all(writes);
    expect(fake.value("k")).toBe("3");
    expect(fake.calls.filter((c) => c.key === "k").length).toBeLessThanOrEqual(2);
  });

  it("retries failed writes until they land", async () => {
    const fake = new FakeIntegration();
    const s = await load(fake);
    fake.flakyWrites = 3;
    await s.set("a", enc("1"));
    expect(fake.value("a")).toBe("1");
  });

  it("flush waits for pending writes and reports a timeout", async () => {
    const fake = new FakeIntegration();
    const s = await load(fake);
    fake.fail = new ConnectError("down", Code.Unavailable);
    void s.set("a", enc("1"));
    expect(await s.flush(30)).toBe(false);
    fake.fail = undefined;
    expect(await s.flush(2000)).toBe(true);
    expect(fake.value("a")).toBe("1");
  });

  it("rejects keys outside the allowed set", async () => {
    const s = await load(new FakeIntegration());
    expect(() => s.set("bad key", enc("x"))).toThrow(/invalid state key/);
  });
});

describe("HapStorage", () => {
  it("preloads HAP items and writes JSON through under hap.<key>", async () => {
    const fake = new FakeIntegration();
    fake.items.set("hap.AccessoryInfo.AABB.json", create(StateItemSchema, { key: "hap.AccessoryInfo.AABB.json", value: enc('{"pincode":"1"}'), version: 1n }));
    fake.items.set("home.json", create(StateItemSchema, { key: "home.json", value: enc("{}"), version: 1n }));
    const s = await load(fake);
    const hap = new HapStorage(s, silentLogger());
    expect(hap.getItem("AccessoryInfo.AABB.json")).toEqual({ pincode: "1" });
    expect(hap.getItem("home.json")).toBeUndefined();

    hap.setItemSync("IdentifierCache.AABB.json", { cache: { a: 1 } });
    expect(hap.getItem("IdentifierCache.AABB.json")).toEqual({ cache: { a: 1 } });
    hap.removeItemSync("AccessoryInfo.AABB.json");
    expect(hap.getItem("AccessoryInfo.AABB.json")).toBeUndefined();
    expect(await s.flush(2000)).toBe(true);
    expect(JSON.parse(fake.value("hap.IdentifierCache.AABB.json")!)).toEqual({ cache: { a: 1 } });
    expect(fake.items.has("hap.AccessoryInfo.AABB.json")).toBe(false);
  });
});

describe("homeStore", () => {
  it("round-trips home.json through vigo-core", async () => {
    const fake = new FakeIntegration();
    const a = homeStore(await load(fake));
    expect(a.loadHome()).toBeUndefined();
    await a.saveHome({ enabled: { art_mode: true } });
    const b = homeStore(await load(fake));
    expect(b.loadHome()).toEqual({ enabled: { art_mode: true } });
  });
});

describe("StateStore after close", () => {
  it("keeps late changes in memory without writing or throwing", async () => {
    const fake = new FakeIntegration();
    const s = await load(fake);
    s.close();
    await s.set("a", enc("1"));
    expect(Buffer.from(s.get("a")!).toString()).toBe("1");
    expect(fake.items.has("a")).toBe(false);
  });
});
