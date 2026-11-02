export const scenarios = [
  "art",
  "tv-mode",
  "motion-sleep",
  "powered-off",
  "connecting",
  "unreachable",
  "not-paired",
  "no-art-host",
  "standby-replica",
  "model-changed",
] as const;

export type Scenario = (typeof scenarios)[number];

export const homeModes = ["online", "offline", "matter"] as const;
export type HomeMode = (typeof homeModes)[number];

export type SentKey = { key: string; action: number };

/** Knobs the in-browser mock exposes to tests and the dev overlay. */
export interface MockControl {
  readonly scenario: Scenario;
  setScenario(s: Scenario): void;
  readonly homeMode: HomeMode;
  setHomeMode(m: HomeMode): void;
  /** Ends every open WatchEvents stream, to exercise reconnects. */
  dropStreams(): void;
  readonly sentKeys: readonly SentKey[];
}
