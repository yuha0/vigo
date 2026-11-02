import { useQueryClient } from "@tanstack/react-query";
import { Check, CloudOff, Lightbulb, RefreshCw, Sparkles, ToggleRight, Tv, type LucideIcon } from "lucide-react";
import { useId } from "react";
import { toast } from "sonner";

import { Page, PageHeader } from "@/components/Page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { AccessoryKind, Protocol, type Accessory, type PairingGroup, type Provider } from "@/gen/vigo/home/v1/home_pb";
import { QrCode } from "@/features/apple-home/QrCode";
import { ResetPairingDialog } from "@/features/apple-home/ResetPairingDialog";
import { useAccessories, useGroups, useProvider, useResetPairing, useSetAccessoryEnabled } from "@/features/apple-home/queries";
import { formatSetupCode } from "@/features/apple-home/setup-code";
import { describeError, isUnavailable } from "@/lib/errors";
import { qk } from "@/lib/queries";

const protocolName: Record<Protocol, string> = {
  [Protocol.UNSPECIFIED]: "Unknown",
  [Protocol.HOMEKIT]: "HomeKit",
  [Protocol.MATTER]: "Matter",
};

export function AppleHomeScreen() {
  const provider = useProvider();
  const ready = !!provider.data?.implemented;
  const groups = useGroups(ready);
  const accessories = useAccessories(ready);
  const qc = useQueryClient();
  const retry = () => void qc.refetchQueries({ queryKey: qk.home });

  const offline = [provider.error, groups.error, accessories.error].some(isUnavailable);
  const otherError = !offline ? (provider.error ?? groups.error ?? accessories.error) : null;

  let body;
  if (offline) {
    body = <OfflineState onRetry={retry} />;
  } else if (otherError) {
    const d = describeError(otherError);
    body = <Notice title={d?.title ?? "Something went wrong"} body={d?.description ?? ""} onRetry={retry} />;
  } else if (provider.isPending) {
    body = <Skeleton className="h-72 w-full rounded-2xl" />;
  } else if (!provider.data?.implemented) {
    body = <ComingSoon provider={provider.data ?? null} />;
  } else if (groups.isPending || accessories.isPending) {
    body = (
      <div className="grid gap-6">
        <Skeleton className="h-72 w-full rounded-2xl" />
        <Skeleton className="h-48 w-full rounded-2xl" />
      </div>
    );
  } else {
    const byGroup = new Map<string, Accessory[]>();
    for (const a of accessories.data?.accessories ?? []) byGroup.set(a.pairingGroupId, [...(byGroup.get(a.pairingGroupId) ?? []), a]);
    body = (
      <div className="grid gap-8">
        {(groups.data?.groups ?? []).map((g) => (
          <GroupCard key={g.id} group={g} accessories={byGroup.get(g.id) ?? []} />
        ))}
      </div>
    );
  }

  return (
    <Page>
      <PageHeader
        eyebrow="Integrations"
        title="Apple Home"
        description="The Frame as a TV tile with Control Center remote, plus optional switches for Art Mode."
        actions={provider.data && <ProviderBadge provider={provider.data} />}
      />
      {body}
    </Page>
  );
}

function ProviderBadge({ provider }: { provider: Provider }) {
  return (
    <div className="flex flex-wrap gap-2">
      <Badge variant="outline">{protocolName[provider.protocol] ?? "Unknown"}</Badge>
      {provider.implemented && (
        <Badge variant={provider.coreConnected ? "success" : "warning"}>{provider.coreConnected ? "Connected to vigo" : "Can't reach vigo-core"}</Badge>
      )}
    </div>
  );
}

function OfflineState({ onRetry }: { onRetry: () => void }) {
  return (
    <Card className="animate-fade-up" data-testid="home-offline">
      <CardContent className="flex flex-col items-start gap-5 p-6 sm:p-8">
        <span className="flex size-14 items-center justify-center rounded-2xl bg-muted">
          <CloudOff className="size-6" strokeWidth={1.75} />
        </span>
        <div>
          <h2 className="font-display text-3xl">HomeKit service offline</h2>
          <p className="mt-2.5 max-w-prose text-[0.9375rem] text-muted-foreground">
            vigo-home isn't running or can't be reached, so Apple Home can't be configured right now. Your TV controls here still work, and
            existing Home app pairings come back when the service does.
          </p>
        </div>
        <Button variant="outline" onClick={onRetry}>
          <RefreshCw /> Try again
        </Button>
      </CardContent>
    </Card>
  );
}

function Notice({ title, body, onRetry }: { title: string; body: string; onRetry: () => void }) {
  return (
    <Card>
      <CardContent className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <p className="font-medium">{title}</p>
          {body && <p className="mt-1 text-sm text-muted-foreground">{body}</p>}
        </div>
        <Button variant="outline" onClick={onRetry}>
          <RefreshCw /> Retry
        </Button>
      </CardContent>
    </Card>
  );
}

