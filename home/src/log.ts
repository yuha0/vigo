import { pino, type Logger } from "pino";

export type { Logger };

export function createLogger(level: string): Logger {
  return pino({
    level,
    base: { component: "vigo-home" },
    timestamp: pino.stdTimeFunctions.isoTime,
    formatters: { level: (label) => ({ level: label }) },
  });
}

export function silentLogger(): Logger {
  return pino({ level: "silent" });
}

export function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
