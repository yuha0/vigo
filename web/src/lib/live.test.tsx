import { act, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { renderApp } from "@/test/render";

const debounceGap = () => new Promise((r) => setTimeout(r, 600));

function becomeVisible() {
  act(() => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

function bfcacheRestore() {
  act(() => {
    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
  });
}

async function cardState(kind: string) {
  await waitFor(() => expect(screen.getByTestId("tv-card")).toHaveAttribute("data-state", kind));
}

describe("Refresh", () => {
  it("is called once on load", async () => {
    const { backend } = renderApp("/", { scenario: "art" });
    await cardState("showing-art");
    await waitFor(() => expect(backend.tv.refreshCount).toBe(1));
    await debounceGap();
    expect(backend.tv.refreshCount).toBe(1);
  });

  it("is called once when the page becomes visible, even if pageshow fires too", async () => {
    const { backend } = renderApp("/", { scenario: "art" });
    await waitFor(() => expect(backend.tv.refreshCount).toBe(1));
    await debounceGap();

    becomeVisible();
    bfcacheRestore();
    await waitFor(() => expect(backend.tv.refreshCount).toBe(2));
    await debounceGap();
    expect(backend.tv.refreshCount).toBe(2);
  });

  it("applies the returned status", async () => {
    const { backend } = renderApp("/", { scenario: "art" });
    await cardState("showing-art");
    await waitFor(() => expect(backend.tv.refreshCount).toBe(1));
    await debounceGap();

    // The TV changed without an event; only Refresh (not the stream) can notice.
    backend.tv.applyScenario("powered-off", false);
    bfcacheRestore();
    await cardState("powered-off");
    expect(backend.tv.refreshCount).toBe(2);
  });

  it("ignores pageshow that isn't a cache restore", async () => {
    const { backend } = renderApp("/", { scenario: "art" });
    await waitFor(() => expect(backend.tv.refreshCount).toBe(1));
    await debounceGap();

    act(() => {
      window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: false }));
    });
    await debounceGap();
    expect(backend.tv.refreshCount).toBe(1);
  });
});
