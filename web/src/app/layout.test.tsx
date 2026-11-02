import { act, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { renderApp } from "@/test/render";

function rail() {
  return document.querySelector('nav[data-layout="rail"]');
}

async function tvCard(kind: string) {
  await waitFor(() => expect(screen.getByTestId("tv-card")).toHaveAttribute("data-state", kind));
  return screen.getByTestId("tv-card");
}

describe("Home", () => {
  it("uses one layout in Art Mode, with a contained preview labelled Selected", async () => {
    renderApp("/", { scenario: "art" });
    const card = await tvCard("showing-art");

    expect(within(card).getByRole("heading", { name: "Art Mode" })).toBeInTheDocument();
    const preview = within(card).getByTestId("tv-preview");
    expect(preview).toHaveAttribute("data-state", "showing-art");
    expect(within(preview).getByRole("img", { name: "Selected artwork SAM-F0206" })).toBeInTheDocument();
    expect(within(preview).getAllByText(/Selected/).length).toBeGreaterThan(0);
    expect(rail()).toBeInTheDocument();
    expect(document.querySelectorAll("nav")).toHaveLength(1);
    expect(screen.queryByText(/displaying/i)).not.toBeInTheDocument();
  });

  it.each([
    ["tv-mode", "TV mode", "Something other than art is on screen", true],
    ["motion-sleep", "Asleep", "Asleep", true],
    ["powered-off", "Powered off", "Powered off", true],
    ["unreachable", "Can't reach the TV", "No answer from the TV", false],
    ["not-paired", "Not paired", "Not paired", false],
    ["no-art-host", "Art Mode hasn't started yet", "Art Mode not started", false],
    ["standby-replica", "Standby replica", "No TV connection on this replica", false],
    ["connecting", "Connecting", "Connecting", false],
  ] as const)("shows %s in the same card with a placeholder preview", async (scenario, title, caption, canShowArt) => {
    renderApp("/", { scenario });
    const card = await tvCard(scenario);

    expect(within(card).getByRole("heading", { name: title })).toBeInTheDocument();
    const preview = within(card).getByTestId("tv-preview");
    expect(preview).toHaveAttribute("data-state", scenario);
    expect(within(preview).queryByRole("img")).not.toBeInTheDocument();
    expect(within(preview).getAllByText(caption).length).toBeGreaterThan(0);
    expect(rail()).toBeInTheDocument();
    if (canShowArt) {
      expect(within(card).getByRole("button", { name: /Show art|Switch to art/ })).toBeInTheDocument();
    } else {
      expect(within(card).queryByRole("button", { name: /Show art|Switch to art/ })).not.toBeInTheDocument();
    }
  });

  it("shows the artwork in the preview when Show art succeeds", async () => {
    const { user } = renderApp("/", { scenario: "powered-off" });
    const card = await tvCard("powered-off");

    await user.click(within(card).getByRole("button", { name: "Show art" }));
    await tvCard("showing-art");
    expect(within(card).getByRole("img", { name: "Selected artwork SAM-F0206" })).toBeInTheDocument();
  });

  it("follows status changes from WatchEvents", async () => {
    const { backend } = renderApp("/", { scenario: "art" });
    await tvCard("showing-art");

    act(() => backend.mock.setScenario("unreachable"));
    const card = await tvCard("unreachable");
    expect(within(card).getByRole("heading", { name: "Can't reach the TV" })).toBeInTheDocument();
  });

  it("reconnects when the stream ends and treats the first message as authoritative", async () => {
    const { backend } = renderApp("/", { scenario: "tv-mode" });
    await tvCard("tv-mode");

    act(() => backend.mock.dropStreams());
    // Change state while nobody is listening; the reconnect snapshot must carry it.
    backend.tv.applyScenario("art", false);
    await waitFor(() => expect(screen.getByTestId("tv-card")).toHaveAttribute("data-state", "showing-art"), { timeout: 3000 });
  });

  it("links to pairing when not paired", async () => {
    renderApp("/", { scenario: "not-paired" });
    const link = await screen.findByRole("link", { name: /Pair with the TV/ });
    expect(link).toHaveAttribute("href", "/tv#pairing");
  });

  it("shows warnings prominently", async () => {
    renderApp("/", { scenario: "model-changed" });
    const alert = await screen.findByRole("alert", { name: "Warnings" });
    expect(within(alert).getByText("The TV's model changed")).toBeInTheDocument();
  });
});

describe("Integrations navigation", () => {
  it("groups Apple Home under Integrations in the rail", async () => {
    const { user } = renderApp("/tv", { scenario: "tv-mode" });
    const rail = await screen.findByRole("navigation", { name: "Main" });
    expect(within(rail).queryByRole("link", { name: "Apple Home" })).not.toBeInTheDocument();

    const group = within(rail).getByRole("button", { name: "Integrations" });
    expect(group).toHaveAttribute("aria-expanded", "false");
    await user.click(group);
    expect(group).toHaveAttribute("aria-expanded", "true");

    await user.click(within(rail).getByRole("link", { name: "Apple Home" }));
    expect(await screen.findByRole("heading", { name: "Apple Home", level: 1 })).toBeInTheDocument();
  });

  it("opens the group in the rail when an integration is showing", async () => {
    renderApp("/integrations/apple-home", { scenario: "tv-mode" });
    const rail = await screen.findByRole("navigation", { name: "Main" });
    expect(within(rail).getByRole("button", { name: "Integrations" })).toHaveAttribute("aria-expanded", "true");
    expect(within(rail).getByRole("link", { name: "Apple Home" })).toHaveAttribute("aria-current", "page");
  });

  it.each(["/integrations", "/apple-home"])("redirects %s to the first integration", async (path) => {
    renderApp(path, { scenario: "tv-mode" });
    expect(await screen.findByRole("heading", { name: "Apple Home", level: 1 })).toBeInTheDocument();
  });
});
