import { EventEmitter } from "node:events";
import { createClient, type Client, type Transport } from "@connectrpc/connect";
import {
  TVService,
  type Setting,
  type Status,
  type UpdateSettingResponse,
} from "../gen/vigo/core/v1/core_pb";
import { errMsg, type Logger } from "../log";
import { sleep } from "../sleep";

export interface CoreEvents {
  connected: [connected: boolean];
  status: [status: Status];
  setting: [setting: Setting];
}

export interface CoreOptions {
  /** First reconnect delay; doubles up to maxBackoffMs. */
  minBackoffMs?: number;
  maxBackoffMs?: number;
  /** Retry interval for ListSettings while connected and settings are unknown. */
  settingsRetryMs?: number;
  /** Deadline for unary calls. */
  callTimeoutMs?: number;
}

export type SettingValue =
  | { case: "intValue"; value: number }
  | { case: "enumValue"; value: string }
  | { case: "boolValue"; value: boolean };

/**
 * A cached view of vigo-core kept fresh by TVService.WatchEvents, plus the commands vigo-home
 * issues. Reads never wait on the network: callers check `connected` and the cached values.
 */
export class Core extends EventEmitter<CoreEvents> {
  private readonly client: Client<typeof TVService>;
  private readonly opts: Required<CoreOptions>;
  private abort?: AbortController;
  private loop?: Promise<void>;
  private settingsTimer?: NodeJS.Timeout;
  private _connected = false;
  private _status?: Status;
  private readonly _settings = new Map<string, Setting>();

  constructor(
    transport: Transport,
    private readonly log: Logger,
    opts: CoreOptions = {},
  ) {
    super();
    this.client = createClient(TVService, transport);
    this.opts = {
      minBackoffMs: opts.minBackoffMs ?? 1000,
      maxBackoffMs: opts.maxBackoffMs ?? 30000,
      settingsRetryMs: opts.settingsRetryMs ?? 15000,
      callTimeoutMs: opts.callTimeoutMs ?? 30000,
    };
  }

  get connected(): boolean {
    return this._connected;
  }

  get status(): Status | undefined {
    return this._connected ? this._status : undefined;
  }

  setting(key: string): Setting | undefined {
    return this._connected ? this._settings.get(key) : undefined;
  }

  start(): void {
    if (this.loop) return;
    this.abort = new AbortController();
    this.loop = this.watchLoop(this.abort.signal);
  }

  async stop(): Promise<void> {
    this.abort?.abort();
    clearTimeout(this.settingsTimer);
    await this.loop;
    this.loop = undefined;
    this.setConnected(false);
  }

  /** Reads the status from vigo-core, bypassing the cache, and updates the cache with it. */
  async getStatus(): Promise<Status | undefined> {
    const res = await this.client.getStatus({}, { timeoutMs: this.opts.callTimeoutMs });
    if (res.status) this.applyStatus(res.status);
    return res.status;
  }

  async showArt(): Promise<void> {
    const res = await this.client.showArt({}, { timeoutMs: this.opts.callTimeoutMs });
    if (res.status) this.applyStatus(res.status);
  }

  async setArtMode(on: boolean): Promise<void> {
    const res = await this.client.setArtMode({ on }, { timeoutMs: this.opts.callTimeoutMs });
    if (res.status) this.applyStatus(res.status);
  }

  async sendKey(key: string): Promise<void> {
    await this.client.sendKey({ key }, { timeoutMs: this.opts.callTimeoutMs });
  }

  async updateSetting(key: string, value: SettingValue): Promise<UpdateSettingResponse> {
    const res = await this.client.updateSetting({ key, value }, { timeoutMs: this.opts.callTimeoutMs });
    if (res.setting) this.applySetting(res.setting);
    if (!res.confirmed) this.log.warn({ key, value: value.value }, "TV acknowledged setting but re-read differs");
    return res;
  }

  private async watchLoop(signal: AbortSignal): Promise<void> {
    let backoff = this.opts.minBackoffMs;
    while (!signal.aborted) {
      try {
        let first = true;
        for await (const ev of this.client.watchEvents({}, { signal })) {
          if (first && ev.event.case === "status") {
            first = false;
            backoff = this.opts.minBackoffMs;
            this._settings.clear();
            this.applyStatus(ev.event.value);
            this.setConnected(true);
            void this.refreshSettings(signal);
            continue;
          }
          switch (ev.event.case) {
            case "status":
              this.applyStatus(ev.event.value);
              break;
            case "settingChanged":
              this.applySetting(ev.event.value);
              break;
            default:
              break;
          }
        }
        if (!signal.aborted) this.log.warn("WatchEvents stream ended");
      } catch (err) {
        if (signal.aborted) break;
        this.log.warn({ err: errMsg(err), retryInMs: backoff }, "WatchEvents stream failed");
      }
      this.setConnected(false);
      if (signal.aborted) break;
      await sleep(backoff * (0.8 + Math.random() * 0.4), signal);
      backoff = Math.min(backoff * 2, this.opts.maxBackoffMs);
    }
  }

  private async refreshSettings(signal: AbortSignal): Promise<void> {
    clearTimeout(this.settingsTimer);
    try {
      const res = await this.client.listSettings({}, { signal, timeoutMs: this.opts.callTimeoutMs });
      for (const s of res.settings) this.applySetting(s);
    } catch (err) {
      if (signal.aborted || !this._connected) return;
      this.log.warn({ err: errMsg(err), retryInMs: this.opts.settingsRetryMs }, "ListSettings failed");
      this.settingsTimer = setTimeout(() => void this.refreshSettings(signal), this.opts.settingsRetryMs);
    }
  }

  private applyStatus(status: Status): void {
    this._status = status;
    this.emit("status", status);
  }

  private applySetting(setting: Setting): void {
    this._settings.set(setting.key, setting);
    this.emit("setting", setting);
  }

  private setConnected(connected: boolean): void {
    if (this._connected === connected) return;
    this._connected = connected;
    if (!connected) clearTimeout(this.settingsTimer);
    this.log.info({ connected }, "vigo-core connection changed");
    this.emit("connected", connected);
  }
}
