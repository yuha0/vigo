import { toast } from "sonner";

import type { ItemResult } from "@/gen/vigo/core/v1/library_pb";

/**
 * One toast for a batch: "Deleted 12, 2 failed", naming the failures. Each
 * item succeeds or fails on its own, so a partial result is normal.
 */
export function batchToast(verb: string, noun: string, results: ItemResult[], name: (id: string) => string) {
  const failed = results.filter((r) => r.errorCode);
  const ok = results.length - failed.length;
  const title = failed.length ? `${verb} ${ok}, ${failed.length} failed` : `${verb} ${ok} ${ok === 1 ? noun : `${noun}s`}`;
  if (!failed.length) {
    toast.success(title);
    return;
  }
  const shown = failed.slice(0, 3).map((f) => `${name(f.id)}: ${f.errorMessage || f.errorCode}`);
  if (failed.length > shown.length) shown.push(`and ${failed.length - shown.length} more`);
  (ok ? toast.warning : toast.error)(title, { description: shown.join("\n"), duration: 10_000 });
}
