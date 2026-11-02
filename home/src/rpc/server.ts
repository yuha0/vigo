import { createServer as createHttpServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { createServer as createHttpsServer, type Server as HttpsServer } from "node:https";
import type { TLSSocket } from "node:tls";
import { connectNodeAdapter } from "@connectrpc/connect-node";
import { errMsg, type Logger } from "../log";
import type { Metrics } from "../metrics";
import { CORE_IDENTITY, hasIdentity, type Pki } from "../pki/pki";
import { homeRoutes, type HomeRuntime } from "./home-service";

/**
 * HomeService (Connect, gRPC and gRPC-Web over HTTP/1.1) behind mutual TLS. Clients must chain
 * to ca.crt and carry vigo-core's URI SAN; vigo-core proxies browser calls here.
 */
export function createHomeServer(opts: { pki: Pki; runtime: HomeRuntime; log: Logger }): HttpsServer {
  const rpc = connectNodeAdapter({ routes: homeRoutes(opts.runtime) });
  const m = opts.pki.current;
  const server = createHttpsServer(
    {
      cert: m.cert,
      key: m.key,
      ca: m.ca,
      requestCert: true,
      rejectUnauthorized: true,
      minVersion: "TLSv1.2",
    },
    (req, res) => {
      const peer = (req.socket as TLSSocket).getPeerCertificate();
      if (!hasIdentity(peer.subjectaltname, CORE_IDENTITY)) {
        opts.log.warn({ subjectAltName: peer.subjectaltname }, "rejected a client without the vigo-core identity");
        res.writeHead(403, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ code: "permission_denied", message: `client is not ${CORE_IDENTITY}` }));
        return;
      }
      rpc(req, res);
    },
  );
  opts.pki.on("change", (next) => server.setSecureContext({ cert: next.cert, key: next.key, ca: next.ca }));
  return server;
}

/** /healthz, /readyz and /metrics on plain HTTP, so probes and Prometheus need no certificate. */
export function createOpsServer(opts: { metrics: Metrics; log: Logger; ready(): boolean }): Server {
  return createHttpServer((req: IncomingMessage, res: ServerResponse) => {
    const path = (req.url ?? "/").split("?")[0];
    if (req.method !== "GET" && req.method !== "HEAD") return text(res, 405, "method not allowed\n");
    switch (path) {
      case "/healthz":
        return text(res, 200, "ok\n");
      case "/readyz":
        return opts.ready() ? text(res, 200, "ready\n") : text(res, 503, "not ready\n");
      case "/metrics":
        opts.metrics.registry.metrics().then(
          (body) => {
            res.writeHead(200, { "Content-Type": opts.metrics.registry.contentType });
            res.end(body);
          },
          (err: unknown) => {
            opts.log.error({ err: errMsg(err) }, "rendering metrics failed");
            text(res, 500, "metrics unavailable\n");
          },
        );
        return;
      default:
        return text(res, 404, "not found\n");
    }
  });
}

function text(res: ServerResponse, status: number, body: string): void {
  res.writeHead(status, { "Content-Type": "text/plain; charset=utf-8" });
  res.end(body);
}

export function listen(server: Server | HttpsServer, host: string, port: number): Promise<void> {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, host || undefined, () => {
      server.off("error", reject);
      resolve();
    });
  });
}
