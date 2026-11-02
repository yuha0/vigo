import { X509Certificate } from "node:crypto";
import { copyFileSync, readFileSync, writeFileSync } from "node:fs";
import { createSecureServer, type Http2SecureServer } from "node:http2";
import { request } from "node:https";
import type { AddressInfo } from "node:net";
import type { TLSSocket } from "node:tls";
import { join } from "node:path";
import { createClient } from "@connectrpc/connect";
import { connectNodeAdapter } from "@connectrpc/connect-node";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { coreTransport } from "../core/transport";
import { TVService } from "../gen/vigo/core/v1/core_pb";
import { silentLogger } from "../log";
import { Metrics } from "../metrics";
import type { HomeRuntime } from "../rpc/home-service";
import { createHomeServer, listen } from "../rpc/server";
import { TestCA } from "../testing/certs";
import { CORE_IDENTITY, hasIdentity, Pki, readPki, uriSans } from "./pki";

const HOME_IDENTITY = "spiffe://vigo/integration/homekit";

let ca: TestCA;
let otherCA: TestCA;
let homeDir: string;
let coreDir: string;
let wrongSanDir: string;
let foreignCoreDir: string;

beforeAll(() => {
  ca = new TestCA();
  otherCA = new TestCA("other-ca");
  homeDir = ca.issue(HOME_IDENTITY);
  coreDir = ca.issue(CORE_IDENTITY);
  wrongSanDir = ca.issue("spiffe://vigo/integration/other");
  foreignCoreDir = otherCA.issue(CORE_IDENTITY);
});

const cleanup: (() => void)[] = [];
afterEach(() => {
  for (const fn of cleanup.splice(0)) fn();
});

const runtime: HomeRuntime = {
  protocol: "homekit",
  implemented: true,
  replica: "pod-a",
  provider: () => undefined,
  leader: () => true,
  coreConnected: () => true,
};

async function startHome(dir: string) {
  const pki = new Pki(dir, silentLogger());
  const server = createHomeServer({ pki, runtime, log: silentLogger() });
  await listen(server, "127.0.0.1", 0);
  cleanup.push(() => server.close());
  return { pki, server, port: (server.address() as AddressInfo).port };
}

/** POSTs GetProvider over HTTPS with the client material in `dir` (or none). */
function call(port: number, dir?: string): Promise<{ status: number; body: string; serverCert: string }> {
  const m = dir ? readPki(dir) : undefined;
  return new Promise((resolve, reject) => {
    const req = request(
      {
        host: "127.0.0.1",
        port,
        method: "POST",
        path: "/vigo.home.v1.HomeService/GetProvider",
        headers: { "Content-Type": "application/json" },
        ca: readFileSync(join(homeDir, "ca.crt")),
        cert: m?.cert,
        key: m?.key,
        checkServerIdentity: () => undefined,
        agent: false,
      },
      (res) => {
        let body = "";
        const serverCert = (res.socket as TLSSocket).getPeerCertificate().fingerprint256;
        res.on("data", (c: Buffer) => (body += c.toString()));
        res.on("end", () => resolve({ status: res.statusCode ?? 0, body, serverCert }));
      },
    );
    req.on("error", reject);
    req.end("{}");
  });
}

describe("PKI", () => {
  it("reads URI SANs", () => {
    expect(uriSans("URI:spiffe://vigo/core, DNS:vigo-core")).toEqual(["spiffe://vigo/core"]);
    expect(uriSans('DNS:a, URI:"spiffe://vigo/x,y"')).toEqual(["spiffe://vigo/x,y"]);
    expect(uriSans(undefined)).toEqual([]);
    expect(hasIdentity("URI:spiffe://vigo/core-not", CORE_IDENTITY)).toBe(false);
    expect(readPki(homeDir).uris).toEqual([HOME_IDENTITY]);
  });

  it("refuses a key that does not match the certificate", () => {
    const dir = ca.issue(HOME_IDENTITY);
    copyFileSync(join(coreDir, "tls.key"), join(dir, "tls.key"));
    expect(() => readPki(dir)).toThrow(/does not match/);
  });

  it("reports certificate expiry", async () => {
    const pki = new Pki(homeDir, silentLogger());
    const metrics = new Metrics({ defaultMetrics: false });
    metrics.trackCertificate(() => pki.current.notAfter);
    const values = (await metrics.certExpiry.get()).values;
    expect(values.map((v) => v.labels.listener).sort()).toEqual(["client", "server"]);
    for (const v of values) {
      expect(v.value).toBeGreaterThan(3600);
      expect(v.value).toBeLessThanOrEqual(86_400 + 60);
    }
  });
});

