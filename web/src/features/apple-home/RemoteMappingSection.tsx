import { create } from "@bufbuild/protobuf";
import {
  ArrowDown,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  CircleDot,
  CircleHelp,
  CornerUpLeft,
  Info,
  LoaderCircle,
  Play,
  Power,
  Volume1,
  Volume2,
  VolumeX,
  type LucideIcon,
} from "lucide-react";
import { useId, type ReactNode } from "react";
import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  RemoteAction,
  RemoteBindingSchema,
  RemoteButton,
  type GetRemoteMappingResponse,
  type KeyOption,
  type RemoteBinding,
  type RemoteTarget,
} from "@/gen/vigo/home/v1/home_pb";
import { useResetRemoteMapping, useUpdateRemoteBinding } from "@/features/apple-home/queries";

type ButtonMeta = { label: string; icon: LucideIcon; hint?: string };

const buttons: Record<RemoteButton, ButtonMeta> = {
  [RemoteButton.UNSPECIFIED]: { label: "Unknown button", icon: CircleHelp },
  [RemoteButton.POWER_ON]: { label: "When the TV shows as off", icon: Power },
  [RemoteButton.POWER_OFF]: { label: "When the TV shows as on", icon: Power },
  [RemoteButton.ARROW_UP]: { label: "Up", icon: ArrowUp },
  [RemoteButton.ARROW_DOWN]: { label: "Down", icon: ArrowDown },
  [RemoteButton.ARROW_LEFT]: { label: "Left", icon: ArrowLeft },
  [RemoteButton.ARROW_RIGHT]: { label: "Right", icon: ArrowRight },
  [RemoteButton.SELECT]: { label: "Select (centre)", icon: CircleDot },
  [RemoteButton.BACK]: { label: "Back", icon: CornerUpLeft },
  [RemoteButton.PLAY_PAUSE]: { label: "Play/Pause", icon: Play },
  [RemoteButton.INFORMATION]: { label: "Info (i)", icon: Info },
  [RemoteButton.VOLUME_UP]: { label: "Volume up", icon: Volume2, hint: "The phone's volume-up button while the remote is open" },
  [RemoteButton.VOLUME_DOWN]: { label: "Volume down", icon: Volume1, hint: "The phone's volume-down button while the remote is open" },
  [RemoteButton.MUTE]: { label: "Mute", icon: VolumeX },
};

const powerButtons = new Set([RemoteButton.POWER_ON, RemoteButton.POWER_OFF]);


const actions: { action: RemoteAction; label: string }[] = [
  { action: RemoteAction.NONE, label: "Nothing" },
  { action: RemoteAction.TOGGLE_ART_MODE, label: "Toggle Art Mode" },
  { action: RemoteAction.SHOW_ART, label: "Show art" },
];

/** Select values: "action:<enum number>" or "key:<KEY_CODE>". */
function encode(t: RemoteTarget | undefined): string {
  if (t?.target.case === "key") return `key:${t.target.value}`;
  if (t?.target.case === "action") return `action:${t.target.value}`;
  return "";
}

function decode(v: string): RemoteTarget["target"] {
  if (v.startsWith("key:")) return { case: "key", value: v.slice(4) };
  return { case: "action", value: Number(v.slice(7)) as RemoteAction };
}

// One grid for every row: the phone's button on the left, a fixed-width picker on the right.
// On narrow screens the picker sits under its label, full width.
const rowGrid = "grid grid-cols-1 items-center gap-x-4 gap-y-2 py-3 sm:grid-cols-[minmax(0,1fr)_13.5rem]";

