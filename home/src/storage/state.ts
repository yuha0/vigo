import { Code, ConnectError, createClient, type Client, type Transport } from "@connectrpc/connect";
import { IntegrationService } from "../gen/vigo/integration/v1/integration_pb";
import { errMsg, type Logger } from "../log";
import { isStateKey } from "./keys";

export interface StateOptions {
  minBackoffMs?: number;
  maxBackoffMs?: number;
  callTimeoutMs?: number;
}

interface KeyState {
  /** Latest value set locally; null is a deletion. */
  desired: Uint8Array | null;
  /** Bumped on every local change. */
  generation: number;
  /** Generation that vigo-core has confirmed. */
  written: number;
  running: boolean;
  waiters: { generation: number; resolve: () => void; reject: (err: unknown) => void }[];
}

/**
 * vigo-home's state, held by vigo-core (IntegrationService ListState/PutState/DeleteState).
 * Reads and writes are synchronous against memory; every change is written through in the
 * background, in order per key, retried until vigo-core accepts it. Only the latest value of a
 * key is sent, so a burst of writes to one key collapses into one call.
 */
export class StateStore {
  private readonly client: Client<typeof IntegrationService>;
  private readonly values = new Map<string, Uint8Array>();
  private readonly keys = new Map<string, KeyState>();
  private readonly opts: Required<StateOptions>;
  private closed = false;

  private constructor(
    transport: Transport,
    private readonly lease: string,
    private readonly log: Logger,
    opts: StateOptions,
  ) {
    this.client = createClient(IntegrationService, transport);
    this.opts = {
      minBackoffMs: opts.minBackoffMs ?? 250,
      maxBackoffMs: opts.maxBackoffMs ?? 10_000,
      callTimeoutMs: opts.callTimeoutMs ?? 15_000,
    };
  }

  /** Loads everything vigo-core holds for this integration. */
  static async load(transport: Transport, lease: string, log: Logger, opts: StateOptions = {}): Promise<StateStore> {
    const store = new StateStore(transport, lease, log, opts);
    const res = await store.client.listState({}, { timeoutMs: store.opts.callTimeoutMs });
    for (const item of res.items) store.values.set(item.key, item.value);
    log.info({ items: res.items.length }, "loaded state from vigo-core");
    return store;
  }

  get(key: string): Uint8Array | undefined {
    return this.values.get(key);
  }

  entries(): IterableIterator<[string, Uint8Array]> {
    return this.values.entries();
  }

  /** Sets a value; resolves once vigo-core has stored it (or a later value of the same key). */
  set(key: string, value: Uint8Array): Promise<void> {
    this.check(key);
    this.values.set(key, value);
    return this.enqueue(key, value);
  }

  delete(key: string): Promise<void> {
    this.check(key);
    this.values.delete(key);
    return this.enqueue(key, null);
  }

  /** Waits until every change so far is stored, or the timeout passes. Returns whether it finished. */
  async flush(timeoutMs = 10_000): Promise<boolean> {
    const pending = [...this.keys.entries()]
      .filter(([, k]) => k.written < k.generation)
      .map(([key, k]) => new Promise<void>((resolve) => k.waiters.push({ generation: k.generation, resolve, reject: () => resolve() })).then(() => key));
    if (pending.length === 0) return true;
    let timer: NodeJS.Timeout | undefined;
    const timeout = new Promise<false>((resolve) => (timer = setTimeout(() => resolve(false), timeoutMs)));
    try {
      return await Promise.race([Promise.all(pending).then(() => true as const), timeout]);
    } finally {
      clearTimeout(timer);
    }
  }

  /** Stops writing through: pending and later changes stay in memory only. */
  close(): void {
    this.closed = true;
  }

  private check(key: string): void {
    if (!isStateKey(key)) throw new Error(`invalid state key ${JSON.stringify(key)}`);
  }

  private enqueue(key: string, desired: Uint8Array | null): Promise<void> {
    if (this.closed) {
      this.log.warn({ key }, "state changed after the store was closed; not persisted");
      return Promise.resolve();
    }
    let k = this.keys.get(key);
    if (!k) {
      k = { desired, generation: 0, written: 0, running: false, waiters: [] };
      this.keys.set(key, k);
    }
    k.desired = desired;
    k.generation++;
    const done = new Promise<void>((resolve, reject) => k.waiters.push({ generation: k.generation, resolve, reject }));
    if (!k.running) void this.drain(key, k);
    return done;
  }

  private async drain(key: string, k: KeyState): Promise<void> {
    k.running = true;
    let backoff = this.opts.minBackoffMs;
    while (k.written < k.generation && !this.closed) {
      const generation = k.generation;
      const value = k.desired;
      try {
        if (value === null) await this.client.deleteState({ lease: this.lease, key }, { timeoutMs: this.opts.callTimeoutMs });
        else await this.client.putState({ lease: this.lease, key, value }, { timeoutMs: this.opts.callTimeoutMs });
        k.written = generation;
        backoff = this.opts.minBackoffMs;
        this.settle(k, (w) => w.generation <= generation, (w) => w.resolve());
      } catch (err) {
        if (ConnectError.from(err).code === Code.InvalidArgument) {
          this.log.error({ key, err: errMsg(err) }, "vigo-core rejected a state write; dropping it");
          k.written = generation;
          this.settle(k, (w) => w.generation <= generation, (w) => w.reject(err));
          continue;
        }
        this.log.warn({ key, err: errMsg(err), retryInMs: backoff }, "state write failed; retrying");
        await new Promise((r) => setTimeout(r, backoff));
        backoff = Math.min(backoff * 2, this.opts.maxBackoffMs);
      }
    }
    k.running = false;
  }

  private settle(k: KeyState, match: (w: KeyState["waiters"][number]) => boolean, fn: (w: KeyState["waiters"][number]) => void): void {
    const ready = k.waiters.filter(match);
    k.waiters = k.waiters.filter((w) => !match(w));
    for (const w of ready) fn(w);
  }
}
