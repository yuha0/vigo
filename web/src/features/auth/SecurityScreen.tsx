import { Page, PageHeader } from "@/components/Page";
import { ChangePasswordCard } from "@/features/auth/ChangePasswordCard";

export function SecurityScreen() {
  return (
    <Page className="max-w-2xl">
      <PageHeader title="Security" description="The password that protects this web app. There's no username, just this one password." />
      <ChangePasswordCard />
    </Page>
  );
}
