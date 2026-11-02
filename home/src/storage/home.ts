import { HOME_KEY } from "./keys";
import type { StateStore } from "./state";

/** Where the HomeKit provider keeps home.json. */
export interface HomeStore {
  loadHome(): unknown;
  /** Resolves once the state is durable. */
  saveHome(state: unknown): Promise<void>;
  /** Waits for every pending write, HAP's included. */
  flush(): Promise<void>;
}

export function homeStore(state: StateStore): HomeStore {
  return {
    loadHome() {
      const raw = state.get(HOME_KEY);
      return raw === undefined ? undefined : (JSON.parse(Buffer.from(raw).toString("utf8")) as unknown);
    },
    saveHome(s) {
      return state.set(HOME_KEY, Buffer.from(JSON.stringify(s), "utf8"));
    },
    async flush() {
      if (!(await state.flush())) throw new Error("timed out writing state to vigo-core");
    },
  };
}

/** An in-memory HomeStore for tests. */
export function memoryHomeStore(initial?: unknown): HomeStore & { saved: unknown } {
  const s = {
    saved: initial,
    loadHome: () => s.saved,
    saveHome: (state: unknown) => {
      s.saved = structuredClone(state);
      return Promise.resolve();
    },
    flush: () => Promise.resolve(),
  };
  return s;
}
