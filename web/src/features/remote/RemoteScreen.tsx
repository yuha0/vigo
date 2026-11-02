import {
  ArrowLeft,
  CalendarDays,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  ExternalLink,
  Grid3x3,
  House,
  Import,
  ListVideo,
  Minus,
  Plus,
  Settings,
  VolumeX,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useState, type ReactNode } from "react";

import { Page, PageHeader } from "@/components/Page";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { KeyAction } from "@/gen/vigo/core/v1/core_pb";
import { useRemoteKey, useSendKey } from "@/features/remote/useRemoteKey";
import { isIOS } from "@/lib/platform";
import { cn } from "@/lib/utils";

const keyboardMap: Record<string, string> = {
  ArrowUp: "KEY_UP",
  ArrowDown: "KEY_DOWN",
  ArrowLeft: "KEY_LEFT",
  ArrowRight: "KEY_RIGHT",
  Enter: "KEY_ENTER",
  Backspace: "KEY_RETURN",
  Escape: "KEY_RETURN",
  ...Object.fromEntries(Array.from({ length: 10 }, (_, d) => [String(d), `KEY_${d}`])),
};

/**
 * The virtual remote deliberately has no power key: KEY_POWER's effect depends
 * on the TV's state and can power it fully off. "Show art" is the only way to wake it.
 */
export function RemoteScreen() {
  const sendKey = useSendKey();
  const mutate = sendKey.mutate;
  const send = useCallback((key: string, action: KeyAction) => mutate({ key, action }), [mutate]);
  const ios = useMemo(() => isIOS(), []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (e.repeat || e.metaKey || e.ctrlKey || e.altKey) return;
      if (target?.closest("input, textarea, select, [contenteditable], [role=dialog]")) return;
      // A focused button already turns Enter into its own click.
      if (e.key === "Enter" && target?.closest("button")) return;
      const key = keyboardMap[e.key];
      if (!key) return;
      e.preventDefault();
      send(key, KeyAction.CLICK);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [send]);

  return (
    <Page className="max-w-xl">
      <PageHeader eyebrow="Remote" title="Remote" description="Hold a button to repeat it. Arrow keys, Enter and Backspace work too." />

      {ios && (
        <Card className="mb-8 animate-fade-up">
          <CardContent className="grid gap-3">
            <Button size="lg" className="w-full" asChild>
              <a href="tvremote://">
                Open iPhone Remote <ExternalLink />
              </a>
            </Button>
            <p className="text-sm text-muted-foreground">
              Uses the Apple TV Remote built into Control Center. The TV must be added to Apple Home first; see Integrations › Apple Home.
            </p>
          </CardContent>
        </Card>
      )}

      <div className="mx-auto flex max-w-[22rem] flex-col items-center gap-8 pb-6 animate-fade-up" aria-label="Virtual remote" role="group">
        <div className="flex w-full justify-between">
          <RoundKey k="KEY_RETURN" label="Back" send={send}>
            <ArrowLeft />
          </RoundKey>
          <RoundKey k="KEY_HOME" label="Home" send={send}>
            <House />
          </RoundKey>
        </div>

        <DPad send={send} />

        <div className="flex w-full items-stretch justify-between gap-4">
          <Rocker up={{ k: "KEY_VOLUP", label: "Volume up" }} down={{ k: "KEY_VOLDOWN", label: "Volume down" }} caption="Vol" send={send} plusMinus />
          <div className="flex flex-col items-center justify-center">
            <RoundKey k="KEY_MUTE" label="Mute" send={send}>
              <VolumeX />
            </RoundKey>
          </div>
          <Rocker up={{ k: "KEY_CHUP", label: "Channel up" }} down={{ k: "KEY_CHDOWN", label: "Channel down" }} caption="Ch" send={send} />
        </div>
      </div>

      <MoreKeys send={send} />
    </Page>
  );
}

type SendFn = (key: string, action: KeyAction) => void;

