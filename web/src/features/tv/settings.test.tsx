import { create } from "@bufbuild/protobuf";
import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { SettingSchema } from "@/gen/vigo/core/v1/core_pb";
import * as fixtures from "@/lib/mock/fixtures";
import { renderApp } from "@/test/render";

const withUnknown = () => [
  ...fixtures.settings(),
  create(SettingSchema, { key: "frame_glow", kind: { case: "toggle", value: { value: false } } }),
  create(SettingSchema, { key: "night_mode", kind: { case: "enum", value: { value: "auto", options: ["off", "on", "auto"] } } }),
  create(SettingSchema, { key: "photo_filter_strength", kind: { case: "range", value: { value: 3, min: 0, max: 5 } } }),
];

function row(key: string) {
  const el = document.querySelector<HTMLElement>(`[data-setting="${key}"]`);
  if (!el) throw new Error(`no setting row ${key}`);
  return el;
}

describe("TV settings", () => {
  it("renders each kind with the right control and friendly labels", async () => {
    renderApp("/tv", { scenario: "art", settings: withUnknown() });
    await screen.findByText("Brightness");

    const brightness = within(row("brightness")).getByRole("slider");
    expect(brightness).toHaveAttribute("aria-valuenow", "4");
    expect(within(row("color_temperature")).getByText("Color temperature")).toBeInTheDocument();
    expect(within(row("motion_sensitivity")).getByText("Medium")).toBeInTheDocument();

    const timer = row("motion_timer");
    expect(within(timer).getByText("Motion timer")).toBeInTheDocument();
    expect(within(timer).getByText(/Off keeps art displayed/)).toBeInTheDocument();
    expect(within(timer).getByRole("combobox")).toHaveTextContent("5 min");

    expect(within(row("brightness_sensor_setting")).getByRole("switch", { name: "Auto brightness" })).toBeChecked();

    expect(within(row("frame_glow")).getByRole("switch", { name: "Frame glow" })).not.toBeChecked();
    const night = row("night_mode");
    expect(within(night).getByText("Night mode")).toBeInTheDocument();
    expect(within(night).getByRole("radio", { name: "auto" })).toHaveAttribute("aria-checked", "true");
    expect(within(row("photo_filter_strength")).getByRole("slider")).toHaveAttribute("aria-valuenow", "3");
  });

  it("applies a confirmed change", async () => {
    const { user, backend } = renderApp("/tv", { scenario: "art" });
    const sw = await screen.findByRole("switch", { name: "Auto brightness" });

    await user.click(sw);
    await waitFor(() => expect(sw).not.toBeChecked());
    const stored = backend.tv.settings.find((s) => s.key === "brightness_sensor_setting");
    expect(stored?.kind.value).toMatchObject({ value: false });
    expect(screen.queryByText(/didn't apply/)).not.toBeInTheDocument();
  });

  it("says so when the TV acknowledges but doesn't apply a change", async () => {
    const { user } = renderApp("/tv", { scenario: "art", stubbornKeys: ["color_temperature"] });
    await screen.findByText("Color temperature");
    const slider = within(row("color_temperature")).getByRole("slider");

    slider.focus();
    await user.keyboard("{ArrowRight}");

    const note = await within(row("color_temperature")).findByText(/acknowledged Cool 1 but didn't apply it/);
    expect(note).toHaveTextContent("It still reads Neutral");
    await waitFor(() => expect(within(row("color_temperature")).getByRole("slider")).toHaveAttribute("aria-valuenow", "0"));
  });

  it("runs the pairing flow from WatchEvents progress", async () => {
    const { user } = renderApp("/tv", { scenario: "not-paired", pairingDelayMs: 300 });

    await user.click(await screen.findByRole("button", { name: "Start pairing" }));
    expect(await screen.findByText("Press Allow on the TV")).toBeInTheDocument();
    expect(screen.getByLabelText(/seconds left/)).toBeInTheDocument();
    expect(await screen.findByText("Paired", {}, { timeout: 3000 })).toBeInTheDocument();
  });
});
