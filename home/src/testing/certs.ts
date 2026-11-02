import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

/** A throwaway CA that issues EC P-256 leaves with a URI SAN, using the openssl CLI. */
export class TestCA {
  readonly dir = mkdtempSync(join(tmpdir(), "vigo-pki-"));
  private serial = 1;
  private readonly caKey: string;

  constructor(name = "test-ca") {
    this.openssl(
      "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
      "-keyout", this.path(`${name}.key`), "-out", this.path("ca.crt"), "-days", "2", "-subj", `/CN=${name}`,
      "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign",
    );
    this.caKey = this.path(`${name}.key`);
  }

  /** Issues a leaf and writes tls.crt, tls.key and ca.crt into `dir` (created if needed). */
  issue(uri: string, dir = mkdtempSync(join(tmpdir(), "vigo-leaf-"))): string {
    mkdirSync(dir, { recursive: true });
    const id = this.serial++;
    const key = this.path(`leaf${id}.key`);
    const csr = this.path(`leaf${id}.csr`);
    const ext = this.path(`leaf${id}.ext`);
    writeFileSync(ext, `subjectAltName=URI:${uri}\nextendedKeyUsage=serverAuth,clientAuth\nbasicConstraints=CA:FALSE\n`);
    this.openssl("req", "-new", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes", "-keyout", key, "-out", csr, "-subj", `/CN=leaf${id}`);
    this.openssl(
      "x509", "-req", "-in", csr, "-CA", this.path("ca.crt"), "-CAkey", this.caKey,
      "-set_serial", String(1000 + id), "-days", "1", "-extfile", ext, "-out", join(dir, "tls.crt"),
    );
    copyFileSync(key, join(dir, "tls.key"));
    copyFileSync(this.path("ca.crt"), join(dir, "ca.crt"));
    return dir;
  }

  private path(name: string): string {
    return join(this.dir, name);
  }

  private openssl(...args: string[]): void {
    execFileSync("openssl", args, { stdio: "pipe" });
  }
}
