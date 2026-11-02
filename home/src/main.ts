import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { homeEndpoint, parseConfig, usage, type Config } from "./config";
import { Core } from "./core/core";
import { coreTransport } from "./core/transport";
import { IntegrationService } from "./gen/vigo/integration/v1/integration_pb";
import { HomeKitProvider } from "./homekit/provider";
import { Leadership } from "./leadership/leadership";
import { createLogger, errMsg, type Logger } from "./log";
import { MatterProvider } from "./matter/provider";
import { Metrics } from "./metrics";
import { Pki } from "./pki/pki";
import type { Provider } from "./provider";
import type { HomeRuntime } from "./rpc/home-service";
import { createHomeServer, createOpsServer, listen } from "./rpc/server";
import { sleep } from "./sleep";
import { HapStorage, installHapStorage } from "./storage/hap";
import { homeStore } from "./storage/home";
import { StateStore } from "./storage/state";

const SHUTDOWN_TIMEOUT_MS = 10_000;
const STATE_FLUSH_TIMEOUT_MS = 5_000;

async function main(): Promise<void> {
  let cfg: Config;
  let endpoint: string;
  try {
    const parsed = parseConfig(process.argv.slice(2), process.env);
    if (parsed === "help") {
      console.log(usage());
      return;
    }
    cfg = parsed;
    endpoint = homeEndpoint(cfg);
  } catch (err) {
    console.error(`${errMsg(err)}\n\n${usage()}`);
    process.exit(2);
  }

  const log = createLogger(cfg.logLevel);
  const metrics = new Metrics();
  log.info(
    { protocol: cfg.protocol, coreUrl: cfg.coreUrl, pkiDir: cfg.pkiDir, endpoint, instance: cfg.instance },
    "starting vigo-home",
  );

  let pki: Pki;
  try {
    pki = new Pki(cfg.pkiDir, log.child({ component: "pki" }));
  } catch (err) {
    log.fatal({ pkiDir: cfg.pkiDir, err: errMsg(err) }, "cannot load the TLS certificate");
    process.exit(2);
  }
  log.info({ uris: pki.current.uris, notAfter: pki.current.notAfter.toISOString() }, "loaded TLS certificate");
  pki.start();
  metrics.trackCertificate(() => pki.current.notAfter);
  metrics.leader.set(0);
  metrics.coreConnected.set(0);

  const transport = coreTransport(cfg.coreUrl, pki);
  let ready = false;
  let leading = false;
  const ops = createOpsServer({ metrics, log, ready: () => ready || !leading });
  await listen(ops, cfg.metricsListen.host, cfg.metricsListen.port);

  const lifetime = new AbortController();
  let shuttingDown = false;
  // Replaced once this replica is leader.
  let shutdown = (signal: string): Promise<void> => {
    log.info({ signal }, "shutting down");
    lifetime.abort();
    pki.stop();
    return close(ops);
  };
  process.on("SIGTERM", () => void shutdown("SIGTERM"));
  process.on("SIGINT", () => void shutdown("SIGINT"));

  const leader = await new Leadership(transport, cfg.instance, log.child({ component: "leadership" })).acquire(lifetime.signal);
  if (!leader) return;
  leading = true;
  metrics.leader.set(1);

  // State must be in place before any HAP object exists: HAP reads its pairings while constructing.
  const state = await StateStore.load(transport, leader.lease, log.child({ component: "state" }));
  installHapStorage(new HapStorage(state, log.child({ component: "hap-storage" })));

  const core = new Core(transport, log.child({ peer: "vigo-core" }));
  core.on("connected", (c) => metrics.coreConnected.set(c ? 1 : 0));
  const provider: Provider =
    cfg.protocol === "homekit"
      ? new HomeKitProvider({
          core,
          store: homeStore(state),
          log,
          metrics,
          tvName: cfg.tvName,
          hapPortTv: cfg.hapPortTv,
          hapPortBridge: cfg.hapPortBridge,
          hapBind: cfg.hapBind,
        })
      : new MatterProvider();
  const runtime: HomeRuntime = {
    protocol: provider.protocol,
    implemented: provider.implemented,
    replica: cfg.instance,
    provider: () => (ready ? provider : undefined),
    leader: () => leading,
    coreConnected: () => core.connected,
  };
  const home = createHomeServer({ pki, runtime, log });

  const stopActive = async () => {
    ready = false;
    await provider.stop().catch((err: unknown) => log.error({ err: errMsg(err) }, "stopping provider failed"));
    await core.stop();
  };

  shutdown = async (signal: string) => {
    if (shuttingDown) return;
    shuttingDown = true;
    log.info({ signal }, "shutting down");
    setTimeout(() => {
      log.error("shutdown timed out");
      process.exit(1);
    }, SHUTDOWN_TIMEOUT_MS).unref();
    await stopActive();
    if (!(await state.flush(STATE_FLUSH_TIMEOUT_MS))) log.error("some state was not written to vigo-core before exit");
    state.close();
    lifetime.abort();
    await Promise.all([close(home), close(ops)]);
    pki.stop();
    log.info("stopped");
    process.exit(0);
  };

  void leader.lost.then(async (reason) => {
    if (shuttingDown) return;
    shuttingDown = true;
    log.error({ reason }, "lost leadership; exiting");
    state.close();
    await Promise.race([stopActive(), new Promise((r) => setTimeout(r, 3000).unref())]);
    process.exit(1);
  });

  try {
    core.start();
    await provider.start();
    await listen(home, cfg.listen.host, cfg.listen.port);
    await register(transport, leader.lease, endpoint, log, lifetime.signal);
    ready = true;
    log.info({ endpoint }, "active");
  } catch (err) {
    if (shuttingDown) return;
    log.fatal({ err: errMsg(err) }, "activation failed");
    await stopActive();
    await state.flush(STATE_FLUSH_TIMEOUT_MS);
    process.exit(1);
  }
}

async function register(transport: Transport, lease: string, endpoint: string, log: Logger, signal: AbortSignal): Promise<void> {
  const client = createClient(IntegrationService, transport);
  for (let attempt = 1; ; attempt++) {
    try {
      await client.register({ lease, endpoint }, { timeoutMs: 10_000, signal });
      log.info({ endpoint }, "registered with vigo-core");
      return;
    } catch (err) {
      const code = ConnectError.from(err).code;
      if (code === Code.FailedPrecondition || code === Code.PermissionDenied || attempt >= 5) throw err;
      log.warn({ err: errMsg(err), attempt }, "registering with vigo-core failed; retrying");
      await sleep(1000 * attempt, signal);
    }
  }
}

function close(server: { close(cb: () => void): unknown; closeAllConnections?: () => void }): Promise<void> {
  return new Promise((resolve) => {
    server.close(() => resolve());
    server.closeAllConnections?.();
  });
}

main().catch((err: unknown) => {
  console.error(err);
  process.exit(1);
});
