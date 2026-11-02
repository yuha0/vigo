import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { ArtModeState, ConnectionState, PowerState, StatusSchema } from "@/gen/vigo/core/v1/core_pb";
import { tvState } from "@/lib/tv-state";

const s = (power: PowerState, artMode: ArtModeState, connection = ConnectionState.CONNECTED) =>
  create(StatusSchema, { power, artMode, connection, showingArt: power === PowerState.ON && artMode === ArtModeState.ON });

describe("tvState", () => {
  it("distinguishes the two standby states", () => {
    expect(tvState(s(PowerState.STANDBY, ArtModeState.ON)).kind).toBe("motion-sleep");
    expect(tvState(s(PowerState.STANDBY, ArtModeState.OFF)).kind).toBe("powered-off");
  });

  it("lets connection problems win over power", () => {
    expect(tvState(s(PowerState.ON, ArtModeState.ON, ConnectionState.NOT_PAIRED)).kind).toBe("not-paired");
    expect(tvState(s(PowerState.STANDBY, ArtModeState.ON, ConnectionState.STANDBY_REPLICA)).kind).toBe("standby-replica");
    expect(tvState(s(PowerState.ON, ArtModeState.OFF, ConnectionState.NO_ART_HOST)).kind).toBe("no-art-host");
  });

  it("never offers Show art when it can't help", () => {
    for (const c of [ConnectionState.NOT_PAIRED, ConnectionState.STANDBY_REPLICA, ConnectionState.CONNECTING, ConnectionState.NO_ART_HOST]) {
      expect(tvState(s(PowerState.STANDBY, ArtModeState.ON, c)).canShowArt).toBe(false);
    }
    expect(tvState(s(PowerState.UNREACHABLE, ArtModeState.UNSPECIFIED)).canShowArt).toBe(false);
  });
});
