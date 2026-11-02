import { createHash, createPrivateKey, X509Certificate } from "node:crypto";
import { EventEmitter } from "node:events";
import { readFileSync, watch, type FSWatcher } from "node:fs";
import { join } from "node:path";
import { errMsg, type Logger } from "../log";

/** The identity vigo-core presents, as a client to :8081 and as the server on :8443. */
export const CORE_IDENTITY = "spiffe://vigo/core";

export interface PkiMaterial {
  cert: Buffer;
  key: Buffer;
  ca: Buffer;
  notAfter: Date;
  /** URI SANs of our own certificate. */
  uris: string[];
}

/**
 * Lists the URI entries of a subjectAltName as Node formats it ("URI:a, DNS:b"). Node quotes an
 * entry as a JSON string when it contains characters that would make the list ambiguous.
 */
export function uriSans(subjectAltName: string | undefined): string[] {
  if (!subjectAltName) return [];
  const out: string[] = [];
  const re = /(\w[\w ]*):("(?:[^"\\]|\\.)*"|[^,]*)(?:, |$)/g;
  for (const m of subjectAltName.matchAll(re)) {
    if (m[1] !== "URI") continue;
    const raw = m[2] ?? "";
    out.push(raw.startsWith('"') ? (JSON.parse(raw) as string) : raw);
  }
  return out;
}

export function hasIdentity(subjectAltName: string | undefined, identity: string): boolean {
  return uriSans(subjectAltName).includes(identity);
}

/** Reads and validates tls.crt, tls.key and ca.crt. Throws if they are missing or do not match. */
export function readPki(dir: string): PkiMaterial {
  const cert = readFileSync(join(dir, "tls.crt"));
  const key = readFileSync(join(dir, "tls.key"));
  const ca = readFileSync(join(dir, "ca.crt"));
  const x509 = new X509Certificate(cert);
  if (!x509.checkPrivateKey(createPrivateKey(key))) throw new Error("tls.key does not match tls.crt");
  new X509Certificate(ca);
  return { cert, key, ca, notAfter: new Date(x509.validTo), uris: uriSans(x509.subjectAltName) };
}

export interface PkiEvents {
  change: [material: PkiMaterial];
}

/**
 * The current certificate material of --pki-dir. cert-manager replaces the files by swapping a
 * symlink, so the directory is watched and also rescanned periodically; a change is only taken
 * once all three files are consistent again.
 */
export class Pki extends EventEmitter<PkiEvents> {
  private material: PkiMaterial;
  private fingerprint: string;
  private watcher?: FSWatcher;
  private poll?: NodeJS.Timeout;
  private debounce?: NodeJS.Timeout;

  constructor(
    readonly dir: string,
    private readonly log: Logger,
    private readonly pollMs = 30_000,
  ) {
    super();
    this.material = readPki(dir);
    this.fingerprint = fingerprint(this.material);
  }

  get current(): PkiMaterial {
    return this.material;
  }

  start(): void {
    try {
      this.watcher = watch(this.dir, () => {
        clearTimeout(this.debounce);
        this.debounce = setTimeout(() => this.reload(), 200);
      });
      this.watcher.on("error", (err) => this.log.warn({ err: errMsg(err) }, "watching the PKI directory failed"));
    } catch (err) {
      this.log.warn({ err: errMsg(err) }, "cannot watch the PKI directory; relying on periodic rescans");
    }
    this.poll = setInterval(() => this.reload(), this.pollMs);
    this.poll.unref();
  }

  stop(): void {
    this.watcher?.close();
    clearTimeout(this.debounce);
    clearInterval(this.poll);
  }

  /** Re-reads the directory; emits "change" if the material changed and is valid. */
  reload(): boolean {
    let next: PkiMaterial;
    try {
      next = readPki(this.dir);
    } catch (err) {
      this.log.warn({ err: errMsg(err) }, "PKI directory is not consistent; keeping the current certificate");
      return false;
    }
    const fp = fingerprint(next);
    if (fp === this.fingerprint) return false;
    this.material = next;
    this.fingerprint = fp;
    this.log.info({ notAfter: next.notAfter.toISOString(), uris: next.uris }, "reloaded TLS certificate");
    this.emit("change", next);
    return true;
  }
}

function fingerprint(m: PkiMaterial): string {
  return createHash("sha256").update(m.cert).update(m.key).update(m.ca).digest("hex");
}
