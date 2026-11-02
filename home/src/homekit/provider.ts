import { Accessory, Bridge, Categories, type PublishInfo } from "@homebridge/hap-nodejs";
import type { Core } from "../core/core";
import type { Setting } from "../gen/vigo/core/v1/core_pb";
import { errMsg, type Logger } from "../log";
import type { Metrics } from "../metrics";
import {
  ACCESSORY_CATALOG,
  PAIRING_GROUP_NAMES,
  ProviderError,
  isAccessoryId,
  isPairingGroupId,
  type AccessoryId,
  type AccessoryView,
  type PairingGroupId,
  type PairingGroupView,
  type Provider,
} from "../provider";
import {
  DEFAULT_REMOTE_MAPPING,
  KEY_OPTIONS,
  bindingsOf,
  isRemoteButton,
  normalize,
  parseRemoteMapping,
  targetError,
  type RemoteBindingView,
  type RemoteMapping,
  type RemoteMappingView,
  type RemoteTargetView,
} from "../remote";
import type { HomeStore } from "../storage/home";
import {
  BRIDGED_BUILDERS,
  accessoryUUID,
  buildTelevision,
  setInfo,
  type AccessoryContext,
  type BuiltAccessory,
} from "./accessories";
import { hapHost, type HapHost } from "./host";
import { generateIdentity, setupURI, type GroupIdentity } from "./identity";
import { DEFAULT_MOTION_TIMER, MOTION_TIMER_KEY, MOTION_TIMER_OFF } from "./mapping";
import { parseHomeState, type HomeState } from "./state";

export interface HomeKitOptions {
  core: Core;
  store: HomeStore;
  log: Logger;
  metrics: Metrics;
  host?: HapHost;
  tvName: string;
  hapPortTv: number;
  hapPortBridge: number;
  hapBind?: string[];
  writeWindowMs?: number;
}

type BridgedId = Exclude<AccessoryId, "television">;

const GROUP_CATEGORY: Record<PairingGroupId, Categories> = {
  tv: Categories.TELEVISION,
  bridge: Categories.BRIDGE,
};

/**
 * Two independently paired HAP accessories:
 *  - "tv": the Television, published on its own. A Television that is bridged gets neither a TV
 *    tile in the Home app nor an entry in the Control Center remote, so it cannot share the bridge.
 *  - "bridge": a Bridge of the optional accessories, advertised only while one is enabled.
 */
export class HomeKitProvider implements Provider {
  readonly protocol = "homekit" as const;
  readonly implemented = true;

  private readonly core: Core;
  private readonly store: HomeStore;
  private readonly log: Logger;
  private readonly metrics: Metrics;
  private readonly host: HapHost;
  private readonly opts: HomeKitOptions;
  private readonly ctx: AccessoryContext;
  private state: HomeState = { version: 1, groups: {}, enabled: {} };
  private remote: RemoteMapping = { ...DEFAULT_REMOTE_MAPPING };
  private started = false;

  private tv?: BuiltAccessory;
  private tvPublished = false;
  private bridge?: Bridge;
  private bridgePublished = false;
  private readonly bridged = new Map<BridgedId, BuiltAccessory>();

  private queue: Promise<unknown> = Promise.resolve();
  private readonly unsubscribe: (() => void)[] = [];

  constructor(opts: HomeKitOptions) {
    this.opts = opts;
    this.core = opts.core;
    this.store = opts.store;
    this.log = opts.log.child({ provider: "homekit" });
    this.metrics = opts.metrics;
    this.host = opts.host ?? hapHost;
    this.ctx = {
      core: this.core,
      log: this.log,
      metrics: this.metrics,
      lastMotionTimer: () => this.state.lastMotionTimer ?? DEFAULT_MOTION_TIMER,
      remoteTarget: (button) => this.remote[button],
      writeWindowMs: opts.writeWindowMs,
    };
  }

  /** HAP storage must already be installed (storage/hap.ts). */
  start(): Promise<void> {
    return this.serialize(async () => {
      if (this.started) return;
      this.state = parseHomeState(this.store.loadHome());
      const { mapping, invalid } = parseRemoteMapping(this.state.remote);
      this.remote = mapping;
      if (invalid.length > 0) this.log.warn({ buttons: invalid }, "invalid stored remote bindings; using the defaults for them");
      let changed = false;
      for (const g of ["tv", "bridge"] as const) {
        if (!this.state.groups[g]) {
          this.state.groups[g] = generateIdentity();
          changed = true;
          this.log.info({ group: g }, "generated pairing identity");
        }
      }
      if (changed) await this.store.saveHome(this.state);

      this.subscribe();
      await this.publishTv();
      await this.reconcileBridge();
      this.started = true;
      this.updatePairedMetrics();
    });
  }

  stop(): Promise<void> {
    return this.serialize(async () => {
      for (const off of this.unsubscribe.splice(0)) off();
      const tasks: Promise<void>[] = [];
      if (this.tv && this.tvPublished) tasks.push(this.host.unpublish(this.tv.accessory));
      if (this.bridge && this.bridgePublished) tasks.push(this.host.unpublish(this.bridge));
      this.tvPublished = false;
      this.bridgePublished = false;
      await Promise.allSettled(tasks);
      this.started = false;
      this.log.info("unpublished HAP accessories");
    });
  }

