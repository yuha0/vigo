import { hostname, networkInterfaces } from "node:os";
import { parseArgs } from "node:util";

export type Protocol = "homekit" | "matter";
export interface Config {
  protocol: Protocol;
  listen: { host: string; port: number };
  metricsListen: { host: string; port: number };
  coreUrl: string;
  pkiDir: string;
  /** Address vigo-core reaches :8081 on; empty means "detect". */
  advertiseAddress: string;
  instance: string;
  tvName: string;
  hapPortTv: number;
  hapPortBridge: number;
  hapBind: string[];
  logLevel: string;
}

interface FlagSpec {
  env: string;
  type: "string" | "boolean";
  default?: string;
  help: string;
}

const flags = {
  protocol: { env: "VIGO_PROTOCOL", type: "string", default: "homekit", help: "homekit | matter" },
  listen: { env: "VIGO_LISTEN", type: "string", default: ":8081", help: "HomeService address (mTLS)" },
  "metrics-listen": { env: "VIGO_METRICS_LISTEN", type: "string", default: ":9090", help: "plain-HTTP /healthz, /readyz and /metrics address" },
  "core-url": { env: "VIGO_CORE_URL", type: "string", default: "https://vigo-core.vigo.svc:8443", help: "vigo-core mTLS base URL" },
  "pki-dir": { env: "VIGO_PKI_DIR", type: "string", default: "/etc/vigo/pki", help: "directory with tls.crt, tls.key and ca.crt" },
  "advertise-address": { env: "VIGO_ADVERTISE_ADDRESS", type: "string", help: "address registered with vigo-core for HomeService (default: POD_IP, then the first non-loopback IPv4)" },
  "tv-name": { env: "VIGO_TV_NAME", type: "string", default: "The Frame", help: "name of the Television accessory" },
  "hap-port-tv": { env: "VIGO_HAP_PORT_TV", type: "string", default: "51830", help: "HAP port of the tv pairing group" },
  "hap-port-bridge": { env: "VIGO_HAP_PORT_BRIDGE", type: "string", default: "51831", help: "HAP port of the bridge pairing group" },
  "hap-bind": { env: "VIGO_HAP_BIND", type: "string", default: "", help: "comma-separated interfaces/addresses to advertise and bind HAP on (default: all)" },
  "log-level": { env: "VIGO_LOG_LEVEL", type: "string", default: "info", help: "pino log level" },
  help: { env: "", type: "boolean", help: "show this help" },
} as const satisfies Record<string, FlagSpec>;

export function usage(): string {
  const lines = ["Usage: vigo-home [flags]", ""];
  for (const [name, spec] of Object.entries(flags) as [string, FlagSpec][]) {
    const def = spec.default !== undefined && spec.default !== "" ? ` (default ${JSON.stringify(spec.default)})` : "";
    const env = spec.env ? ` [${spec.env}]` : "";
    lines.push(`  --${name.padEnd(16)} ${spec.help}${def}${env}`);
  }
  return lines.join("\n");
}

export class ConfigError extends Error {}

export function parseConfig(argv: string[], env: NodeJS.ProcessEnv): Config | "help" {
  const { values } = parseArgs({
    args: argv,
    options: Object.fromEntries(Object.entries(flags).map(([k, v]) => [k, { type: v.type }])),
    strict: true,
    allowPositionals: false,
  });
  if (values.help) return "help";

  const str = (name: keyof typeof flags): string | undefined => {
    const spec: FlagSpec = flags[name];
    const v = values[name];
    if (typeof v === "string") return v;
    const e = spec.env ? env[spec.env] : undefined;
    return e !== undefined && e !== "" ? e : spec.default;
  };
  const port = (name: keyof typeof flags): number => {
    const raw = str(name) ?? "";
    const n = Number(raw);
    if (!Number.isInteger(n) || n < 0 || n > 65535) throw new ConfigError(`--${name}: invalid port ${raw}`);
    return n;
  };
  const oneOf = <T extends string>(name: keyof typeof flags, allowed: readonly T[]): T => {
    const v = str(name) ?? "";
    if (!(allowed as readonly string[]).includes(v)) {
      throw new ConfigError(`--${name}: must be one of ${allowed.join(", ")}, got ${JSON.stringify(v)}`);
    }
    return v as T;
  };

  return {
    protocol: oneOf("protocol", ["homekit", "matter"] as const),
    listen: parseListen(str("listen") ?? ""),
    metricsListen: parseListen(str("metrics-listen") ?? ""),
    coreUrl: str("core-url") ?? "",
    pkiDir: str("pki-dir") ?? "",
    advertiseAddress: str("advertise-address") ?? env.POD_IP ?? "",
    instance: env.POD_NAME || hostname(),
    tvName: str("tv-name") ?? "",
    hapPortTv: port("hap-port-tv"),
    hapPortBridge: port("hap-port-bridge"),
    hapBind: (str("hap-bind") ?? "").split(",").map((s) => s.trim()).filter(Boolean),
    logLevel: str("log-level") ?? "info",
  };
}

export function parseListen(addr: string): { host: string; port: number } {
  const i = addr.lastIndexOf(":");
  if (i < 0) throw new ConfigError(`--listen: expected [host]:port, got ${JSON.stringify(addr)}`);
  const host = addr.slice(0, i).replace(/^\[(.*)\]$/, "$1");
  const port = Number(addr.slice(i + 1));
  if (!Number.isInteger(port) || port < 0 || port > 65535) throw new ConfigError(`--listen: invalid port in ${addr}`);
  return { host, port };
}

/**
 * The HomeService URL to register with vigo-core. vigo-home runs with hostNetwork, so POD_IP is
 * the node's address; outside Kubernetes the first non-loopback IPv4 address is used.
 */
export function homeEndpoint(cfg: Config, interfaces = networkInterfaces()): string {
  let addr = cfg.advertiseAddress;
  if (!addr) {
    addr = Object.values(interfaces)
      .flat()
      .find((i) => i && i.family === "IPv4" && !i.internal)?.address ?? "";
    if (!addr) throw new ConfigError("cannot determine an address to register: set --advertise-address or POD_IP");
  }
  const host = addr.includes(":") ? `[${addr}]` : addr;
  return `https://${host}:${cfg.listen.port}`;
}
