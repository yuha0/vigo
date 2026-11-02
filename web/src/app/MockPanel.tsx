import { FlaskConical } from "lucide-react";
import { Popover } from "radix-ui";
import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { homeModes, scenarios, type HomeMode, type MockControl, type Scenario } from "@/lib/mock/control";

/** Development-only switcher for the in-browser mock. */
export function MockPanel({ control }: { control: MockControl }) {
  const [scenario, setScenario] = useState<Scenario>(control.scenario);
  const [home, setHome] = useState<HomeMode>(control.homeMode);
  const [rateLimited, setRateLimited] = useState(false);

  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button
          className="fixed right-[max(0.75rem,env(safe-area-inset-right))] bottom-[max(0.75rem,env(safe-area-inset-bottom))] z-40 flex size-11 items-center justify-center rounded-full bg-popover/80 text-muted-foreground shadow-lg ring-1 ring-border backdrop-blur-xl hover:text-foreground"
          aria-label="Mock backend"
        >
          <FlaskConical className="size-5" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content side="top" align="end" sideOffset={10} className="z-50 w-72 rounded-2xl border bg-popover p-4 shadow-2xl">
          <p className="mb-3 text-xs font-medium tracking-[0.14em] text-muted-foreground uppercase">Mock backend</p>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label>TV scenario</Label>
              <Select
                value={scenario}
                onValueChange={(v) => {
                  setScenario(v as Scenario);
                  control.setScenario(v as Scenario);
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {scenarios.map((s) => (
                    <SelectItem key={s} value={s}>
                      {s}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>vigo-home</Label>
              <Select
                value={home}
                onValueChange={(v) => {
                  setHome(v as HomeMode);
                  control.setHomeMode(v as HomeMode);
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {homeModes.map((m) => (
                    <SelectItem key={m} value={m}>
                      {m}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5 rounded-xl bg-muted/60 px-3 py-2.5 text-sm">
              <span className="text-muted-foreground">Password</span>
              <code className="font-mono select-all">{control.devPassword}</code>
            </div>
            <Button variant="outline" onClick={() => control.expireSession()}>
              Expire session
            </Button>
            <Button
              variant="outline"
              aria-pressed={rateLimited}
              onClick={() => {
                control.setRateLimited(!rateLimited);
                setRateLimited(!rateLimited);
              }}
            >
              {rateLimited ? "Stop rate limiting" : "Rate-limit sign-in"}
            </Button>
            <Button variant="outline" onClick={() => control.dropStreams()}>
              Drop live stream
            </Button>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
