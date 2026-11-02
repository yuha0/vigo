import { cn } from "@/lib/utils";

/** The vigo mark: a framed landscape, matching the app icon. */
export function Mark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 40 40" aria-hidden="true" className={cn("size-8", className)}>
      <rect width="40" height="40" rx="10" fill="#211f1c" />
      <rect x="7" y="11" width="26" height="17" rx="1" fill="#efe9df" />
      <rect x="8.6" y="12.6" width="22.8" height="13.8" fill="#ecd3ad" />
      <circle cx="25" cy="17.5" r="2.6" fill="#c9783f" />
      <path d="M8.6 23 C 13 20.5, 17 20.8, 21 22.5 S 28 24.6, 31.4 23.2 V 26.4 H 8.6 Z" fill="#5d6a53" />
      <rect x="17.5" y="30.5" width="5" height="1" rx="0.5" fill="#8a8178" />
    </svg>
  );
}
