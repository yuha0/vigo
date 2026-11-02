import { Accessory, AccessoryInfo, Categories, uuid } from "@homebridge/hap-nodejs";
import { describe, expect, it } from "vitest";
import { generateIdentity, generatePincode, isValidPincode, setupURI } from "./identity";

describe("identity", () => {
  it("generates valid, non-trivial setup codes", () => {
    for (let i = 0; i < 2000; i++) expect(isValidPincode(generatePincode())).toBe(true);
    expect(isValidPincode("111-11-111")).toBe(false);
    expect(isValidPincode("123-45-678")).toBe(false);
    expect(isValidPincode("876-54-321")).toBe(false);
    expect(isValidPincode("000-00-000")).toBe(false);
    expect(isValidPincode("031-45-154")).toBe(true);
  });

  it("generates HAP-valid usernames and setup ids", () => {
    for (let i = 0; i < 200; i++) {
      const id = generateIdentity();
      expect(() => AccessoryInfo.assertValidUsername(id.username)).not.toThrow();
      expect(id.setupID).toMatch(/^[0-9A-Z]{4}$/);
      expect(parseInt(id.username.slice(0, 2), 16) & 0x03).toBe(0x02);
    }
  });

  it("computes the same setup URI as HAP-NodeJS", () => {
    for (const category of [Categories.TELEVISION, Categories.BRIDGE, Categories.SWITCH, Categories.OTHER]) {
      for (let i = 0; i < 50; i++) {
        const id = generateIdentity();
        const acc = new Accessory("Test Accessory", uuid.generate(`test-${i}`));
        const info = AccessoryInfo.create(id.username);
        info.pincode = id.pincode;
        info.category = category;
        acc._accessoryInfo = info;
        (acc as unknown as { _setupID: string })._setupID = id.setupID;
        expect(setupURI(id.pincode, category, id.setupID)).toBe(acc.setupURI());
      }
    }
  });
});
