import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { formatSetupCode } from "@/features/apple-home/setup-code";
import { renderApp } from "@/test/render";

function group(id: string) {
  const el = document.querySelector<HTMLElement>(`[data-group="${id}"]`);
  if (!el) throw new Error(`no group ${id}`);
  return el;
}

describe("Apple Home", () => {
  it("shows an offline state when vigo-home is down", async () => {
    renderApp("/integrations/apple-home", { homeMode: "offline" });
    expect(await screen.findByRole("heading", { name: "HomeKit service offline" })).toBeInTheDocument();
  });

  it("shows unimplemented providers as coming soon", async () => {
    renderApp("/integrations/apple-home", { homeMode: "matter" });
    expect(await screen.findByText("Coming soon")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Matter support/ })).toBeInTheDocument();
  });

  it("shows a QR code and setup code for unpaired groups, and toggles accessories", async () => {
    const { user, backend } = renderApp("/integrations/apple-home");
    await screen.findByRole("heading", { name: "Television" });

    const tv = group("tv");
    expect(within(tv).getByRole("img", { name: "HomeKit setup code for Television" })).toBeInTheDocument();
    expect(within(tv).getByText("318-42-907")).toBeInTheDocument();
    expect(within(tv).getByRole("switch", { name: "The Frame" })).toBeDisabled();

    const bridge = group("bridge");
    expect(within(bridge).queryByRole("img")).not.toBeInTheDocument();
    const artMode = within(bridge).getByRole("switch", { name: "Art Mode" });
    expect(artMode).not.toBeChecked();
    await user.click(artMode);
    await waitFor(() => expect(artMode).toBeChecked());
    expect(backend.home.accessories.find((a) => a.id === "art_mode")?.enabled).toBe(true);
  });

  it("requires typing a confirmation before resetting pairing", async () => {
    const { user, backend } = renderApp("/integrations/apple-home");
    await screen.findByRole("heading", { name: "Accessories" });

    await user.click(within(group("bridge")).getByRole("button", { name: "Reset pairing" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/add them in the Home app/)).toBeInTheDocument();
    const confirm = within(dialog).getByRole("button", { name: "Reset pairing" });
    expect(confirm).toBeDisabled();

    await user.type(within(dialog).getByRole("textbox"), "reset");
    expect(confirm).toBeEnabled();
    await user.click(confirm);

    await waitFor(() => expect(backend.home.groups.find((g) => g.id === "bridge")?.paired).toBe(false));
    expect(await within(group("bridge")).findByRole("img", { name: /HomeKit setup code/ })).toBeInTheDocument();
  });

  it("formats setup codes", () => {
    expect(formatSetupCode("31842907")).toBe("318-42-907");
    expect(formatSetupCode("318-42-907")).toBe("318-42-907");
  });
});
