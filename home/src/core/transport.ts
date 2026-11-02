import type { PeerCertificate } from "node:tls";
import type { Transport } from "@connectrpc/connect";
import { createGrpcTransport } from "@connectrpc/connect-node";
import { CORE_IDENTITY, hasIdentity, type Pki, type PkiMaterial } from "../pki/pki";

/** Accepts the peer only if it carries vigo-core's URI SAN. The chain is still verified against ca.crt. */
export function checkCoreIdentity(_host: string, cert: PeerCertificate): Error | undefined {
  if (hasIdentity(cert.subjectaltname, CORE_IDENTITY)) return undefined;
  return new Error(`peer certificate does not carry ${CORE_IDENTITY} (subjectAltName: ${cert.subjectaltname ?? "none"})`);
}

export function grpcTransport(baseUrl: string, m: PkiMaterial): Transport {
  return createGrpcTransport({
    baseUrl,
    pingIntervalMs: 30_000,
    pingTimeoutMs: 10_000,
    nodeOptions: {
      cert: m.cert,
      key: m.key,
      ca: m.ca,
      // Peers are authorised by URI SAN, never by host name.
      checkServerIdentity: checkCoreIdentity,
    },
  });
}

/**
 * A gRPC-over-mTLS transport to vigo-core that picks up renewed certificates: each change of the
 * PKI builds a fresh transport (and with it a fresh HTTP/2 session) for new calls. Streams that
 * are already open keep their connection.
 */
export function coreTransport(baseUrl: string, pki: Pki, build = grpcTransport): Transport {
  let current = build(baseUrl, pki.current);
  pki.on("change", (m) => {
    current = build(baseUrl, m);
  });
  return {
    unary: (...args) => current.unary(...args),
    stream: (...args) => current.stream(...args),
  };
}
