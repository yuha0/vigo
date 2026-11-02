import { clone, create } from "@bufbuild/protobuf";
import { timestampFromMs, timestampNow } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  ArtDeletedSchema,
  ArtModeState,
  ArtworkSchema,
  ConfigSchema,
  ConnectionState,
  ContentType,
  KeyAction,
  PairingSchema,
  PairingState,
  PowerState,
  SelectedArtSchema,
  SettingSchema,
  StatusSchema,
  WarningSchema,
  WatchEventsResponseSchema,
  type Artwork,
  type Config,
  type Pairing,
  type Setting,
  type Status,
  type UpdateSettingRequest,
  type WatchEventsResponse,
} from "@/gen/vigo/core/v1/core_pb";
import type { Scenario, SentKey } from "@/lib/mock/control";
import * as fixtures from "@/lib/mock/fixtures";

export type MockTvOptions = {
  scenario: Scenario;
  /** Delay before a state change lands, as the real TV takes a moment. */
  transitionMs: number;
  /** How long the TV waits for someone to press Allow. */
  pairingDelayMs: number;
  pairingOutcome: "allow" | "timeout";
  /** Run the motion timer, so an idle TV falls asleep. One minute of timer = this many ms. */
  motionMinuteMs: number | null;
  /** Keys whose setter the mock acknowledges without applying, as the real TV sometimes does. */
  stubbornKeys: string[];
  settings?: Setting[];
  /** Start with two uploaded photos besides the TV's own art. */
  uploads?: boolean;
};

type Event = WatchEventsResponse["event"];
type Listener = (ev: Event | "end") => void;

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

function fail(code: Code, message: string): never {
  throw new ConnectError(message, code);
}

export class MockTv {
  status: Status;
  settings: Setting[];
  artworks: Artwork[];
  pairing: Pairing;
  config: Config;
  readonly sentKeys: SentKey[] = [];
  scenario: Scenario;

  private listeners = new Set<Listener>();
  private sleepTimer: ReturnType<typeof setTimeout> | undefined;
  private pairingTimers: ReturnType<typeof setTimeout>[] = [];
  private uploadSeq = 2;

  constructor(private opts: MockTvOptions) {
    this.scenario = opts.scenario;
    this.settings = opts.settings ?? fixtures.settings();
    this.artworks = fixtures.artworks(opts.uploads ?? true);
    this.config = create(ConfigSchema, { tvHost: "192.168.40.21", tvPort: 8002, clientName: "vigo" });
    this.status = create(StatusSchema, {
      device: fixtures.device(),
      selectedArt: create(SelectedArtSchema, { contentId: "SAM-F0206", matteId: "none", portraitMatteId: "shadowbox_polar", confirmed: true }),
      replica: "vigo-core-7c9f6d4b8-x2lqp",
    });
    this.pairing = create(PairingSchema, { state: PairingState.PAIRED });
    this.applyScenario(opts.scenario, false);
  }

  applyScenario(s: Scenario, emit = true) {
    this.scenario = s;
    const shape = fixtures.scenarioStatus(s);
    this.pairing = create(PairingSchema, {
      state: shape.connection === ConnectionState.NOT_PAIRED ? PairingState.UNPAIRED : PairingState.PAIRED,
    });
    // "unconfirmed" mimics a fresh vigo that only has the TV's possibly stale get_current_artwork report.
    const prev = this.status.selectedArt;
    const selectedArt = create(SelectedArtSchema, {
      contentId: prev?.contentId,
      matteId: prev?.matteId,
      portraitMatteId: prev?.portraitMatteId,
      confirmed: s !== "unconfirmed",
    });
    this.patch(
      {
        selectedArt,
        power: shape.power,
        artMode: shape.artMode,
        connection: shape.connection,
        leader: shape.leader,
        replica: shape.leader ? "vigo-core-7c9f6d4b8-x2lqp" : "vigo-core-7c9f6d4b8-m8r4t",
        warnings: (shape.warnings ?? []).map((w) =>
          create(WarningSchema, { ...w, since: timestampFromMs(Date.now() - 3 * 3_600_000) }),
        ),
      },
      emit,
    );
    if (emit) this.emit({ case: "pairing", value: clone(PairingSchema, this.pairing) });
  }

  // ---- events ---------------------------------------------------------------