const surface =
  "select-none touch-none outline-none transition-[background-color,transform,box-shadow] duration-150 focus-visible:ring-[3px] focus-visible:ring-ring/50 active:scale-[0.96]";
const keyFace =
  "bg-card text-foreground shadow-[0_1px_2px_rgb(0_0_0/0.06),0_6px_16px_-8px_rgb(0_0_0/0.18)] ring-1 ring-border/70 active:bg-accent dark:shadow-[0_1px_0_rgb(255_255_255/0.04)_inset,0_8px_20px_-10px_rgb(0_0_0/0.6)]";

function RoundKey({ k, label, send, children, className }: { k: string; label: string; send: SendFn; children: ReactNode; className?: string }) {
  const handlers = useRemoteKey(k, send);
  return (
    <button type="button" aria-label={label} data-key={k} className={cn(surface, keyFace, "flex size-16 items-center justify-center rounded-full [&_svg]:size-6", className)} {...handlers}>
      {children}
    </button>
  );
}

function DPad({ send }: { send: SendFn }) {
  const arrow = "absolute flex items-center justify-center text-muted-foreground hover:text-foreground [&_svg]:size-7";
  return (
    <div className={cn("relative size-[17rem] rounded-full", keyFace, "active:bg-card")}>
      <DirKey k="KEY_UP" label="Up" send={send} className={cn(arrow, "inset-x-16 top-0 h-[5.25rem] rounded-t-full")}>
        <ChevronUp />
      </DirKey>
      <DirKey k="KEY_DOWN" label="Down" send={send} className={cn(arrow, "inset-x-16 bottom-0 h-[5.25rem] rounded-b-full")}>
        <ChevronDown />
      </DirKey>
      <DirKey k="KEY_LEFT" label="Left" send={send} className={cn(arrow, "inset-y-16 left-0 w-[5.25rem] rounded-l-full")}>
        <ChevronLeft />
      </DirKey>
      <DirKey k="KEY_RIGHT" label="Right" send={send} className={cn(arrow, "inset-y-16 right-0 w-[5.25rem] rounded-r-full")}>
        <ChevronRight />
      </DirKey>
      <DirKey
        k="KEY_ENTER"
        label="OK"
        send={send}
        className="absolute inset-[5.25rem] flex items-center justify-center rounded-full bg-primary text-sm font-semibold tracking-wide text-primary-foreground shadow-md"
      >
        OK
      </DirKey>
    </div>
  );
}

function DirKey({ k, label, send, className, children }: { k: string; label: string; send: SendFn; className?: string; children: ReactNode }) {
  const handlers = useRemoteKey(k, send);
  return (
    <button type="button" aria-label={label} data-key={k} className={cn(surface, "active:bg-accent/70", className)} {...handlers}>
      {children}
    </button>
  );
}

function Rocker({
  up,
  down,
  caption,
  send,
  plusMinus = false,
}: {
  up: { k: string; label: string };
  down: { k: string; label: string };
  caption: string;
  send: SendFn;
  plusMinus?: boolean;
}) {
  return (
    <div className={cn("flex w-20 flex-col items-center overflow-hidden rounded-full", keyFace, "active:bg-card")}>
      <RockerHalf k={up.k} label={up.label} send={send}>
        {plusMinus ? <Plus /> : <ChevronUp />}
      </RockerHalf>
      <span className="py-1 text-[0.6875rem] font-medium tracking-[0.14em] text-muted-foreground uppercase" aria-hidden="true">
        {caption}
      </span>
      <RockerHalf k={down.k} label={down.label} send={send}>
        {plusMinus ? <Minus /> : <ChevronDown />}
      </RockerHalf>
    </div>
  );
}

function RockerHalf({ k, label, send, children }: { k: string; label: string; send: SendFn; children: ReactNode }) {
  const handlers = useRemoteKey(k, send);
  return (
    <button type="button" aria-label={label} data-key={k} className={cn(surface, "flex h-16 w-full items-center justify-center active:bg-accent [&_svg]:size-6")} {...handlers}>
      {children}
    </button>
  );
}