describe("HomeService over mTLS", () => {
  it("accepts vigo-core's certificate", async () => {
    const { port } = await startHome(homeDir);
    const res = await call(port, coreDir);
    expect(res.status).toBe(200);
    expect(JSON.parse(res.body)).toMatchObject({ provider: { replica: "pod-a" } });
  });

  it("rejects a certificate with another URI SAN", async () => {
    const { port } = await startHome(homeDir);
    const res = await call(port, wrongSanDir);
    expect(res.status).toBe(403);
    expect(JSON.parse(res.body)).toMatchObject({ code: "permission_denied" });
  });

  it("rejects clients without a certificate or from another CA", async () => {
    const { port } = await startHome(homeDir);
    await expect(call(port)).rejects.toThrow();
    await expect(call(port, foreignCoreDir)).rejects.toThrow();
  });

  it("serves a renewed certificate after reload", async () => {
    const dir = ca.issue(HOME_IDENTITY);
    const { pki, port } = await startHome(dir);
    const before = (await call(port, coreDir)).serverCert;
    expect(before).toBe(new X509Certificate(readFileSync(join(dir, "tls.crt"))).fingerprint256);

    ca.issue(HOME_IDENTITY, dir);
    expect(pki.reload()).toBe(true);
    const after = (await call(port, coreDir)).serverCert;
    expect(after).not.toBe(before);
    expect(after).toBe(new X509Certificate(readFileSync(join(dir, "tls.crt"))).fingerprint256);
  });

  it("keeps the current certificate while the directory is inconsistent", () => {
    const dir = ca.issue(HOME_IDENTITY);
    const pki = new Pki(dir, silentLogger());
    writeFileSync(join(dir, "tls.key"), "garbage");
    expect(pki.reload()).toBe(false);
    expect(pki.current.uris).toEqual([HOME_IDENTITY]);
  });
});

/** A stand-in vigo-core serving TVService.GetStatus over HTTP/2 + mTLS, recording client certs. */
async function startCore(dir: string) {
  const m = readPki(dir);
  const clients: string[] = [];
  const handler = connectNodeAdapter({
    routes: (router) => {
      router.service(TVService, {
        getStatus: (_req, ctx) => {
          clients.push(ctx.requestHeader.get("x-client-cert") ?? "");
          return {};
        },
      });
    },
  });
  const server: Http2SecureServer = createSecureServer(
    { cert: m.cert, key: m.key, ca: m.ca, requestCert: true, rejectUnauthorized: true, allowHTTP1: false },
    (req, res) => {
      req.headers["x-client-cert"] = (req.socket as TLSSocket).getPeerCertificate().fingerprint256;
      handler(req, res);
    },
  );
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  cleanup.push(() => server.close());
  return { url: `https://127.0.0.1:${(server.address() as AddressInfo).port}`, clients };
}

describe("client transport to vigo-core", () => {
  it("calls vigo-core when it presents spiffe://vigo/core, regardless of host name", async () => {
    const core = await startCore(coreDir);
    const client = createClient(TVService, coreTransport(core.url, new Pki(homeDir, silentLogger())));
    await client.getStatus({});
    expect(core.clients).toEqual([new X509Certificate(readFileSync(join(homeDir, "tls.crt"))).fingerprint256]);
  });

  it("refuses a server without vigo-core's identity", async () => {
    const impostor = await startCore(wrongSanDir);
    const client = createClient(TVService, coreTransport(impostor.url, new Pki(homeDir, silentLogger())));
    await expect(client.getStatus({})).rejects.toThrow(/spiffe:\/\/vigo\/core/);
    expect(impostor.clients).toEqual([]);
  });

  it("presents the renewed client certificate after reload", async () => {
    const dir = ca.issue(HOME_IDENTITY);
    const core = await startCore(coreDir);
    const pki = new Pki(dir, silentLogger());
    const client = createClient(TVService, coreTransport(core.url, pki));
    await client.getStatus({});
    ca.issue(HOME_IDENTITY, dir);
    expect(pki.reload()).toBe(true);
    await client.getStatus({});
    const fp = new X509Certificate(readFileSync(join(dir, "tls.crt"))).fingerprint256;
    expect(core.clients).toHaveLength(2);
    expect(core.clients[0]).not.toBe(fp);
    expect(core.clients[1]).toBe(fp);
  });
});
