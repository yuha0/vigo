import { create } from "@bufbuild/protobuf";
import { LoaderCircle } from "lucide-react";
import { useId, useState, type FormEvent } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Segmented } from "@/components/ui/segmented";
import { Skeleton } from "@/components/ui/skeleton";
import { ConfigSchema, type Config } from "@/gen/vigo/core/v1/core_pb";
import { useConfig, useUpdateConfig } from "@/features/tv/queries";
import { describeError } from "@/lib/errors";

export function ConnectionCard() {
  const config = useConfig();
  return (
    <Card>
      <CardContent>
        {config.isPending ? (
          <div className="grid gap-4">
            <Skeleton className="h-11 w-full" />
            <Skeleton className="h-11 w-full" />
          </div>
        ) : config.isError ? (
          <p className="text-[0.9375rem] text-muted-foreground">{describeError(config.error)?.title ?? "Couldn't load the connection settings."}</p>
        ) : (
          <ConnectionForm key={`${config.data?.tvHost}:${config.data?.tvPort}:${config.data?.clientName}`} config={config.data} />
        )}
      </CardContent>
    </Card>
  );
}

function ConnectionForm({ config }: { config: Config | null }) {
  const update = useUpdateConfig();
  const [host, setHost] = useState(config?.tvHost ?? "");
  const [port, setPort] = useState(String(config?.tvPort || 8002));
  const [name, setName] = useState(config?.clientName ?? "");
  const ids = { host: useId(), name: useId(), nameHint: useId() };

  const dirty = host !== (config?.tvHost ?? "") || Number(port) !== (config?.tvPort || 8002) || name !== (config?.clientName ?? "");
  const valid = host.trim() !== "" && name.trim() !== "";
  const nameChanged = name.trim() !== (config?.clientName ?? "");

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!dirty || !valid) return;
    const reconnect = host.trim() !== config?.tvHost || Number(port) !== config?.tvPort;
    update.mutate(create(ConfigSchema, { tvHost: host.trim(), tvPort: Number(port), clientName: name.trim() }), {
      onSuccess: () =>
        toast.success("Connection saved", {
          description: reconnect ? "vigo is reconnecting to the TV." : nameChanged ? "Pair again so the TV knows the new name." : undefined,
        }),
    });
  };

  return (
    <form className="grid gap-5" onSubmit={submit}>
      <div className="grid gap-2">
        <Label htmlFor={ids.host}>TV address</Label>
        <Input
          id={ids.host}
          value={host}
          onChange={(e) => setHost(e.target.value)}
          placeholder="192.168.1.20"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          inputMode="url"
        />
      </div>
      <div className="grid gap-2">
        <Label asChild>
          <span>Port</span>
        </Label>
        <Segmented
          aria-label="Port"
          value={port}
          onValueChange={setPort}
          className="sm:max-w-sm"
          options={[
            { value: "8002", label: "8002 · TLS" },
            { value: "8001", label: "8001 · Plain" },
          ]}
        />
      </div>
      <div className="grid gap-2">
        <Label htmlFor={ids.name}>Client name</Label>
        <Input
          id={ids.name}
          value={name}
          onChange={(e) => setName(e.target.value)}
          aria-describedby={ids.nameHint}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
        />
        <p id={ids.nameHint} className="text-sm text-muted-foreground">
          Shown on the TV and in its Device Connection Manager. Changing it means pairing again.
        </p>
      </div>
      <div className="flex justify-end">
        <Button type="submit" disabled={!dirty || !valid || update.isPending}>
          {update.isPending && <LoaderCircle className="animate-spin" />}
          Save
        </Button>
      </div>
    </form>
  );
}
