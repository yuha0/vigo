import { TriangleAlert } from "lucide-react";

import type { Warning } from "@/gen/vigo/core/v1/core_pb";
import { cn } from "@/lib/utils";

export function WarningsBanner({ warnings, className }: { warnings: Warning[]; className?: string }) {
  if (!warnings.length) return null;
  return (
    <div role="alert" aria-label="Warnings" className={cn("flex flex-col gap-2", className)}>
      {warnings.map((w) => (
        <div
          key={w.code + w.message}
          className="flex gap-3 rounded-2xl bg-warning-surface px-4 py-3.5 text-[0.9375rem] text-warning-foreground ring-1 ring-warning/30 animate-fade-up"
        >
          <TriangleAlert className="mt-0.5 size-[1.125rem] shrink-0 text-warning" />
          <div className="min-w-0">
            <p className="font-medium">{warningTitle(w.code)}</p>
            <p className="mt-0.5 opacity-85">{w.message}</p>
          </div>
        </div>
      ))}
    </div>
  );
}

const titles: Record<string, string> = {
  model_changed: "The TV's model changed",
  token_rejected: "The TV rejected vigo's token",
};

function warningTitle(code: string): string {
  return titles[code] ?? code.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase());
}
