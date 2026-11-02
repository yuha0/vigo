import { Code, ConnectError } from "@connectrpc/connect";
import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { createQueryClient } from "@/app/query-client";
import { RemoteAction, RemoteButton } from "@/gen/vigo/home/v1/home_pb";
import { qk } from "@/lib/queries";
import { renderApp } from "@/test/render";

const PATH = "/integrations/apple-home";

async function section() {
  const heading = await screen.findByRole("heading", { name: "Remote app button mapping" });
  return heading.closest("section") as HTMLElement;
}

const picker = (name: string) => screen.getByRole("combobox", { name: `${name} does` });

async function choose(user: ReturnType<typeof renderApp>["user"], button: string, option: string | RegExp) {
  await user.click(picker(button));
  await user.click(await screen.findByRole("option", { name: option }));
}

describe("Remote app button mapping", () => {
  it("lists the iPhone remote's buttons, power first as one group", async () => {
    renderApp(PATH);
    const card = await section();
    expect(card).toHaveTextContent("buttons can't be added or removed");
    expect(within(card).queryByText(/Other HomeKit keys/)).not.toBeInTheDocument();

    const list = within(card).getByRole("list", { name: "Remote buttons" });
    const rows = [...list.querySelectorAll(":scope > li")].map((li) => li.getAttribute("data-button"));
    expect(rows).toEqual([
      "POWER",
      "ARROW_UP",
      "ARROW_DOWN",
      "ARROW_LEFT",
      "ARROW_RIGHT",
      "SELECT",
      "BACK",
      "PLAY_PAUSE",
      "INFORMATION",
      "VOLUME_UP",
      "VOLUME_DOWN",
      "MUTE",
    ]);

    const power = within(list).getByRole("list", { name: "Power button" });
    expect(within(power).getAllByRole("listitem")).toHaveLength(2);
    expect(picker("Power, when the TV shows as off")).toHaveTextContent("Show art");
    expect(picker("Power, when the TV shows as on")).toHaveTextContent("Nothing");
    expect(card).toHaveTextContent("There is no verified way to power this TV off remotely; Toggle Art Mode switches to TV mode.");

    expect(within(list).getByText("The phone's volume-up button while the remote is open")).toBeInTheDocument();
    expect(picker("Select (centre)")).toHaveTextContent(/^KEY_ENTER$/);
    expect(picker("Up")).toHaveTextContent(/^KEY_UP$/);
    expect(picker("Info (i)")).toHaveTextContent("Nothing");
    expect(picker("Play/Pause")).toHaveTextContent("Nothing");
    expect(picker("Mute")).toHaveTextContent("KEY_MUTE");
  });

  it("binds the power button", async () => {
    const { user, backend } = renderApp(PATH);
    await section();
    await choose(user, "Power, when the TV shows as on", "Toggle Art Mode");
    await waitFor(() =>
      expect(backend.home.bindings.find((b) => b.button === RemoteButton.POWER_OFF)?.target?.target).toEqual({
        case: "action",
        value: RemoteAction.TOGGLE_ART_MODE,
      }),
    );
  });

  it("offers our actions and the TV keys exactly as the API names them, never KEY_POWER", async () => {
    const { user, backend } = renderApp(PATH);
    await section();
    await user.click(picker("Info (i)"));
    const listbox = await screen.findByRole("listbox");
    expect(within(listbox).getByRole("option", { name: "Toggle Art Mode" })).toBeInTheDocument();
    expect(within(listbox).getByRole("option", { name: "Show art" })).toBeInTheDocument();
    const keys = within(listbox).getAllByRole("option").map((o) => o.textContent).filter((t) => t?.startsWith("KEY_"));
    expect(keys).toEqual(backend.home.remoteMapping().keys.map((k) => k.key));
    expect(listbox).not.toHaveTextContent(/verified/i);
    expect(within(listbox).queryByRole("option", { name: /KEY_POWER/ })).not.toBeInTheDocument();
  });

  it("saves one binding at a time, and it survives a reload", async () => {
    const qc = createQueryClient();
    const { user, backend } = renderApp(PATH, {}, undefined, qc);
    await section();
    const spy = vi.spyOn(backend.home, "updateRemoteMapping");

    await choose(user, "Info (i)", "Toggle Art Mode");
    await waitFor(() => expect(spy).toHaveBeenCalledOnce());
    expect(spy.mock.calls[0][0]).toHaveLength(1);
    expect(spy.mock.calls[0][0][0]).toMatchObject({
      button: RemoteButton.INFORMATION,
      target: { target: { case: "action", value: RemoteAction.TOGGLE_ART_MODE } },
    });

    await choose(user, "Play/Pause", "KEY_MENU");
    await waitFor(() => expect(spy).toHaveBeenCalledTimes(2));
    expect(spy.mock.calls[1][0][0].target?.target).toEqual({ case: "key", value: "KEY_MENU" });

    await qc.refetchQueries({ queryKey: qk.homeRemote });
    expect(picker("Info (i)")).toHaveTextContent("Toggle Art Mode");
    expect(picker("Play/Pause")).toHaveTextContent("KEY_MENU");
  });

  it("rolls back and explains when saving fails", async () => {
    const { user, backend } = renderApp(PATH);
    await section();
    vi.spyOn(backend.home, "updateRemoteMapping").mockImplementation(() => {
      throw new ConnectError("KEY_POWER can't be bound", Code.InvalidArgument);
    });
    await choose(user, "Info (i)", "Show art");
    expect(await screen.findByText("Couldn't change the button")).toBeInTheDocument();
    await waitFor(() => expect(picker("Info (i)")).toHaveTextContent("Nothing"));
  });

  it("resets to defaults only after confirmation", async () => {
    const { user, backend } = renderApp(PATH);
    const card = await section();
    await choose(user, "Up", "Show art");
    await waitFor(() => expect(picker("Up")).toHaveTextContent("Show art"));

    await user.click(within(card).getByRole("button", { name: "Reset to defaults" }));
    let confirm = await screen.findByRole("alertdialog", { name: "Reset the remote buttons?" });
    await user.click(within(confirm).getByRole("button", { name: "Keep my changes" }));
    expect(picker("Up")).toHaveTextContent("Show art");

    await user.click(within(card).getByRole("button", { name: "Reset to defaults" }));
    confirm = await screen.findByRole("alertdialog", { name: "Reset the remote buttons?" });
    await user.click(within(confirm).getByRole("button", { name: "Reset" }));
    await waitFor(() => expect(picker("Up")).toHaveTextContent("KEY_UP"));
    expect(backend.home.bindings.find((b) => b.button === RemoteButton.ARROW_UP)?.target?.target).toEqual({ case: "key", value: "KEY_UP" });
  });

  it("shows the offline state when vigo-home is down", async () => {
    renderApp(PATH, { homeMode: "offline" });
    expect(await screen.findByRole("heading", { name: "HomeKit service offline" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Remote app button mapping" })).not.toBeInTheDocument();
  });

  it("validates keys in the mock like the server", async () => {
    const { backend } = renderApp(PATH);
    await section();
    const bind = (value: string) => () =>
      backend.home.updateRemoteMapping([
        { $typeName: "vigo.home.v1.RemoteBinding", button: RemoteButton.ARROW_UP, target: { $typeName: "vigo.home.v1.RemoteTarget", target: { case: "key", value } } },
      ]);
    expect(bind("KEY_POWER")).toThrow(/KEY_POWER/);
    expect(bind("KEY_PLAY")).toThrow(/isn't a key/);
  });
});
