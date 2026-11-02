import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport, type Transport } from "@connectrpc/connect";
import { Core } from "../core/core";
import {
  ArtModeState,
  ConnectionState,
  PowerState,
  SettingSchema,
  StatusSchema,
  TVService,
  type Setting,
  type Status,
  type WatchEventsResponse,
} from "../gen/vigo/core/v1/core_pb";
import { silentLogger } from "../log";

export interface Call {
  method: string;
  req: unknown;
}

/** An in-memory vigo-core TVService for tests. */
export class FakeTV {
  status: Status = create(StatusSchema, {
    power: PowerState.ON,
    artMode: ArtModeState.ON,
    connection: ConnectionState.CONNECTED,
    showingArt: true,
  });
  settings = new Map<string, Setting>([
    ["brightness", create(SettingSchema, { key: "brightness", kind: { case: "range", value: { value: 4, min: 0, max: 10 } } })],
    [
      "motion_timer",
      create(SettingSchema, {
        key: "motion_timer",
        kind: { case: "enum", value: { value: "15", options: ["off", "5", "15", "30", "60", "120", "240"] } },
      }),
    ],
  ]);
  readonly calls: Call[] = [];
  /** Set to make every unary call fail. */
  fail?: ConnectError;
  watchers = 0;

  private subscribers = new Set<(ev: WatchEventsResponse["event"]) => void>();
  private endStreams = new Set<() => void>();

  readonly transport: Transport = createRouterTransport(({ service }) => {
    service(TVService, {
      getStatus: () => ({ status: this.status }),
      watchEvents: (_req, ctx) => this.watch(ctx.signal),
      showArt: (req) => {
        this.record("showArt", req);
        this.status = create(StatusSchema, { ...this.status, power: PowerState.ON, artMode: ArtModeState.ON });
        this.emit({ case: "status", value: this.status });
        return { status: this.status };
      },
      setArtMode: (req) => {
        this.record("setArtMode", req);
        this.status = create(StatusSchema, { ...this.status, artMode: req.on ? ArtModeState.ON : ArtModeState.OFF });
        this.emit({ case: "status", value: this.status });
        return { status: this.status };
      },
      listSettings: () => {
        this.check();
        return { settings: [...this.settings.values()] };
      },
      updateSetting: (req) => {
        this.record("updateSetting", req);
        const cur = this.settings.get(req.key);
        if (!cur) throw new ConnectError("no such setting", Code.InvalidArgument);
        let next: Setting;
        if (cur.kind.case === "range" && req.value.case === "intValue") {
          next = create(SettingSchema, { key: req.key, kind: { case: "range", value: { ...cur.kind.value, value: req.value.value } } });
        } else if (cur.kind.case === "enum" && req.value.case === "enumValue") {
          next = create(SettingSchema, { key: req.key, kind: { case: "enum", value: { ...cur.kind.value, value: req.value.value } } });
        } else {
          throw new ConnectError("wrong value type", Code.InvalidArgument);
        }
        this.settings.set(req.key, next);
        this.emit({ case: "settingChanged", value: next });
        return { setting: next, confirmed: true };
      },
      sendKey: (req) => {
        this.record("sendKey", req);
        if (req.key === "KEY_POWER") throw new ConnectError("use ShowArt", Code.InvalidArgument);
        return {};
      },
    });
  });

  private record(method: string, req: unknown): void {
    this.check();
    this.calls.push({ method, req });
  }

  private check(): void {
    if (this.fail) throw this.fail;
  }

  /** Pushes an event to every open WatchEvents stream. */
  emit(event: WatchEventsResponse["event"]): void {
    for (const s of this.subscribers) s(event);
  }

  /** Ends every open WatchEvents stream, as a vigo-core restart would. */
  dropStreams(): void {
    for (const end of this.endStreams) end();
  }

  setSetting(s: Setting): void {
    this.settings.set(s.key, s);
    this.emit({ case: "settingChanged", value: s });
  }

  private async *watch(signal: AbortSignal): AsyncGenerator<{ event: WatchEventsResponse["event"] }> {
    this.check();
    const queue: WatchEventsResponse["event"][] = [];
    let wake: (() => void) | undefined;
    let ended = false;
    const push = (ev: WatchEventsResponse["event"]) => {
      queue.push(ev);
      wake?.();
    };
    const end = () => {
      ended = true;
      wake?.();
    };
    this.subscribers.add(push);
    this.endStreams.add(end);
    signal.addEventListener("abort", end, { once: true });
    this.watchers++;
    try {
      yield { event: { case: "status", value: this.status } };
      while (!ended) {
        if (queue.length === 0) await new Promise<void>((r) => (wake = r));
        wake = undefined;
        while (queue.length > 0) yield { event: queue.shift()! };
      }
    } finally {
      this.watchers--;
      this.subscribers.delete(push);
      this.endStreams.delete(end);
    }
  }
}

export async function until(cond: () => unknown, timeoutMs = 2000): Promise<void> {
  const start = Date.now();
  while (!cond()) {
    if (Date.now() - start > timeoutMs) throw new Error("condition not met in time");
    await new Promise((r) => setTimeout(r, 5));
  }
}

/** A started Core connected to a FakeTV, with settings loaded. */
export async function connectedCore(tv = new FakeTV()): Promise<{ tv: FakeTV; core: Core }> {
  const core = new Core(tv.transport, silentLogger(), { minBackoffMs: 10, maxBackoffMs: 20, settingsRetryMs: 20 });
  core.start();
  await until(() => core.connected && core.setting("motion_timer") && core.setting("brightness"));
  return { tv, core };
}
