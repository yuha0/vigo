import { screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { KeyAction } from "@/gen/vigo/core/v1/core_pb";
import { renderApp } from "@/test/render";

const iPhone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1";

describe("Remote", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("has no power key", async () => {
    renderApp("/remote");
    await screen.findByRole("group", { name: "Virtual remote" });

    expect(screen.queryByRole("button", { name: /power/i })).not.toBeInTheDocument();
    const keys = [...document.querySelectorAll("[data-key]")].map((el) => el.getAttribute("data-key"));
    expect(keys).toEqual(
      expect.arrayContaining(["KEY_UP", "KEY_DOWN", "KEY_LEFT", "KEY_RIGHT", "KEY_ENTER", "KEY_RETURN", "KEY_HOME", "KEY_VOLUP", "KEY_VOLDOWN", "KEY_MUTE", "KEY_CHUP", "KEY_CHDOWN"]),
    );
    expect(keys).not.toContain("KEY_POWER");
  });

  it("sends a click on tap and press/release on hold", async () => {
    const { user, backend } = renderApp("/remote");
    const up = await screen.findByRole("button", { name: "Up" });

    await user.click(up);
    await waitFor(() => expect(backend.mock.sentKeys).toContainEqual({ key: "KEY_UP", action: KeyAction.CLICK }));

    const vol = screen.getByRole("button", { name: "Volume up" });
    await user.pointer({ keys: "[MouseLeft>]", target: vol });
    await new Promise((r) => setTimeout(r, 450));
    await user.pointer({ keys: "[/MouseLeft]", target: vol });
    await waitFor(() =>
      expect(backend.mock.sentKeys.filter((k) => k.key === "KEY_VOLUP").map((k) => k.action)).toEqual([KeyAction.PRESS, KeyAction.RELEASE]),
    );
  });

  it("offers the iPhone Remote only on iOS", async () => {
    vi.stubGlobal("navigator", { ...navigator, userAgent: iPhone, maxTouchPoints: 5 });
    renderApp("/remote");
    const link = await screen.findByRole("link", { name: /Open iPhone Remote/ });
    expect(link).toHaveAttribute("href", "tvremote://");
    expect(screen.getByText(/added to Apple Home first/)).toBeInTheDocument();
  });

  it("hides the iPhone Remote elsewhere", async () => {
    renderApp("/remote");
    await screen.findByRole("group", { name: "Virtual remote" });
    expect(screen.queryByRole("link", { name: /Open iPhone Remote/ })).not.toBeInTheDocument();
  });
});
