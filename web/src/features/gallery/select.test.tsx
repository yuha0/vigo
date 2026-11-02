import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ItemResultSchema } from "@/gen/vigo/core/v1/library_pb";
import { create } from "@bufbuild/protobuf";
import { renderApp } from "@/test/render";

async function gallery(...args: Parameters<typeof renderApp>) {
  localStorage.clear();
  const r = renderApp(...args);
  await screen.findByRole("list", { name: "Artworks" });
  return r;
}

const bar = () => screen.getByRole("toolbar", { name: "Selection" });
const box = (name: string) => screen.getByRole("checkbox", { name });
const idOf = (b: ReturnType<typeof renderApp>["backend"], name: string) =>
  b.library.artworks.find((a) => a.sources[0]?.path.endsWith(name) || a.tvContentId === name)!.id;

describe("Gallery select mode", () => {
  it("turns on and off, with Escape and Cancel", async () => {
    const { user } = await gallery("/gallery");
    await user.click(screen.getByRole("button", { name: "Select" }));
    expect(box("vik-beach.jpg")).toHaveAttribute("aria-checked", "false");
    expect(screen.queryByRole("button", { name: /^Details for/ })).not.toBeInTheDocument();

    await user.click(box("vik-beach.jpg"));
    expect(box("vik-beach.jpg")).toHaveAttribute("aria-checked", "true");
    expect(bar()).toHaveTextContent("1 selected");

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("toolbar", { name: "Selection" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "vik-beach.jpg" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Select" }));
    expect(box("vik-beach.jpg")).toHaveAttribute("aria-checked", "false");
    await user.click(within(bar()).getByRole("button", { name: "Cancel selecting" }));
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
  });

  it("enters select mode with a long press on touch", async () => {
    await gallery("/gallery");
    const tile = screen.getByRole("button", { name: "vik-beach.jpg" });
    fireEvent.pointerDown(tile, { pointerType: "touch", clientX: 5, clientY: 5 });
    await act(() => new Promise((r) => setTimeout(r, 600)));
    fireEvent.pointerUp(tile, { pointerType: "touch" });
    fireEvent.click(tile);
    expect(box("vik-beach.jpg")).toHaveAttribute("aria-checked", "true");
    expect(bar()).toHaveTextContent("1 selected");
  });

  it("selects all loaded results except greyed ones, and clears", async () => {
    const { user } = await gallery("/gallery");
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(within(bar()).getByRole("button", { name: "Select all" }));
    const boxes = screen.getAllByRole("checkbox");
    expect(boxes.filter((b) => b.getAttribute("aria-checked") === "true")).toHaveLength(58);
    expect(bar()).toHaveTextContent("58 selected");
    expect(screen.getByRole("checkbox", { name: "wedding-1988.jpg" })).toBeDisabled();

    await user.click(within(bar()).getByRole("button", { name: "Clear" }));
    expect(bar()).toHaveTextContent("0 selected");
  });

  it("selects a range with shift-click", async () => {
    const { user } = await gallery("/gallery");
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(box("vik-beach.jpg"));
    await user.keyboard("{Shift>}");
    await user.click(box("alfama.jpg"));
    await user.keyboard("{/Shift}");
    expect(bar()).toHaveTextContent("4 selected");
  });

  it("starts over when the tabs change", async () => {
    const { user } = await gallery("/gallery");
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(box("vik-beach.jpg"));
    await user.click(within(screen.getByRole("group", { name: "Show" })).getByRole("button", { name: /^Art Store/ }));
    await waitFor(() => expect(bar()).toHaveTextContent("0 selected"));
  });

  it("deletes only eligible items and says how many were skipped", async () => {
    const { user, backend } = await gallery("/gallery");
    const spy = vi.spyOn(backend.library, "deleteArtworks");
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(box("vik-beach.jpg"));
    await user.click(box("SAM-F0101"));
    expect(bar()).toHaveTextContent("2 selected");
    expect(bar()).toHaveTextContent("1 can't be deleted");
    expect(within(bar()).getByRole("button", { name: /^Delete/ })).toHaveTextContent("1");

    await user.click(within(bar()).getByRole("button", { name: /^Delete/ }));
    const confirm = await screen.findByRole("alertdialog", { name: "Delete 1 artwork?" });
    expect(confirm).toHaveTextContent("deleted from their sources and removed from the TV");
    expect(confirm).toHaveTextContent("1 selected item is skipped");
    await user.click(within(confirm).getByRole("button", { name: "Delete 1" }));
    await waitFor(() => expect(spy).toHaveBeenCalledWith([idOf(backend, "vik-beach.jpg")]));
    expect(await screen.findByText("Deleted 1 artwork")).toBeInTheDocument();
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
  });

  it("lists every copy of multi-copy items before deleting", async () => {
    const { user } = await gallery("/gallery");
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(box("water-lilies.jpg"));
    await user.click(within(bar()).getByRole("button", { name: /^Delete/ }));
    const files = within(await screen.findByRole("alertdialog")).getByRole("list", { name: "Files to delete" });
    expect(within(files).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "/srv/photos/paintings/water-lilies.jpg",
      "/mnt/nas/family/art/water-lilies.jpg",
    ]);
  });

  it("summarises a partial failure", async () => {
    const { user, backend } = await gallery("/gallery");
    vi.spyOn(backend.library, "deleteArtworks").mockImplementation((ids) =>
      ids.map((id, i) =>
        create(ItemResultSchema, i === 1 ? { id, errorCode: "failed_precondition", errorMessage: "The TV is unreachable." } : { id }),
      ),
    );
    await user.click(screen.getByRole("button", { name: "Select" }));
    for (const n of ["vik-beach.jpg", "skogafoss.jpg", "aurora.jpg"]) await user.click(box(n));
    await user.click(within(bar()).getByRole("button", { name: /^Delete/ }));
    await user.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete 3" }));
    const toast = (await screen.findByText("Deleted 2, 1 failed")).closest("[data-sonner-toast]")!;
    expect(toast).toHaveTextContent("skogafoss.jpg: The TV is unreachable.");
  });

  it("refreshes TV-only thumbnails from the bar", async () => {
    const { user, backend } = await gallery("/gallery");
    const spy = vi.spyOn(backend.library, "refreshThumbnails");
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(box("vik-beach.jpg"));
    await user.click(box("SAM-F0101"));
    const refresh = within(bar()).getByRole("button", { name: /^Refresh thumbnail/ });
    expect(refresh).toHaveTextContent("1");
    await user.click(refresh);
    await waitFor(() => expect(spy).toHaveBeenCalledWith(["tv:SAM-F0101"]));
    expect(await screen.findByText("Refreshed 1 thumbnail")).toBeInTheDocument();
    expect(spy).toHaveBeenCalledOnce();
    expect(screen.getAllByText("Refreshed 1 thumbnail")).toHaveLength(1);
  });

  it("refreshes one thumbnail from the details and reloads the image", async () => {
    const { user, backend } = await gallery("/gallery");
    const tile = screen.getByRole("button", { name: "SAM-F0101" }).closest("li")!;
    const before = tile.querySelector("img")!.getAttribute("src");

    await user.click(screen.getByRole("button", { name: "Details for SAM-F0101" }));
    const sheet = await screen.findByRole("dialog", { name: "SAM-F0101" });
    const button = within(sheet).getByRole("button", { name: "Refresh thumbnail" });
    expect(button).toHaveAttribute("title", expect.stringMatching(/doesn't download it again unless you ask/));
    await user.click(button);
    expect(await screen.findByText("Refreshed 1 thumbnail")).toBeInTheDocument();
    await waitFor(() => expect(tile.querySelector("img")!.getAttribute("src")).not.toBe(before));
    await new Promise((r) => setTimeout(r, 100));
    expect(backend.calls.filter((c) => c === "LibraryService/RefreshThumbnails")).toHaveLength(1);
    expect(screen.getAllByText("Refreshed 1 thumbnail")).toHaveLength(1);
    expect(tile.querySelector("img")!.getAttribute("src")).toMatch(/#v=\d+$/);

    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Details for vik-beach.jpg" }));
    expect(within(await screen.findByRole("dialog", { name: "vik-beach.jpg" })).queryByRole("button", { name: "Refresh thumbnail" })).not.toBeInTheDocument();
  });
});
