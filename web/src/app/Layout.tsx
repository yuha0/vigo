import { ChevronDown, LogOut, ShieldCheck } from "lucide-react";
import { Suspense, useId, useState } from "react";
import { NavLink, Outlet, ScrollRestoration, useLocation } from "react-router";

import { isGroup, navItems, type NavGroup } from "@/app/nav";
import { LiveIndicator } from "@/components/LiveIndicator";
import { Mark } from "@/components/Mark";
import { useAuth } from "@/lib/auth";
import { useStatus } from "@/lib/queries";
import { cn } from "@/lib/utils";

/** One layout for every screen and every TV state: a left rail and the page. */
export function AppLayout() {
  const { data: status } = useStatus();
  const hasWarnings = (status?.warnings.length ?? 0) > 0;

  return (
    <div className="flex min-h-dvh">
      <Rail hasWarnings={hasWarnings} />
      <main className="min-w-0 flex-1 pt-safe pr-safe pb-safe">
        <Suspense fallback={null}>
          <Outlet />
        </Suspense>
      </main>
      <ScrollRestoration />
    </div>
  );
}

function inGroup(pathname: string, group: NavGroup) {
  return pathname === group.base || pathname.startsWith(`${group.base}/`);
}

const railItem =
  "group relative flex min-h-14 w-full flex-col items-center justify-center gap-1 rounded-2xl px-1 text-[0.6875rem] font-medium text-muted-foreground transition-colors duration-200 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 lg:min-h-11 lg:flex-row lg:justify-start lg:gap-3 lg:px-3.5 lg:text-[0.9375rem]";
const railActive = "bg-card text-foreground shadow-[0_1px_3px_rgb(0_0_0/0.06)]";
const railIdle = "hover:bg-card/60 hover:text-foreground";

function Rail({ hasWarnings }: { hasWarnings: boolean }) {
  return (
    <nav
      aria-label="Main"
      data-layout="rail"
      className="sticky top-0 z-30 flex h-dvh w-[calc(4.75rem+env(safe-area-inset-left,0px))] shrink-0 flex-col border-r border-border/60 bg-rail pt-safe pb-safe pl-safe lg:w-[calc(15rem+env(safe-area-inset-left,0px))]"
    >
      <div className="flex h-20 shrink-0 items-center justify-center gap-3 lg:justify-start lg:px-6">
        <Mark className="size-9" />
        <span className="hidden font-display text-2xl tracking-tight lg:inline">vigo</span>
      </div>
      <ul className="scrollbar-none flex flex-1 flex-col items-center gap-1.5 overflow-y-auto px-2 pt-2 lg:items-stretch lg:px-3">
        {navItems.map((item) =>
          isGroup(item) ? (
            <RailGroup key={item.base} group={item} />
          ) : (
            <li key={item.to} className="w-full">
              <NavLink to={item.to} end={item.to === "/"} className={({ isActive }) => cn(railItem, isActive ? railActive : railIdle)}>
                <span className="relative">
                  <item.icon className="size-[1.375rem] lg:size-5" strokeWidth={1.75} aria-hidden="true" />
                  {item.to === "/" && hasWarnings && (
                    <span className="absolute -top-0.5 -right-1 size-2 rounded-full bg-warning ring-2 ring-rail" aria-label="Warnings" />
                  )}
                </span>
                <span className="max-w-full truncate leading-tight">{item.label}</span>
              </NavLink>
            </li>
          ),
        )}
      </ul>
      <div className="flex shrink-0 flex-col items-center gap-2 px-2 pt-2 pb-5 lg:items-stretch lg:px-3">
        <NavLink to="/security" className={({ isActive }) => cn(railItem, isActive ? railActive : railIdle)}>
          <ShieldCheck className="size-[1.375rem] lg:size-5" strokeWidth={1.75} aria-hidden="true" />
          <span className="max-w-full truncate leading-tight">Security</span>
        </NavLink>
        <SignOutButton />
        <div className="flex justify-center pt-2 lg:justify-start lg:px-3">
          <LiveIndicator className="flex-col gap-1 text-[0.625rem] lg:flex-row lg:gap-2 lg:text-xs" />
        </div>
      </div>
    </nav>
  );
}

function SignOutButton() {
  const { logout } = useAuth();
  const [busy, setBusy] = useState(false);
  return (
    <button
      type="button"
      disabled={busy}
      onClick={() => {
        setBusy(true);
        void logout().finally(() => setBusy(false));
      }}
      className={cn(railItem, railIdle)}
    >
      <LogOut className="size-[1.375rem] lg:size-5" strokeWidth={1.75} aria-hidden="true" />
      <span className="max-w-full truncate leading-tight">Sign out</span>
    </button>
  );
}

/** An expandable rail entry; it opens by itself when one of its pages is showing. */
function RailGroup({ group }: { group: NavGroup }) {
  const { pathname } = useLocation();
  const active = inGroup(pathname, group);
  const [open, setOpen] = useState(active);
  const listId = useId();

  const [wasActive, setWasActive] = useState(active);
  if (active !== wasActive) {
    setWasActive(active);
    if (active) setOpen(true);
  }

  return (
    <li className="w-full">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={listId}
        onClick={() => setOpen((o) => !o)}
        className={cn(railItem, active && !open ? railActive : active ? "text-foreground" : railIdle)}
      >
        <group.icon className="size-[1.375rem] lg:size-5" strokeWidth={1.75} aria-hidden="true" />
        <span className="max-w-full truncate leading-tight">{group.label}</span>
        <ChevronDown
          aria-hidden="true"
          className={cn(
            "size-3 transition-transform duration-200 lg:ml-auto lg:size-4",
            open && "rotate-180",
          )}
        />
      </button>
      <ul
        id={listId}
        hidden={!open}
        aria-label={group.label}
        className="mt-1 flex flex-col gap-1 rounded-2xl bg-card/40 p-1 animate-in fade-in slide-in-from-top-1 duration-200 lg:ml-[1.4rem] lg:rounded-none lg:border-l lg:border-border lg:bg-transparent lg:py-0 lg:pr-0 lg:pl-2"
      >
        {group.children.map((child) => (
          <li key={child.to}>
            <NavLink
              to={child.to}
              className={({ isActive }) =>
                cn(
                  "flex min-h-11 w-full flex-col items-center justify-center gap-0.5 rounded-xl px-1 text-[0.625rem] font-medium text-muted-foreground transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
                  "lg:flex-row lg:justify-start lg:gap-3 lg:px-3 lg:text-sm",
                  isActive ? railActive : railIdle,
                )
              }
            >
              <child.icon className="size-[1.125rem] lg:size-4" strokeWidth={1.75} aria-hidden="true" />
              <span className="max-w-full truncate leading-tight">{child.label}</span>
            </NavLink>
          </li>
        ))}
      </ul>
    </li>
  );
}
