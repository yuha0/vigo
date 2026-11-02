import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { Role } from "../gen/vigo/integration/v1/integration_pb";
import { silentLogger } from "../log";
import { until } from "../testing/fake-core";
import { FakeIntegration } from "../testing/fake-integration";
import { Leadership } from "./leadership";

function leadership(fake: FakeIntegration) {
  return new Leadership(fake.transport, "pod-a", silentLogger(), { minBackoffMs: 5, maxBackoffMs: 10 });
}

describe("Leadership", () => {
  it("stays standby until LEADER arrives, then returns the lease", async () => {
    const fake = new FakeIntegration();
    let leader: Awaited<ReturnType<Leadership["acquire"]>>;
    const p = leadership(fake)
      .acquire(new AbortController().signal)
      .then((l) => (leader = l));
    await until(() => fake.streams === 1);
    await new Promise((r) => setTimeout(r, 20));
    expect(leader).toBeUndefined();
    fake.send(Role.LEADER);
    await p;
    expect(leader!.lease).toBe("lease-1");
  });

  it("reports loss when the stream ends after LEADER", async () => {
    const fake = new FakeIntegration();
    const p = leadership(fake).acquire(new AbortController().signal);
    await until(() => fake.streams === 1);
    fake.send(Role.LEADER);
    const leader = (await p)!;
    let reason: string | undefined;
    void leader.lost.then((r) => (reason = r));
    await new Promise((r) => setTimeout(r, 20));
    expect(reason).toBeUndefined();
    fake.endStreams();
    await until(() => reason !== undefined);
    expect(reason).toMatch(/ended/);
  });

  it("treats any non-LEADER role after LEADER as loss", async () => {
    const fake = new FakeIntegration();
    const p = leadership(fake).acquire(new AbortController().signal);
    await until(() => fake.streams === 1);
    fake.send(Role.LEADER);
    const leader = (await p)!;
    fake.send(Role.STANDBY);
    expect(await leader.lost).toMatch(/STANDBY/);
  });

  it("retries with backoff while vigo-core is unreachable", async () => {
    const fake = new FakeIntegration();
    fake.fail = new ConnectError("connection refused", Code.Unavailable);
    const p = leadership(fake).acquire(new AbortController().signal);
    await new Promise((r) => setTimeout(r, 50));
    fake.fail = undefined;
    await until(() => fake.streams === 1);
    fake.send(Role.LEADER);
    expect((await p)?.lease).toBe("lease-1");
  });

  it("reconnects when the stream ends while standby", async () => {
    const fake = new FakeIntegration();
    const p = leadership(fake).acquire(new AbortController().signal);
    await until(() => fake.streams === 1);
    fake.endStreams();
    await until(() => fake.opened === 2 && fake.streams === 1);
    fake.send(Role.LEADER);
    expect((await p)?.lease).toBe("lease-1");
  });

  it("gives up leadership when aborted", async () => {
    const fake = new FakeIntegration();
    const abort = new AbortController();
    const p = leadership(fake).acquire(abort.signal);
    await until(() => fake.streams === 1);
    fake.send(Role.LEADER);
    await p;
    abort.abort();
    await until(() => fake.streams === 0);
  });

  it("returns undefined when aborted while standby", async () => {
    const fake = new FakeIntegration();
    const abort = new AbortController();
    const p = leadership(fake).acquire(abort.signal);
    await until(() => fake.streams === 1);
    abort.abort();
    expect(await p).toBeUndefined();
  });
});
