import { useLiveState } from "@/lib/live";
import { cn } from "@/lib/utils";

export function LiveIndicator({ className, compact = false }: { className?: string; compact?: boolean }) {
  const live = useLiveState();
  const label = live === "live" ? "Live" : live === "connecting" ? "Connecting" : "Reconnecting";
  return (
    <span role="status" className={cn("inline-flex items-center gap-2 text-xs text-muted-foreground", className)} title={`Live updates: ${label}`}>
      <span className="relative flex size-2">
        {live !== "live" && <span className="absolute inline-flex size-full animate-ping rounded-full bg-warning opacity-60" />}
        <span className={cn("relative inline-flex size-2 rounded-full", live === "live" ? "bg-success" : "bg-warning")} />
      </span>
      <span className={cn(compact && "sr-only")}>{label}</span>
    </span>
  );
}
