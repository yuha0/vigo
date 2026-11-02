import { ArrowLeft, ChevronDown, ChevronLeft, ChevronRight, ChevronUp, ExternalLink, House, Minus, Plus, VolumeX } from "lucide-react";
import { useCallback, useEffect, useMemo, type ReactNode } from "react";

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
      if (target?.closest("input, textarea, select, [contenteditable], button, [role=dialog]")) return;
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
