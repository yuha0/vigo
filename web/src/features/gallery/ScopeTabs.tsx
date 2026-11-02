import { Folder, Store } from "lucide-react";

import type { Scope } from "@/features/gallery/scope";
import { cn } from "@/lib/utils";

const tabs: { id: keyof Scope; label: string; hint: string; icon: typeof Store }[] = [
  { id: "store", label: "Art Store", hint: "Samsung art and uploads made elsewhere", icon: Store },
  { id: "local", label: "Local", hint: "From your folders, managed by vigo", icon: Folder },
];

/** Two large toggles; either or both can be on, never neither. */
export function ScopeTabs({
  scope,
  counts,
  onToggle,
}: {
  scope: Scope;
  counts?: { store: number; local: number };
  onToggle: (which: keyof Scope) => void;
}) {
  return (
    <div role="group" aria-label="Show" className="mb-5 grid grid-cols-2 gap-1 rounded-2xl bg-muted p-1">
      {tabs.map((t) => {
        const on = scope[t.id];
        const last = on && !scope[t.id === "store" ? "local" : "store"];
        return (
          <button
            key={t.id}
            type="button"
            aria-pressed={on}
            aria-label={`${t.label}${counts ? `, ${counts[t.id]}` : ""}`}
            title={last ? "At least one stays on" : t.hint}
            onClick={() => onToggle(t.id)}
            className={cn(
              "flex min-h-14 items-center justify-center gap-2.5 rounded-xl px-3 text-[0.9375rem] font-medium outline-none transition-all duration-200 focus-visible:ring-[3px] focus-visible:ring-ring/50",
              on ? "bg-card text-foreground shadow-[0_1px_3px_rgb(0_0_0/0.12)]" : "text-muted-foreground hover:text-foreground",
              last && "cursor-default",
            )}
          >
            <t.icon className={cn("size-5 shrink-0", on ? "text-brass" : "opacity-60")} strokeWidth={1.75} aria-hidden="true" />
            <span>{t.label}</span>
            {counts && <span className="tabular-nums text-muted-foreground">{counts[t.id]}</span>}
          </button>
        );
      })}
    </div>
  );
}