  listAccessories(): AccessoryView[] {
    return ACCESSORY_CATALOG.map((a) => ({ ...a, enabled: this.isEnabled(a.id) }));
  }

  setAccessoryEnabled(id: string, enabled: boolean): Promise<AccessoryView> {
    return this.serialize(async () => {
      if (!isAccessoryId(id)) throw new ProviderError("not_found", `unknown accessory ${JSON.stringify(id)}`);
      const view = this.view(id);
      if (!view.canDisable) {
        if (!enabled) throw new ProviderError("invalid_argument", `accessory ${id} cannot be disabled`);
        return view;
      }
      if (view.enabled === enabled) return view;
      this.requireStarted();
      this.state.enabled[id] = enabled;
      await this.store.saveHome(this.state);
      await this.reconcileBridge();
      this.log.info({ accessory: id, enabled }, "accessory enablement changed");
      return this.view(id);
    });
  }

  listPairingGroups(): PairingGroupView[] {
    const groups = (["tv", "bridge"] as const).map((g) => this.groupView(g));
    for (const g of groups) this.metrics.paired.set({ group: g.id }, g.paired ? 1 : 0);
    return groups;
  }

  resetPairing(groupId: string): Promise<PairingGroupView> {
    return this.serialize(async () => {
      if (!isPairingGroupId(groupId)) throw new ProviderError("not_found", `unknown pairing group ${JSON.stringify(groupId)}`);
      this.requireStarted();
      const old = this.state.groups[groupId]!;

      if (groupId === "tv") {
        if (this.tv) await this.host.destroy(this.tv.accessory);
        this.host.forget(old.username);
        this.tv = undefined;
        this.tvPublished = false;
      } else {
        if (this.bridge) await this.host.destroy(this.bridge);
        this.host.forget(old.username);
        this.bridge = undefined;
        this.bridgePublished = false;
        this.bridged.clear();
      }

      // A fresh device id too, so iOS treats the result as a new accessory rather than a
      // known one that suddenly rejects it.
      this.state.groups[groupId] = generateIdentity();
      await this.store.saveHome(this.state);
      await this.store.flush();
      this.log.warn({ group: groupId, oldUsername: old.username }, "pairing reset");

      if (groupId === "tv") await this.publishTv();
      else await this.reconcileBridge();
      this.updatePairedMetrics();
      return this.groupView(groupId);
    });
  }

  getRemoteMapping(): RemoteMappingView {
    return { bindings: bindingsOf(this.remote), keys: [...KEY_OPTIONS] };
  }

  updateRemoteMapping(bindings: { button: string; target: unknown }[]): Promise<RemoteBindingView[]> {
    return this.serialize(async () => {
      this.requireStarted();
      const next: RemoteMapping = { ...this.remote };
      for (const b of bindings) {
        if (!isRemoteButton(b.button)) throw new ProviderError("invalid_argument", `unknown remote button ${JSON.stringify(b.button)}`);
        const err = targetError(b.target);
        if (err) throw new ProviderError("invalid_argument", `${b.button}: ${err}`);
        next[b.button] = normalize(b.target as RemoteTargetView);
      }
      await this.saveRemote(next);
      this.log.info({ buttons: bindings.map((b) => b.button) }, "remote mapping updated");
      return bindingsOf(this.remote);
    });
  }

  resetRemoteMapping(): Promise<RemoteBindingView[]> {
    return this.serialize(async () => {
      this.requireStarted();
      await this.saveRemote({ ...DEFAULT_REMOTE_MAPPING });
      this.log.info("remote mapping reset to defaults");
      return bindingsOf(this.remote);
    });
  }

  private async saveRemote(next: RemoteMapping): Promise<void> {
    const previous = this.state.remote;
    this.state.remote = next;
    try {
      await this.store.saveHome(this.state);
    } catch (err) {
      this.state.remote = previous;
      throw err;
    }
    this.remote = next;
  }

  private serialize<T>(fn: () => Promise<T>): Promise<T> {
    const run = this.queue.then(fn);
    this.queue = run.catch(() => undefined);
    return run;
  }

  private requireStarted(): void {
    if (!this.started) throw new ProviderError("unavailable", "the homekit provider is not running");
  }

  private isEnabled(id: AccessoryId): boolean {
    return id === "television" || this.state.enabled[id] === true;
  }

  private view(id: AccessoryId): AccessoryView {
    const a = ACCESSORY_CATALOG.find((x) => x.id === id)!;
    return { ...a, enabled: this.isEnabled(id) };
  }

  private groupView(g: PairingGroupId): PairingGroupView {
    const identity = this.state.groups[g];
    const accessoryIds = ACCESSORY_CATALOG.filter((a) => a.pairingGroupId === g && this.isEnabled(a.id)).map((a) => a.id);
    return {
      id: g,
      name: PAIRING_GROUP_NAMES[g],
      advertised: g === "tv" ? this.tvPublished : this.bridgePublished,
      paired: identity && this.started ? this.host.isPaired(identity.username) : false,
      setupCode: identity?.pincode ?? "",
      setupUri: identity ? setupURI(identity.pincode, GROUP_CATEGORY[g], identity.setupID) : "",
      accessoryIds,
    };
  }

