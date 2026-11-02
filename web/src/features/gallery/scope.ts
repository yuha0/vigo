import { useCallback, useState } from "react";

import { ArtworkOrigin } from "@/gen/vigo/core/v1/library_pb";

export type Scope = { store: boolean; local: boolean };

export function scopeOrigin(s: Scope): ArtworkOrigin {
  if (s.store && s.local) return ArtworkOrigin.UNSPECIFIED;
  return s.local ? ArtworkOrigin.SOURCES : ArtworkOrigin.TV_ONLY;
}

const KEY = "vigo.gallery.scope";
const BOTH: Scope = { store: true, local: true };

function load(): Scope {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? "null") as Partial<Scope> | null;
    if (v && typeof v.store === "boolean" && typeof v.local === "boolean" && (v.store || v.local)) return { store: v.store, local: v.local };
  } catch {
    // Storage can be unavailable (private mode, blocked); the default is fine.
  }
  return BOTH;
}

function save(s: Scope) {
  try {
    localStorage.setItem(KEY, JSON.stringify(s));
  } catch {
    // Not remembering the choice is acceptable.
  }
}

/**
 * Which of Art Store and Local the gallery shows, remembered per device. At
 * least one stays on: turning off the only active one does nothing.
 */
export function useScope() {
  const [scope, setScope] = useState<Scope>(load);
  const toggle = useCallback((which: keyof Scope) => {
    setScope((cur) => {
      const next = { ...cur, [which]: !cur[which] };
      if (!next.store && !next.local) return cur;
      save(next);
      return next;
    });
  }, []);
  return { scope, toggle };
}