  listen(fn: Listener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  emit(ev: Event) {
    for (const l of this.listeners) l(ev);
  }

  dropStreams() {
    for (const l of this.listeners) l("end");
  }

  async *events(signal: AbortSignal): AsyncGenerator<WatchEventsResponse> {
    const queue: (Event | "end")[] = [{ case: "status", value: clone(StatusSchema, this.status) }];
    let wake: (() => void) | undefined;
    const unlisten = this.listen((ev) => {
      queue.push(ev);
      wake?.();
    });
    const onAbort = () => wake?.();
    signal.addEventListener("abort", onAbort);
    try {
      while (!signal.aborted) {
        const next = queue.shift();
        if (next === "end") return;
        if (next) {
          yield create(WatchEventsResponseSchema, { time: timestampNow(), event: next });
          continue;
        }
        await new Promise<void>((r) => (wake = r));
        wake = undefined;
      }
    } finally {
      unlisten();
      signal.removeEventListener("abort", onAbort);
    }
  }

  private patch(p: Partial<Status>, emit = true) {
    const next = { ...this.status, ...p };
    next.showingArt = next.power === PowerState.ON && next.artMode === ArtModeState.ON;
    next.updatedAt = timestampNow();
    this.status = create(StatusSchema, next);
    if (emit) this.emit({ case: "status", value: clone(StatusSchema, this.status) });
    this.armMotionTimer();
  }

  private settle() {
    return this.opts.transitionMs > 0 ? sleep(this.opts.transitionMs) : Promise.resolve();
  }

  /** Falls asleep after motion_timer minutes in Art Mode, like the real panel with nobody around. */
  private armMotionTimer() {
    clearTimeout(this.sleepTimer);
    const minuteMs = this.opts.motionMinuteMs;
    if (minuteMs == null || !this.status.showingArt) return;
    const timer = this.settings.find((s) => s.key === "motion_timer");
    if (timer?.kind.case !== "enum" || timer.kind.value.value === "off") return;
    const minutes = Number(timer.kind.value.value);
    if (!Number.isFinite(minutes)) return;
    this.sleepTimer = setTimeout(() => this.patch({ power: PowerState.STANDBY }), minutes * minuteMs);
  }

  // ---- preconditions -------------------------------------------------------

  private requireConnection() {
    switch (this.status.connection) {
      case ConnectionState.STANDBY_REPLICA:
        fail(Code.FailedPrecondition, "this replica is on standby and holds no TV connection");
        break;
      case ConnectionState.NOT_PAIRED:
        fail(Code.FailedPrecondition, "the TV is not paired");
        break;
      case ConnectionState.DISCONNECTED:
        fail(Code.FailedPrecondition, "the TV is unreachable");
        break;
      case ConnectionState.CONNECTING:
      case ConnectionState.UNSPECIFIED:
        fail(Code.FailedPrecondition, "still connecting to the TV");
    }
    if (this.status.power === PowerState.UNREACHABLE) fail(Code.FailedPrecondition, "the TV is unreachable");
  }

  private requireArtHost() {
    this.requireConnection();
    if (this.status.connection === ConnectionState.NO_ART_HOST) {
      fail(Code.FailedPrecondition, "no art host: Art Mode has not been entered since the TV started");
    }
  }

  // ---- TVService -------------------------------------------------------------

  async showArt(contentId: string) {
    this.requireArtHost();
    if (contentId) this.select(contentId);
    await this.settle();
    this.patch({ power: PowerState.ON, artMode: ArtModeState.ON });
    return clone(StatusSchema, this.status);
  }

  async setArtMode(on: boolean) {
    this.requireArtHost();
    if (this.status.power === PowerState.STANDBY) {
      fail(Code.FailedPrecondition, "the TV is in standby; use Show art to wake it");
    }
    await this.settle();
    this.patch({ artMode: on ? ArtModeState.ON : ArtModeState.OFF });
    return clone(StatusSchema, this.status);
  }

  /** How many times Refresh was called, for tests. */
  refreshCount = 0;

  refresh() {
    this.refreshCount++;
    return clone(StatusSchema, this.status);
  }

  listSettings() {
    this.requireArtHost();
    return this.settings.map((s) => clone(SettingSchema, s));
  }

  async updateSetting(req: UpdateSettingRequest) {
    this.requireArtHost();
    const idx = this.settings.findIndex((s) => s.key === req.key);
    if (idx < 0) fail(Code.InvalidArgument, `unknown setting "${req.key}"`);
    const current = this.settings[idx];
    const next = clone(SettingSchema, current);
    const v = req.value;
    if (next.kind.case === "range" && v.case === "intValue") {
      const r = next.kind.value;
      if (v.value < r.min || v.value > r.max) fail(Code.InvalidArgument, `${req.key} must be between ${r.min} and ${r.max}`);
      r.value = v.value;
    } else if (next.kind.case === "enum" && v.case === "enumValue") {
      if (!next.kind.value.options.includes(v.value)) fail(Code.InvalidArgument, `"${v.value}" is not a valid ${req.key}`);
      next.kind.value.value = v.value;
    } else if (next.kind.case === "toggle" && v.case === "boolValue") {
      next.kind.value.value = v.value;
    } else {
      fail(Code.InvalidArgument, `wrong value type for ${req.key}`);
    }
    await this.settle();
    if (this.opts.stubbornKeys.includes(req.key)) {
      return { setting: clone(SettingSchema, current), confirmed: false };
    }
    this.settings[idx] = next;
    this.emit({ case: "settingChanged", value: clone(SettingSchema, next) });
    if (req.key === "motion_timer") this.armMotionTimer();
    return { setting: clone(SettingSchema, next), confirmed: true };
  }

  sendKey(key: string, action: KeyAction) {
    if (key === "KEY_POWER") fail(Code.InvalidArgument, "KEY_POWER is never sent; use Show art");
    if (!/^KEY_[A-Z0-9_]+$/.test(key)) fail(Code.InvalidArgument, `"${key}" is not a key code`);
    this.requireConnection();
    this.sentKeys.push({ key, action: action || KeyAction.CLICK });
  }

  // ---- ArtService ------------------------------------------------------------

  listArt(categoryId: string) {
    this.requireArtHost();
    const seen = new Set<string>();
    const artworks = this.artworks.filter((a) => {
      if (seen.has(a.contentId)) return false;
      seen.add(a.contentId);
      return !categoryId || a.categoryIds.includes(categoryId);
    });
    const categoryIds = [...new Set(this.artworks.flatMap((a) => a.categoryIds))].sort();
    return { artworks: artworks.map((a) => clone(ArtworkSchema, a)), categoryIds };
  }

  private select(contentId: string) {
    if (!this.artworks.some((a) => a.contentId === contentId)) fail(Code.NotFound, `no artwork ${contentId}`);
    this.status = create(StatusSchema, {
      ...this.status,
      selectedArt: create(SelectedArtSchema, {
        contentId,
        matteId: this.status.selectedArt?.matteId,
        portraitMatteId: this.status.selectedArt?.portraitMatteId,
        confirmed: true,
      }),
    });
    this.emit({ case: "artSelected", value: clone(SelectedArtSchema, this.status.selectedArt!) });
  }

  async selectArt(contentId: string) {
    this.requireArtHost();
    // select_image does nothing from power-off standby, so vigo-core fails fast instead of timing out.
    if (this.status.power === PowerState.STANDBY && this.status.artMode !== ArtModeState.ON) {
      fail(Code.FailedPrecondition, "the TV is powered off; use Show art");
    }
    if (!this.artworks.some((a) => a.contentId === contentId)) fail(Code.NotFound, `no artwork ${contentId}`);
    await this.settle();
    this.select(contentId);
    // select_image is what wakes the TV from motion-sleep.
    if (this.status.power === PowerState.STANDBY && this.status.artMode === ArtModeState.ON) {
      this.patch({ power: PowerState.ON });
    } else {
      this.patch({});
    }
    return clone(StatusSchema, this.status);
  }

  async deleteArt(ids: string[]) {
    this.requireArtHost();
    for (const id of ids) {
      const a = this.artworks.find((x) => x.contentId === id);
      if (!a) fail(Code.NotFound, `no artwork ${id}`);
      if (a.contentType !== ContentType.MOBILE) fail(Code.InvalidArgument, `${id} is not an upload; only uploads can be deleted`);
    }
    await this.settle();
    this.artworks = this.artworks.filter((a) => !ids.includes(a.contentId));
    this.emit({ case: "artDeleted", value: create(ArtDeletedSchema, { contentIds: ids }) });
    if (ids.includes(this.status.selectedArt?.contentId ?? "")) {
      const fallback = this.artworks.find((a) => a.contentType === ContentType.SERVER);
      if (fallback) this.select(fallback.contentId);
    }
    return ids;
  }

  listMattes() {
    this.requireArtHost();
    return { matteTypes: fixtures.matteTypes, colors: fixtures.matteColors };
  }

  changeMatte(): never {
    this.requireArtHost();
    fail(Code.FailedPrecondition, "the TV refused the matte change (error -7)");
  }

  /** Puts an uploaded image on the TV, as the reconciler would, and returns its content id. */
  addUpload(width: number, height: number): string {
    const contentId = `MY_F${String(this.uploadSeq++).padStart(4, "0")}`;
    const art = create(ArtworkSchema, {
      contentId,
      contentType: ContentType.MOBILE,
      categoryIds: ["MY-C0002", "MY-C0008"],
      matteId: "none",
      width: height,
      height: width,
      imageDate: timestampNow(),
    });
    this.artworks.push(art);
    this.emit({ case: "artAdded", value: clone(ArtworkSchema, art) });
    return contentId;
  }

  /** Removes content from the TV, as the reconciler would. */
  removeContent(contentId: string) {
    this.artworks = this.artworks.filter((a) => a.contentId !== contentId);
    this.emit({ case: "artDeleted", value: create(ArtDeletedSchema, { contentIds: [contentId] }) });
    if (this.status.selectedArt?.contentId === contentId) {
      const fallback = this.artworks.find((a) => a.contentType === ContentType.SERVER);
      if (fallback) this.select(fallback.contentId);
    }
  }

  // ---- PairingService ----------------------------------------------------------

  private setPairing(p: Partial<Pairing>) {
    this.pairing = create(PairingSchema, { state: p.state, message: p.message ?? "", deadline: p.deadline });
    this.emit({ case: "pairing", value: clone(PairingSchema, this.pairing) });
  }

  startPairing() {
    const c = this.status.connection;
    if (c === ConnectionState.STANDBY_REPLICA) fail(Code.FailedPrecondition, "this replica is on standby");
    if (c === ConnectionState.DISCONNECTED || this.status.power === PowerState.UNREACHABLE) {
      fail(Code.FailedPrecondition, "the TV is unreachable");
    }
    if (this.pairing.state === PairingState.WAITING_FOR_ALLOW) return clone(PairingSchema, this.pairing);
    const timeoutMs = 30_000;
    this.setPairing({ state: PairingState.WAITING_FOR_ALLOW, deadline: timestampFromMs(Date.now() + timeoutMs) });
    this.clearPairingTimers();
    if (this.opts.pairingOutcome === "allow") {
      this.pairingTimers.push(
        setTimeout(() => {
          this.setPairing({ state: PairingState.PAIRED, message: "Paired. The TV will remember vigo." });
          if (this.status.connection === ConnectionState.NOT_PAIRED) {
            this.patch({ connection: ConnectionState.CONNECTED, power: PowerState.ON, artMode: ArtModeState.ON });
          }
        }, this.opts.pairingDelayMs),
      );
    } else {
      this.pairingTimers.push(
        setTimeout(() => this.setPairing({ state: PairingState.FAILED, message: "Nobody pressed Allow on the TV in time." }), timeoutMs),
      );
    }
    return clone(PairingSchema, this.pairing);
  }

  cancelPairing() {
    this.clearPairingTimers();
    const paired = this.status.connection !== ConnectionState.NOT_PAIRED;
    this.setPairing({ state: paired ? PairingState.PAIRED : PairingState.UNPAIRED, message: "Pairing cancelled." });
    return clone(PairingSchema, this.pairing);
  }

  private clearPairingTimers() {
    this.pairingTimers.forEach(clearTimeout);
    this.pairingTimers = [];
  }

  // ---- ConfigService -------------------------------------------------------------

  async updateConfig(c: Config | undefined) {
    if (!c) fail(Code.InvalidArgument, "config is required");
    if (!c.tvHost.trim()) fail(Code.InvalidArgument, "TV host is required");
    if (c.tvPort !== 8001 && c.tvPort !== 8002) fail(Code.InvalidArgument, "TV port must be 8002 or 8001");
    if (!c.clientName.trim()) fail(Code.InvalidArgument, "client name is required");
    const reconnect = c.tvHost !== this.config.tvHost || c.tvPort !== this.config.tvPort;
    this.config = clone(ConfigSchema, c);
    if (reconnect && this.status.connection !== ConnectionState.STANDBY_REPLICA) {
      const previous = this.status.connection;
      this.patch({ connection: ConnectionState.CONNECTING });
      setTimeout(() => this.patch({ connection: previous }), Math.max(this.opts.transitionMs * 2, 10));
    }
    return clone(ConfigSchema, this.config);
  }

  dispose() {
    clearTimeout(this.sleepTimer);
    this.clearPairingTimers();
    this.dropStreams();
  }
}
