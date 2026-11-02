import { Blocks, CircleDot, House, HousePlug, LayoutGrid, Tv, type LucideIcon } from "lucide-react";

export type NavLink = { to: string; label: string; icon: LucideIcon };

/** A nav entry that opens a sub-menu; its route prefix is `base`. */
export type NavGroup = { base: string; label: string; icon: LucideIcon; children: NavLink[] };

export type NavEntry = NavLink | NavGroup;

export function isGroup(e: NavEntry): e is NavGroup {
  return "children" in e;
}

/** Each integration is a sub-item of "Integrations"; Matter would be added here. */
export const integrations: NavLink[] = [{ to: "/integrations/apple-home", label: "Apple Home", icon: HousePlug }];

export const navItems: NavEntry[] = [
  { to: "/", label: "Home", icon: House },
  { to: "/tv", label: "TV", icon: Tv },
  { to: "/gallery", label: "Gallery", icon: LayoutGrid },
  { base: "/integrations", label: "Integrations", icon: Blocks, children: integrations },
  { to: "/remote", label: "Remote", icon: CircleDot },
];
