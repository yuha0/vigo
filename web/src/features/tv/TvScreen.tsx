import { useEffect } from "react";
import { useLocation } from "react-router";

import { Page, PageHeader, Section } from "@/components/Page";
import { WarningsBanner } from "@/components/WarningsBanner";
import { ChangePasswordCard } from "@/features/auth/ChangePasswordCard";
import { ArtModeCard } from "@/features/tv/ArtModeCard";
import { ConnectionCard } from "@/features/tv/ConnectionCard";
import { DeviceCard } from "@/features/tv/DeviceCard";
import { PairingCard } from "@/features/tv/PairingCard";
import { SettingsCard } from "@/features/tv/SettingsCard";
import { useStatus } from "@/lib/queries";
import { tvState } from "@/lib/tv-state";

export function TvScreen() {
  const { data } = useStatus();
  const status = data ?? null;
  const state = tvState(status);
  const { hash } = useLocation();
  const pairingFirst = state.kind === "not-paired";

  useEffect(() => {
    if (hash) document.getElementById(hash.slice(1))?.scrollIntoView?.({ behavior: "smooth", block: "start" });
  }, [hash]);

  return (
    <Page>
      <PageHeader eyebrow="TV" title="The Frame" description="Art Mode, picture settings and how vigo reaches the TV." />
      {status && <WarningsBanner warnings={status.warnings} className="mb-8" />}
      <div className="grid gap-10">
        {/* Ordered with CSS rather than moved in the tree, so the pairing card isn't remounted mid-flow. */}
        <Section id="pairing" title="Pairing" description="A one-time handshake that lets vigo control the TV." className={pairingFirst ? "order-first" : "order-3"}>
          <PairingCard />
        </Section>
        <Section title="Art Mode" className="order-1">
          <ArtModeCard status={status} state={state} />
        </Section>
        <Section title="Art settings" description="Read from the TV. Changes are checked by reading them back." className="order-2">
          <SettingsCard state={state} />
        </Section>
        <Section title="Connection" className="order-4">
          <ConnectionCard />
        </Section>
        <Section title="Device" className="order-5">
          <DeviceCard status={status} />
        </Section>
        <Section id="security" title="Security" description="The password that protects this web app." className="order-6">
          <ChangePasswordCard />
        </Section>
      </div>
    </Page>
  );
}