  private publishInfo(g: PairingGroupId): PublishInfo {
    const identity: GroupIdentity = this.state.groups[g]!;
    const info: PublishInfo = {
      username: identity.username,
      pincode: identity.pincode,
      setupID: identity.setupID,
      category: GROUP_CATEGORY[g],
      port: g === "tv" ? this.opts.hapPortTv : this.opts.hapPortBridge,
      addIdentifyingMaterial: false,
    };
    if (this.opts.hapBind && this.opts.hapBind.length > 0) info.bind = this.opts.hapBind;
    return info;
  }

  private watchPairing(acc: Accessory, g: PairingGroupId): void {
    acc.on("paired", () => {
      this.log.info({ group: g }, "controller paired");
      this.updatePairedMetrics();
      void this.store.flush().catch((err: unknown) => this.log.error({ err: errMsg(err) }, "persisting pairing failed"));
    });
    acc.on("unpaired", () => {
      this.log.warn({ group: g }, "last controller unpaired");
      this.updatePairedMetrics();
    });
  }

  private async publishTv(): Promise<void> {
    if (!this.tv) {
      this.tv = buildTelevision(this.ctx, this.opts.tvName);
      this.watchPairing(this.tv.accessory, "tv");
    }
    this.tv.refresh();
    if (this.tvPublished) return;
    const info = this.publishInfo("tv");
    await this.host.publish(this.tv.accessory, info);
    this.tvPublished = true;
    this.log.info({ group: "tv", port: info.port, username: info.username }, "published HAP accessory");
  }

  /** Brings the bridge's accessories and advertisement in line with what is enabled. */
  private async reconcileBridge(): Promise<void> {
    const want = new Set(
      ACCESSORY_CATALOG.filter((a) => a.pairingGroupId === "bridge" && this.isEnabled(a.id)).map((a) => a.id as BridgedId),
    );

    if (want.size === 0) {
      if (this.bridge && this.bridgePublished) {
        await this.host.unpublish(this.bridge);
        this.bridgePublished = false;
        this.log.info({ group: "bridge" }, "unpublished HAP bridge: no accessory enabled");
      }
      if (this.bridge && this.bridged.size > 0) {
        this.bridge.removeBridgedAccessories([...this.bridged.values()].map((b) => b.accessory));
      }
      this.bridged.clear();
      return;
    }

    if (!this.bridge) {
      this.bridge = new Bridge("vigo Bridge", accessoryUUID("bridge"));
      setInfo(this.bridge, "vigo Bridge", "bridge");
      this.watchPairing(this.bridge, "bridge");
    }
    for (const [id, built] of this.bridged) {
      if (!want.has(id)) {
        this.bridge.removeBridgedAccessory(built.accessory);
        this.bridged.delete(id);
        this.log.info({ accessory: id }, "removed bridged accessory");
      }
    }
    for (const id of want) {
      if (this.bridged.has(id)) continue;
      const built = BRIDGED_BUILDERS[id](this.ctx);
      built.refresh();
      this.bridge.addBridgedAccessory(built.accessory);
      this.bridged.set(id, built);
      this.log.info({ accessory: id }, "added bridged accessory");
    }
    if (!this.bridgePublished) {
      const info = this.publishInfo("bridge");
      await this.host.publish(this.bridge, info);
      this.bridgePublished = true;
      this.log.info({ group: "bridge", port: info.port, username: info.username }, "published HAP bridge");
    }
  }

  private subscribe(): void {
    const onStatus = () => {
      this.tv?.refresh();
      this.bridged.get("art_mode")?.refresh();
    };
    const onSetting = (s: Setting) => {
      if (s.key === MOTION_TIMER_KEY) {
        this.rememberMotionTimer(s);
        this.bridged.get("keep_art_on")?.refresh();
      }
      if (s.key === "brightness") this.bridged.get("art_brightness")?.refresh();
    };
    const onConnected = () => {
      this.tv?.refresh();
      for (const b of this.bridged.values()) b.refresh();
    };
    this.core.on("status", onStatus);
    this.core.on("setting", onSetting);
    this.core.on("connected", onConnected);
    this.unsubscribe.push(
      () => this.core.off("status", onStatus),
      () => this.core.off("setting", onSetting),
      () => this.core.off("connected", onConnected),
    );
  }

  private rememberMotionTimer(s: Setting): void {
    if (s.kind.case !== "enum") return;
    const value = s.kind.value.value;
    if (value === "" || value === MOTION_TIMER_OFF || value === this.state.lastMotionTimer) return;
    this.state.lastMotionTimer = value;
    this.log.info({ motionTimer: value }, "remembered motion_timer");
    this.store.saveHome(this.state).catch((err: unknown) => this.log.error({ err: errMsg(err) }, "saving home state failed"));
  }

  private updatePairedMetrics(): void {
    this.listPairingGroups();
  }
}