function ComingSoon({ provider }: { provider: Provider | null }) {
  const name = provider ? protocolName[provider.protocol] : "This provider";
  return (
    <Card className="animate-fade-up">
      <CardContent className="flex flex-col items-start gap-5 p-6 sm:p-8">
        <span className="flex size-14 items-center justify-center rounded-2xl bg-accent text-brass">
          <Sparkles className="size-6" strokeWidth={1.75} />
        </span>
        <div>
          <Badge variant="outline" className="mb-3">
            Coming soon
          </Badge>
          <h2 className="font-display text-3xl">{name} support is on its way</h2>
          <p className="mt-2.5 max-w-prose text-[0.9375rem] text-muted-foreground">
            vigo-home is running with the {name} provider, which isn't available yet. Switch vigo-home to HomeKit to add the TV to Apple Home today.
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

const kindIcon: Record<AccessoryKind, LucideIcon> = {
  [AccessoryKind.UNSPECIFIED]: ToggleRight,
  [AccessoryKind.TELEVISION]: Tv,
  [AccessoryKind.SWITCH]: ToggleRight,
  [AccessoryKind.LIGHTBULB]: Lightbulb,
};

function GroupCard({ group, accessories }: { group: PairingGroup; accessories: Accessory[] }) {
  const reset = useResetPairing();
  const showCode = !group.paired && !!group.setupUri;

  return (
    <Card className="overflow-hidden animate-fade-up" data-group={group.id}>
      <div className="flex flex-wrap items-start justify-between gap-3 px-5 pt-5 sm:px-6 sm:pt-6">
        <div>
          <h2 className="font-display text-2xl">{group.name}</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            {group.paired
              ? "Added to Apple Home."
              : group.advertised
                ? "Ready to add to Apple Home."
                : "Not advertised. It appears in the Home app once at least one of its accessories is enabled."}
          </p>
        </div>
        <Badge variant={group.paired ? "success" : group.advertised ? "warning" : "default"}>
          {group.paired && <Check />}
          {group.paired ? "Paired" : group.advertised ? "Not paired" : "Hidden"}
        </Badge>
      </div>

      {showCode && (
        <div className="mx-5 mt-5 flex flex-col items-center gap-5 rounded-2xl bg-muted/50 p-5 sm:mx-6 sm:flex-row sm:items-center sm:gap-7">
          <div className="rounded-xl bg-white p-3 shadow-sm ring-1 ring-black/5">
            <QrCode value={group.setupUri} label={`HomeKit setup code for ${group.name}`} className="size-40 sm:size-44" />
          </div>
          <div className="text-center sm:text-left">
            <p className="text-xs font-medium tracking-[0.14em] text-muted-foreground uppercase">Setup code</p>
            <p className="mt-1 font-mono text-3xl font-medium tracking-[0.12em] tabular-nums" aria-label={`Setup code ${group.setupCode.split("").join(" ")}`}>
              {formatSetupCode(group.setupCode)}
            </p>
            <p className="mt-3 max-w-xs text-sm text-muted-foreground">
              In the Home app, tap <strong className="font-medium text-foreground">+</strong>, then{" "}
              <strong className="font-medium text-foreground">Add Accessory</strong>, and scan this code. Or enter the numbers by hand.
            </p>
          </div>
        </div>
      )}

      {accessories.length > 0 && (
        <ul className="mt-3 divide-y divide-border/70 px-5 sm:px-6">
          {accessories.map((a) => (
            <AccessoryRow key={a.id} accessory={a} />
          ))}
        </ul>
      )}

      {group.paired && (
        <div className="mt-2 flex flex-wrap items-center justify-between gap-3 border-t border-border/70 bg-muted/30 px-5 py-4 sm:px-6">
          <p className="max-w-md text-sm text-muted-foreground">Starting over? Resetting issues a new setup code.</p>
          <ResetPairingDialog
            group={group}
            pending={reset.isPending}
            onConfirm={() =>
              reset.mutate(group.id, {
                onSuccess: () => toast.success(`${group.name} pairing reset`, { description: "Scan the new code in the Home app to add it again." }),
              })
            }
          />
        </div>
      )}
      {!group.paired && <div className="h-3" />}
    </Card>
  );
}

function AccessoryRow({ accessory: a }: { accessory: Accessory }) {
  const set = useSetAccessoryEnabled();
  const id = useId();
  const Icon = kindIcon[a.kind] ?? ToggleRight;
  return (
    <li className="flex items-center gap-4 py-4">
      <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-muted">
        <Icon className="size-5" strokeWidth={1.75} />
      </span>
      <div className="min-w-0 flex-1">
        <label htmlFor={id} className="font-medium">
          {a.name}
        </label>
        <p className="mt-0.5 text-sm text-muted-foreground">{a.description}</p>
      </div>
      {a.canDisable ? (
        <Switch id={id} checked={a.enabled} onCheckedChange={(v) => set.mutate({ id: a.id, enabled: v })} />
      ) : (
        <span className="flex items-center gap-3">
          <span className="hidden text-xs text-muted-foreground sm:inline">Always on</span>
          <Switch id={id} checked={a.enabled} disabled aria-describedby={`${id}-always`} />
          <span id={`${id}-always`} className="sr-only">
            Always on
          </span>
        </span>
      )}
    </li>
  );
}