/*
 * The extra keys come from the samsungtvws key list. Only KEY_UP, KEY_ENTER,
 * KEY_HOME and KEY_VOLUP have been observed on this TV, so the labels name the
 * key, not what it does on screen. There is deliberately no INFO key.
 */
const functionKeys: { k: string; label: string; short: string; icon: LucideIcon }[] = [
  { k: "KEY_MENU", label: "Menu", short: "Menu", icon: Settings },
  { k: "KEY_TOOLS", label: "Tools", short: "Tools", icon: Wrench },
  { k: "KEY_GUIDE", label: "Guide", short: "Guide", icon: CalendarDays },
  { k: "KEY_SOURCE", label: "Source", short: "Source", icon: Import },
  { k: "KEY_CH_LIST", label: "Channel list", short: "Ch list", icon: ListVideo },
];

const colourKeys: { k: string; label: string; className: string }[] = [
  { k: "KEY_RED", label: "Red", className: "bg-[#c8372d] text-white" },
  { k: "KEY_GREEN", label: "Green", className: "bg-[#2f8a47] text-white" },
  { k: "KEY_YELLOW", label: "Yellow", className: "bg-[#e8c13a] text-neutral-900" },
  { k: "KEY_BLUE", label: "Blue", className: "bg-[#2f63c4] text-white" },
];

function MoreKeys({ send }: { send: SendFn }) {
  const [padOpen, setPadOpen] = useState(false);
  const padId = useId();
  return (
    <div className="mx-auto mt-6 grid max-w-[22rem] gap-6 animate-fade-up">
      <section aria-label="Function keys" className="grid grid-cols-5 gap-2">
        {functionKeys.map((f) => (
          <PillKey key={f.k} k={f.k} label={f.label} send={send} className="flex-col gap-1 py-2 text-[0.6875rem]">
            <f.icon className="size-5" strokeWidth={1.75} aria-hidden="true" />
            <span className="leading-none">{f.short}</span>
          </PillKey>
        ))}
      </section>

      <section aria-label="Colour keys" className="grid grid-cols-4 gap-2">
        {colourKeys.map((c) => (
          <PillKey key={c.k} k={c.k} label={c.label} send={send} className={cn("h-11 text-xs font-semibold ring-black/10 dark:ring-white/15", c.className)}>
            {c.label}
          </PillKey>
        ))}
      </section>

      <section aria-label="Number pad">
        <button
          type="button"
          aria-expanded={padOpen}
          aria-controls={padId}
          onClick={() => setPadOpen((o) => !o)}
          className="flex h-11 w-full items-center justify-center gap-2 rounded-full text-sm font-medium text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          <Grid3x3 className="size-4" aria-hidden="true" />
          {padOpen ? "Hide numbers" : "Numbers"}
          <ChevronDown className={cn("size-4 transition-transform", padOpen && "rotate-180")} aria-hidden="true" />
        </button>
        <div id={padId} hidden={!padOpen} className="mt-3 grid grid-cols-3 gap-2 animate-in fade-in slide-in-from-top-1">
          {["1", "2", "3", "4", "5", "6", "7", "8", "9", "", "0", ""].map((d, i) =>
            d ? (
              <PillKey key={d} k={`KEY_${d}`} label={d} send={send} className="h-12 font-display text-xl tabular-nums">
                {d}
              </PillKey>
            ) : (
              <span key={`gap${i}`} aria-hidden="true" />
            ),
          )}
        </div>
      </section>
    </div>
  );
}

function PillKey({ k, label, send, className, children }: { k: string; label: string; send: SendFn; className?: string; children: ReactNode }) {
  const handlers = useRemoteKey(k, send, { hold: false });
  return (
    <button
      type="button"
      aria-label={label}
      data-key={k}
      className={cn(surface, keyFace, "flex min-h-11 items-center justify-center rounded-2xl", className)}
      {...handlers}
    >
      {children}
    </button>
  );
}