export function RemoteMappingSection({ mapping }: { mapping: GetRemoteMappingResponse | undefined }) {
  if (!mapping) {
    return <Skeleton className="h-96 w-full rounded-2xl" />;
  }
  const power = mapping.bindings.filter((b) => powerButtons.has(b.button));
  const rest = mapping.bindings.filter((b) => !powerButtons.has(b.button));

  return (
    <Card className="overflow-hidden animate-fade-up" aria-labelledby="remote-buttons-title">
      <div className="flex flex-wrap items-start justify-between gap-3 px-5 pt-5 sm:px-6 sm:pt-6">
        <div className="max-w-prose">
          <h2 id="remote-buttons-title" className="font-display text-2xl">
            Remote app button mapping
          </h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Apple draws the remote in Control Center, so buttons can't be added or removed. Each one can do something different on the TV, though.
          </p>
        </div>
        <ResetButton />
      </div>
      <ul className="mt-3 divide-y divide-border/70 px-5 sm:px-6" aria-label="Remote buttons">
        {power.length > 0 && (
          <li className="py-3" data-button="POWER">
            <ButtonName icon={Power} heading>
              Power button
            </ButtonName>
            <ul aria-label="Power button" className="divide-y divide-border/50">
              {power.map((b) => (
                <BindingRow key={b.button} binding={b} keys={mapping.keys} nested />
              ))}
            </ul>
            <p className="text-sm text-muted-foreground sm:pl-15">
              There is no verified way to power this TV off remotely; Toggle Art Mode switches to TV mode.
            </p>
          </li>
        )}
        {rest.map((b) => (
          <BindingRow key={b.button} binding={b} keys={mapping.keys} />
        ))}
      </ul>
      <div className="h-3" />
    </Card>
  );
}

function ButtonName({ icon: Icon, heading = false, children }: { icon: LucideIcon; heading?: boolean; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-center gap-4">
      <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-muted">
        <Icon className="size-5" strokeWidth={1.75} aria-hidden="true" />
      </span>
      {heading ? <h3 className="font-medium">{children}</h3> : <div className="min-w-0">{children}</div>}
    </div>
  );
}

function BindingRow({ binding, keys, nested = false }: { binding: RemoteBinding; keys: KeyOption[]; nested?: boolean }) {
  const update = useUpdateRemoteBinding();
  const id = useId();
  const meta = buttons[binding.button] ?? buttons[RemoteButton.UNSPECIFIED];
  const value = encode(binding.target);
  const saving = update.isPending;
  const name = nested ? `Power, ${meta.label.charAt(0).toLowerCase()}${meta.label.slice(1)}` : meta.label;

  const label = (
    <>
      <label htmlFor={id} className={nested ? "text-sm" : "font-medium"}>
        {meta.label}
      </label>
      {meta.hint && <p className="text-xs text-muted-foreground">{meta.hint}</p>}
    </>
  );

  return (
    <li className={rowGrid} data-button={RemoteButton[binding.button]}>
      {/* Nested power rows line their labels up with the other rows' labels, past the icon column. */}
      {nested ? <div className="min-w-0 sm:pl-15">{label}</div> : <ButtonName icon={meta.icon}>{label}</ButtonName>}
      <div className="flex items-center gap-1.5">
        <Select
          value={value}
          onValueChange={(v) => update.mutate(create(RemoteBindingSchema, { button: binding.button, target: { target: decode(v) } }))}
        >
          <SelectTrigger id={id} aria-label={`${name} does`} className="min-w-0 flex-1">
            <SelectValue placeholder="Not set" />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectLabel>Actions</SelectLabel>
              {actions.map((a) => (
                <SelectItem key={a.action} value={`action:${a.action}`}>
                  {a.label}
                </SelectItem>
              ))}
            </SelectGroup>
            <SelectSeparator />
            <SelectGroup>
              <SelectLabel>TV keys</SelectLabel>
              {keys.map((k) => (
                <SelectItem key={k.key} value={`key:${k.key}`} className="font-mono text-[0.875rem]">
                  {k.key}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <span className="flex size-5 shrink-0 items-center justify-center" aria-live="polite">
          {saving && <LoaderCircle className="size-4 animate-spin text-muted-foreground" aria-label="Saving" />}
        </span>
      </div>
    </li>
  );
}

function ResetButton() {
  const reset = useResetRemoteMapping();
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="outline" disabled={reset.isPending}>
          {reset.isPending && <LoaderCircle className="animate-spin" />}
          Reset to defaults
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Reset the remote buttons?</AlertDialogTitle>
          <AlertDialogDescription>Every button goes back to what it did when vigo was installed. Your changes are lost.</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep my changes</AlertDialogCancel>
          <AlertDialogAction onClick={() => reset.mutate(undefined, { onSuccess: () => toast.success("Remote buttons reset") })}>Reset</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
