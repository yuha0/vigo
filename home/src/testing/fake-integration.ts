import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport, type Transport } from "@connectrpc/connect";
import {
  IntegrationService,
  Role,
  StateItemSchema,
  type StateItem,
} from "../gen/vigo/integration/v1/integration_pb";

interface RoleMsg {
  role: Role;
  lease?: string;
}

/** An in-memory vigo-core IntegrationService for tests. */
export class FakeIntegration {
  readonly items = new Map<string, StateItem>();
  readonly calls: { method: string; key?: string; lease?: string }[] = [];
  registered?: { lease: string; endpoint: string };
  lease = "lease-1";
  /** Calls fail with this until cleared. */
  fail?: ConnectError;
  /** Remaining PutState/DeleteState calls to fail with unavailable. */
  flakyWrites = 0;
  /** Set to hold every write until released. */
  gate?: Promise<void>;
  /** Pushes the next role to the open leadership stream; ends it on null. */
  private roles: ((r: RoleMsg | null) => void)[] = [];
  streams = 0;
  /** Leadership streams opened so far. */
  opened = 0;

  readonly transport: Transport = createRouterTransport(({ service }) => {
    service(IntegrationService, {
      acquireLeadership: (req, ctx) => this.leadership(ctx.signal),
      register: (req) => {
        this.check("register");
        if (req.lease !== this.lease) throw new ConnectError("not leader", Code.FailedPrecondition);
        this.registered = { lease: req.lease, endpoint: req.endpoint };
        return {};
      },
      listState: () => {
        this.check("listState");
        return { items: [...this.items.values()] };
      },
      putState: async (req) => {
        await this.write("putState", req.key, req.lease);
        const version = (this.items.get(req.key)?.version ?? 0n) + 1n;
        this.items.set(req.key, create(StateItemSchema, { key: req.key, value: req.value, version }));
        return { version };
      },
      deleteState: async (req) => {
        await this.write("deleteState", req.key, req.lease);
        this.items.delete(req.key);
        return {};
      },
    });
  });

  /** Sends a role on every open leadership stream. */
  send(role: Role): void {
    for (const push of this.roles) push({ role, lease: role === Role.LEADER ? this.lease : "" });
  }

  endStreams(): void {
    for (const push of this.roles) push(null);
  }

  value(key: string): string | undefined {
    const v = this.items.get(key)?.value;
    return v && Buffer.from(v).toString("utf8");
  }

  private check(method: string): void {
    if (this.fail) throw this.fail;
    this.calls.push({ method });
  }

  private async write(method: string, key: string, lease: string): Promise<void> {
    if (this.fail) throw this.fail;
    await this.gate;
    if (lease !== this.lease) throw new ConnectError("stale lease", Code.FailedPrecondition);
    if (this.flakyWrites > 0) {
      this.flakyWrites--;
      throw new ConnectError("flaky", Code.Unavailable);
    }
    this.calls.push({ method, key, lease });
  }

  private async *leadership(signal: AbortSignal): AsyncGenerator<RoleMsg> {
    if (this.fail) throw this.fail;
    const queue: (RoleMsg | null)[] = [];
    let wake: (() => void) | undefined;
    const push = (r: RoleMsg | null) => {
      queue.push(r);
      wake?.();
    };
    this.roles.push(push);
    signal.addEventListener("abort", () => push(null), { once: true });
    this.streams++;
    this.opened++;
    try {
      yield { role: Role.STANDBY };
      for (;;) {
        if (queue.length === 0) await new Promise<void>((r) => (wake = r));
        wake = undefined;
        const next = queue.shift();
        if (next === null || next === undefined) return;
        yield next;
      }
    } finally {
      this.streams--;
      this.roles = this.roles.filter((p) => p !== push);
    }
  }
}
