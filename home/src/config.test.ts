import { describe, expect, it } from "vitest";
import { ConfigError, homeEndpoint, parseConfig, parseListen, type Config } from "./config";

describe("config", () => {
  it("has the documented defaults", () => {
    const c = parseConfig([], {}) as Config;
    expect(c).toMatchObject({
      protocol: "homekit",
      listen: { host: "", port: 8081 },
      metricsListen: { host: "", port: 9090 },
      coreUrl: "https://vigo-core.vigo.svc:8443",
      pkiDir: "/etc/vigo/pki",
      advertiseAddress: "",
      hapPortTv: 51830,
      hapPortBridge: 51831,
      hapBind: [],
    });
  });

  it("reads VIGO_* and downward API env, with flags taking precedence", () => {
    const env = { VIGO_PROTOCOL: "matter", VIGO_HAP_PORT_TV: "1234", POD_NAME: "p", POD_IP: "10.0.0.7" };
    const c = parseConfig(["--hap-port-tv", "4321", "--pki-dir", "a", "--pki-dir", "b"], env) as Config;
    expect(c).toMatchObject({ protocol: "matter", hapPortTv: 4321, instance: "p", advertiseAddress: "10.0.0.7", pkiDir: "b" });
    expect((parseConfig(["--advertise-address", "10.0.0.8"], env) as Config).advertiseAddress).toBe("10.0.0.8");
  });

  it("rejects bad values", () => {
    expect(() => parseConfig(["--protocol", "zigbee"], {})).toThrow(ConfigError);
    expect(() => parseConfig(["--hap-port-bridge", "70000"], {})).toThrow(ConfigError);
    expect(() => parseConfig(["--nope"], {})).toThrow();
  });

  it("parses listen addresses", () => {
    expect(parseListen(":8081")).toEqual({ host: "", port: 8081 });
    expect(parseListen("0.0.0.0:9")).toEqual({ host: "0.0.0.0", port: 9 });
    expect(parseListen("[::1]:80")).toEqual({ host: "::1", port: 80 });
  });

  it("builds the endpoint to register", () => {
    const cfg = parseConfig([], { POD_IP: "198.51.100.21" }) as Config;
    expect(homeEndpoint(cfg)).toBe("https://198.51.100.21:8081");
    expect(homeEndpoint({ ...cfg, advertiseAddress: "fd00::1" })).toBe("https://[fd00::1]:8081");
    const detected = homeEndpoint({ ...cfg, advertiseAddress: "" }, {
      lo: [{ address: "127.0.0.1", family: "IPv4", internal: true, netmask: "", mac: "", cidr: null }],
      eth0: [{ address: "192.168.1.5", family: "IPv4", internal: false, netmask: "", mac: "", cidr: null }],
    });
    expect(detected).toBe("https://192.168.1.5:8081");
    expect(() => homeEndpoint({ ...cfg, advertiseAddress: "" }, {})).toThrow(ConfigError);
  });
});
