import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { PowerState, StatusSchema } from "../gen/vigo/core/v1/core_pb";
import { connectedCore, until } from "../testing/fake-core";

describe("Core", () => {
  it("caches the first status and settings, then applies deltas", async () => {
    const { tv, core } = await connectedCore();
    expect(core.status?.power).toBe(PowerState.ON);
    tv.status = create(StatusSchema, { ...tv.status, power: PowerState.STANDBY });
    tv.emit({ case: "status", value: tv.status });
    await until(() => core.status?.power === PowerState.STANDBY);
    await core.stop();
  });

  it("reconnects with backoff and hides cached state while disconnected", async () => {
    const { tv, core } = await connectedCore();
    const transitions: boolean[] = [];
    core.on("connected", (c) => transitions.push(c));

    tv.fail = new ConnectError("restarting", Code.Unavailable);
    tv.dropStreams();
    await until(() => !core.connected);
    expect(core.status).toBeUndefined();
    expect(core.setting("brightness")).toBeUndefined();

    tv.fail = undefined;
    await until(() => core.connected && core.setting("brightness") !== undefined);
    expect(transitions).toEqual([false, true]);
    expect(tv.watchers).toBe(1);
    await core.stop();
    expect(tv.watchers).toBe(0);
  });
});
