import { Counter, Gauge, Registry, collectDefaultMetrics } from "@prometheus-io/client";

export type WriteOutcome = "ok" | "error" | "ignored";

export class Metrics {
  readonly registry = new Registry();
  readonly leader: Gauge;
  readonly paired: Gauge<"group">;
  readonly coreConnected: Gauge;
  readonly characteristicWrites: Counter<"accessory" | "outcome">;
  readonly certExpiry: Gauge<"listener">;
  private certNotAfter?: () => Date;

  constructor(opts: { defaultMetrics?: boolean } = {}) {
    if (opts.defaultMetrics ?? true) collectDefaultMetrics({ register: this.registry });
    this.leader = new Gauge({
      name: "vigo_home_leader",
      help: "1 while this replica holds integration leadership.",
      registers: [this.registry],
    });
    this.paired = new Gauge({
      name: "vigo_home_paired",
      help: "1 if at least one controller is paired to the pairing group.",
      labelNames: ["group"],
      registers: [this.registry],
    });
    this.coreConnected = new Gauge({
      name: "vigo_home_core_connected",
      help: "1 while the WatchEvents stream to vigo-core is up.",
      registers: [this.registry],
    });
    this.characteristicWrites = new Counter({
      name: "vigo_home_characteristic_writes_total",
      help: "HomeKit characteristic writes by accessory and outcome.",
      labelNames: ["accessory", "outcome"],
      registers: [this.registry],
    });
    const certNotAfter = () => this.certNotAfter?.();
    this.certExpiry = new Gauge({
      name: "vigo_tls_cert_expiry_seconds",
      help: "Seconds until the certificate in use expires. listener: server is :8081, client is the connection to vigo-core.",
      labelNames: ["listener"],
      registers: [this.registry],
      collect() {
        const notAfter = certNotAfter();
        if (!notAfter) return;
        const remaining = (notAfter.getTime() - Date.now()) / 1000;
        this.set({ listener: "server" }, remaining);
        this.set({ listener: "client" }, remaining);
      },
    });
  }

  /** Both the server and the client side present the one certificate from --pki-dir. */
  trackCertificate(notAfter: () => Date): void {
    this.certNotAfter = notAfter;
  }

  recordWrite(accessory: string, outcome: WriteOutcome): void {
    this.characteristicWrites.inc({ accessory, outcome });
  }
}
