import { createClient, type Client, type Transport } from "@connectrpc/connect";
import {
  IntegrationService,
  Role,
  type AcquireLeadershipResponse,
} from "../gen/vigo/integration/v1/integration_pb";
import { errMsg, type Logger } from "../log";
import { sleep } from "../sleep";

export interface Leader {
  /** Ties Register and state writes to this leadership. */
  lease: string;
  /** Resolves, with the reason, when leadership ends. It never comes back on the same stream. */
  lost: Promise<string>;
}

export interface LeadershipOptions {
  minBackoffMs?: number;
  maxBackoffMs?: number;
}

/**
 * Holds IntegrationService.AcquireLeadership. vigo-core backs the stream with a Postgres advisory
 * lock, so this replica is leader exactly while the stream it got LEADER on stays open.
 */
export class Leadership {
  private readonly client: Client<typeof IntegrationService>;
  private readonly minBackoffMs: number;
  private readonly maxBackoffMs: number;

  constructor(
    transport: Transport,
    private readonly instance: string,
    private readonly log: Logger,
    opts: LeadershipOptions = {},
  ) {
    this.client = createClient(IntegrationService, transport);
    this.minBackoffMs = opts.minBackoffMs ?? 1000;
    this.maxBackoffMs = opts.maxBackoffMs ?? 30_000;
  }

  /**
   * Waits, as a standby, until this replica becomes leader. Reconnects with backoff while
   * vigo-core is unreachable. Resolves undefined if aborted first; aborting after it resolved
   * closes the stream and so gives leadership up.
   */
  async acquire(signal: AbortSignal): Promise<Leader | undefined> {
    let backoff = this.minBackoffMs;
    while (!signal.aborted) {
      try {
        const it = this.client.acquireLeadership({ instance: this.instance }, { signal })[Symbol.asyncIterator]();
        for (;;) {
          const next = await it.next();
          if (next.done) break;
          const msg: AcquireLeadershipResponse = next.value;
          backoff = this.minBackoffMs;
          if (msg.role === Role.LEADER) {
            if (!msg.lease) throw new Error("LEADER without a lease");
            this.log.info({ instance: this.instance }, "became leader");
            return { lease: msg.lease, lost: this.watch(it) };
          }
          if (msg.role === Role.STANDBY) this.log.info({ instance: this.instance }, "standing by");
        }
        if (!signal.aborted) this.log.warn("leadership stream ended before becoming leader");
      } catch (err) {
        if (signal.aborted) break;
        this.log.warn({ err: errMsg(err), retryInMs: backoff }, "leadership stream failed");
      }
      await sleep(backoff * (0.8 + Math.random() * 0.4), signal);
      backoff = Math.min(backoff * 2, this.maxBackoffMs);
    }
    return undefined;
  }

  private async watch(it: AsyncIterator<AcquireLeadershipResponse>): Promise<string> {
    try {
      for (;;) {
        const next = await it.next();
        if (next.done) return "leadership stream ended";
        if (next.value.role !== Role.LEADER) return `leadership stream reported role ${Role[next.value.role] ?? next.value.role}`;
      }
    } catch (err) {
      return `leadership stream failed: ${errMsg(err)}`;
    }
  }
}
