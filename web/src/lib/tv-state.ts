import { ArtModeState, ConnectionState, PowerState, type Status } from "@/gen/vigo/core/v1/core_pb";

/**
 * The selected content id, but only when the TV confirmed it. An unconfirmed
 * id comes from get_current_artwork, which can be hours out of date, so the
 * UI never presents it as the selection.
 */
export function confirmedSelection(status: Status | null | undefined): string {
  const s = status?.selectedArt;
  return s?.confirmed ? s.contentId : "";
}

export type TvStateKind =
  | "loading"
  | "showing-art"
  | "tv-mode"
  | "motion-sleep"
  | "powered-off"
  | "standby"
  | "unreachable"
  | "not-paired"
  | "no-art-host"
  | "standby-replica"
  | "connecting"
  | "unknown";

export type TvState = {
  kind: TvStateKind;
  title: string;
  body: string;
  /** Whether ShowArt has a chance of doing something useful from this state. */
  canShowArt: boolean;
  /** Whether the TV is in a state to accept art and settings requests. */
  controllable: boolean;
};

/**
 * Maps a Status to the one state the UI explains. Order matters: a replica or
 * pairing problem hides everything the TV might be doing.
 */
export function tvState(status: Status | null | undefined): TvState {
  if (!status) {
    return { kind: "loading", title: "Checking the TV", body: "Asking vigo for the TV's status…", canShowArt: false, controllable: false };
  }
  const { connection, power, artMode } = status;

  if (connection === ConnectionState.STANDBY_REPLICA) {
    return {
      kind: "standby-replica",
      title: "Standby replica",
      body:
        "This vigo replica isn't the leader, so it holds no connection to the TV. Requests are normally routed to the leader; seeing this usually means the leader is restarting.",
      canShowArt: false,
      controllable: false,
    };
  }
  if (connection === ConnectionState.NOT_PAIRED) {
    return {
      kind: "not-paired",
      title: "Not paired",
      body:
        "vigo has no access token for this TV, or the TV rejected the one it had. Pair once — it takes a press of Allow on the TV remote.",
      canShowArt: false,
      controllable: false,
    };
  }
  if (power === PowerState.UNREACHABLE || connection === ConnectionState.DISCONNECTED) {
    return {
      kind: "unreachable",
      title: "Can't reach the TV",
      body:
        power === PowerState.UNREACHABLE
          ? "The TV isn't answering on the network. If it has been asleep for a while, check that IP Remote is turned on in the TV's settings; without it, only the physical remote can wake it."
          : "The TV answers on the network, but vigo's connection to it dropped. vigo reconnects on its own.",
      canShowArt: false,
      controllable: false,
    };
  }
  if (connection === ConnectionState.CONNECTING || connection === ConnectionState.UNSPECIFIED) {
    return { kind: "connecting", title: "Connecting", body: "vigo is connecting to the TV. This usually takes a few seconds.", canShowArt: false, controllable: false };
  }
  if (connection === ConnectionState.NO_ART_HOST) {
    return {
      kind: "no-art-host",
      title: "Art Mode hasn't started yet",
      body:
        "The TV has restarted and its art app hasn't come up since, so it ignores art requests. Enter Art Mode once with the TV's own remote; after that vigo can control art until the next restart.",
      canShowArt: false,
      controllable: false,
    };
  }
  if (power === PowerState.STANDBY) {
    if (artMode === ArtModeState.ON) {
      return {
        kind: "motion-sleep",
        title: "Asleep",
        body: "The motion timer put the TV to sleep because nobody was around. Show art wakes it with the selected artwork.",
        canShowArt: true,
        controllable: true,
      };
    }
    if (artMode === ArtModeState.OFF) {
      return {
        kind: "powered-off",
        title: "Powered off",
        body: "The TV is in standby. Show art turns it on in Art Mode; the panel may stay dark until it senses someone in the room.",
        canShowArt: true,
        controllable: true,
      };
    }
    return { kind: "standby", title: "Standby", body: "The TV is in standby. Show art wakes it into Art Mode.", canShowArt: true, controllable: true };
  }
  if (power === PowerState.ON && artMode === ArtModeState.OFF) {
    return {
      kind: "tv-mode",
      title: "TV mode",
      body: "The TV is on and showing something other than art.",
      canShowArt: true,
      controllable: true,
    };
  }
  if (status.showingArt) {
    return {
      kind: "showing-art",
      title: "Art Mode",
      body: "The TV is in Art Mode. The panel may still be dark if the motion timer has turned it off.",
      canShowArt: true,
      controllable: true,
    };
  }
  return { kind: "unknown", title: "Status unknown", body: "The TV reported a state vigo doesn't recognise.", canShowArt: true, controllable: true };
}
