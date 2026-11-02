import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { SyncState } from "@/gen/vigo/core/v1/library_pb";
import { renderApp } from "@/test/render";

function tile(name: string) {
  const btn = screen.getByRole("button", { name: new RegExp(`^${name.replace(/\./g, "\\.")}`) });
  return btn.closest("li") as HTMLElement;
}

async function gallery(...args: Parameters<typeof renderApp>) {
  const r = renderApp(...args);
  await screen.findByRole("list", { name: "Artworks" });
  return r;
}

async function openDetails(user: ReturnType<typeof renderApp>["user"], name: string) {
  await user.click(screen.getByRole("button", { name: `Details for ${name}` }));
  return screen.findByRole("dialog", { name: name });
}

const lastRequest = (b: ReturnType<typeof renderApp>["backend"]) => b.library.requests.at(-1)!;

describe("Gallery grid", () => {
  it("lists the library with pagination", async () => {
    const { user, backend } = await gallery("/gallery");
    expect(screen.getByRole("heading", { name: "Gallery", level: 1 })).toBeInTheDocument();
    const list = screen.getByRole("list", { name: "Artworks" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(60);
    expect(lastRequest(backend)).toMatchObject({ includeDeleting: true, pageSize: 60, pageToken: "" });

    await user.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(within(list).getAllByRole("listitem")).toHaveLength(120));
    expect(lastRequest(backend).pageToken).toBe("60");
  });

  it("greys out deleting artworks, keeps them visible, and ignores taps", async () => {
    const { backend } = await gallery("/gallery", { transitionMs: 300 }, (b) => void b.tv.showArt("MY_F0002"));

    for (const [name, badge] of [
      ["wedding-1988.jpg", "Waiting to delete"],
      ["harbour.jpg", "Deleting"],
    ]) {
      const li = tile(name);
      expect(li).toHaveAttribute("aria-disabled", "true");
      expect(li).toHaveClass("pointer-events-none", "grayscale");
      expect(within(li).getByText(badge)).toBeInTheDocument();
      const select = within(li).getByRole("button", { name: `${name}, being deleted` });
      expect(select).toBeDisabled();
      expect(within(li).getByRole("button", { name: `Details for ${name}` })).toBeDisabled();
      fireEvent.click(select);
    }
    expect(screen.queryByText("Selecting…")).not.toBeInTheDocument();
    expect(backend.tv.status.selectedArt?.contentId).toBe("MY_F0002");
  });

  it("shows sync and issue badges", async () => {
    await gallery("/gallery");
    expect(within(tile("arashiyama.jpg")).getByText("Waiting to upload")).toBeInTheDocument();
    expect(within(tile("kinkaku-ji.jpg")).getByText("Uploading")).toBeInTheDocument();
    const error = within(tile("grandmother-1962.png")).getByText("Sync error").closest("[title]");
    expect(error).toHaveAttribute("title", "the TV didn't answer the upload in time");

    const fushimi = tile("fushimi-inari.jpg");
    expect(within(fushimi).getByRole("img", { name: "Low resolution: the TV will upscale" })).toBeInTheDocument();
    expect(within(fushimi).getByRole("img", { name: "Not 16:9" })).toBeInTheDocument();
    expect(within(tile("tram-28.jpg")).queryByRole("img")).not.toBeInTheDocument();
  });
});

describe("Provenance", () => {
  it("marks art vigo doesn't manage and frames art it does", async () => {
    const { user } = await gallery("/gallery");

    const store = tile("SAM-F0101");
    expect(store).toHaveAttribute("data-provenance", "art-store");
    expect(within(store).getByText("Art Store")).toBeInTheDocument();
    expect(within(store).getByText(/^Not managed/)).toHaveTextContent("Not managed by vigo");

    for (const name of ["vik-beach.jpg", "arashiyama.jpg", "first-day.jpg"]) {
      const managed = tile(name);
      expect(managed).toHaveAttribute("data-provenance", "managed");
      expect(managed.querySelector("span[data-provenance]")).toBeNull();
      expect(within(managed).queryByText(/^Not managed/)).not.toBeInTheDocument();
    }

    await user.type(screen.getByRole("searchbox", { name: "Search" }), "MY_F0001");
    await waitFor(() => expect(tile("MY_F0001")).toHaveAttribute("data-provenance", "elsewhere"));
    expect(within(tile("MY_F0001")).getByText("Elsewhere")).toBeInTheDocument();
  });

  it("splits the unfiltered grid into managed and TV-only sections", async () => {
    const { user } = await gallery("/gallery");
    const list = screen.getByRole("list", { name: "Artworks" });
    const headings = within(list).getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
    expect(headings).toEqual(["From your folders", "On the TV only"]);

    const order = [...list.querySelectorAll("li[data-provenance], li[data-group]")].map((li) =>
      li.getAttribute("data-group") ?? (li.getAttribute("data-provenance") === "managed" ? "m" : "u"),
    );
    const split = order.indexOf("unmanaged");
    expect(order.slice(1, split).every((x) => x === "m")).toBe(true);
    expect(order.slice(split + 1).every((x) => x === "u")).toBe(true);

    await user.type(screen.getByRole("searchbox", { name: "Search" }), "a");
    await waitFor(() => expect(within(screen.getByRole("list", { name: "Artworks" })).queryAllByRole("heading")).toHaveLength(0));
  });

  it("states provenance plainly in the details", async () => {
    const { user } = await gallery("/gallery");
    const managed = await openDetails(user, "vik-beach.jpg");
    expect(managed).toHaveTextContent("Managed by vigo, from /srv/photos");
    await user.keyboard("{Escape}");

    const store = await openDetails(user, "SAM-F0101");
    expect(store).toHaveTextContent("Samsung Art Store, not managed by vigo");
    expect(store).toHaveTextContent("can't be deleted or edited here");
    await user.keyboard("{Escape}");

    await user.type(screen.getByRole("searchbox", { name: "Search" }), "MY_F0001");
    await waitFor(() => expect(screen.getAllByRole("button", { name: /^Details for/ })).toHaveLength(1));
    expect(await openDetails(user, "MY_F0001")).toHaveTextContent("Uploaded to the TV elsewhere, not managed by vigo");
  });
});

describe("Gallery filters", () => {
  it("maps tag chips and search to the request", async () => {
    const { user, backend } = await gallery("/gallery");

    await user.click(screen.getByRole("button", { name: "Filters" }));
    const sheet = await screen.findByRole("dialog", { name: "Filter" });
    const source = within(sheet).getByRole("region", { name: "Source" });
    expect(within(source).getByRole("button", { name: /^Art Store/ })).toBeInTheDocument();
    expect(within(source).getByRole("button", { name: /^Uploaded elsewhere/ })).toBeInTheDocument();
    expect(within(source).getByRole("button", { name: /^\/srv\/photos/ })).toBeInTheDocument();
    const tvCategory = within(sheet).getByTitle("tv-category/MY-C0008");
    expect(tvCategory.closest("details")).not.toHaveAttribute("open");

    await user.click(within(sheet).getByTitle("issue/aspect-ratio"));
    await user.click(within(sheet).getByTitle("folder/2024/iceland"));
    await user.click(within(sheet).getByRole("button", { name: "Show results" }));

    await waitFor(() => expect(lastRequest(backend).tags).toEqual(["folder/2024/iceland", "issue/aspect-ratio"]));
    const list = await screen.findByRole("list", { name: "Artworks" });
    await waitFor(() => expect(within(list).getAllByRole("listitem")).toHaveLength(1));
    expect(within(list).getByRole("button", { name: /^skogafoss\.jpg/ })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Remove filter Not 16:9" }));
    await waitFor(() => expect(lastRequest(backend).tags).toEqual(["folder/2024/iceland"]));

    await user.type(screen.getByRole("searchbox", { name: "Search" }), "aurora");
    await waitFor(() => expect(lastRequest(backend)).toMatchObject({ query: "aurora", tags: ["folder/2024/iceland"] }));
  });
});

describe("Gallery selection", () => {
  it("moves Selected only once the TV confirms, one at a time", async () => {
    const { user, backend } = await gallery("/gallery", { transitionMs: 300 }, (b) => void b.tv.showArt("MY_F0002"));
    expect(screen.getByRole("button", { name: "vik-beach.jpg, selected" })).toHaveAttribute("aria-pressed", "true");

    await user.click(screen.getByRole("button", { name: "skogafoss.jpg" }));
    const pending = await screen.findByRole("button", { name: "skogafoss.jpg, selecting" });
    expect(pending).toHaveAttribute("aria-pressed", "false");
    expect(within(pending).getByText("Selecting…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "vik-beach.jpg, selected" })).toHaveAttribute("aria-pressed", "true");

    await user.click(screen.getByRole("button", { name: "aurora.jpg" }));
    expect(screen.getByRole("button", { name: "aurora.jpg" })).toHaveAttribute("aria-pressed", "false");

    expect(await screen.findByRole("button", { name: "skogafoss.jpg, selected" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "vik-beach.jpg" })).toHaveAttribute("aria-pressed", "false");
    expect(backend.tv.status.selectedArt?.contentId).toBe("MY_F0003");
  });

  it("can't select artwork that isn't on the TV yet", async () => {
    const { user, backend } = await gallery("/gallery", {}, (b) => void b.tv.showArt("MY_F0002"));
    const btn = screen.getByRole("button", { name: "arashiyama.jpg, not on the TV yet" });
    expect(btn).toHaveAttribute("aria-disabled", "true");
    await user.click(btn);
    expect(screen.queryByText("Selecting…")).not.toBeInTheDocument();
    expect(backend.tv.status.selectedArt?.contentId).toBe("MY_F0002");
  });

  it("keeps Selected on failure and offers Show art for the tapped artwork", async () => {
    const { user, backend } = await gallery("/gallery", { scenario: "powered-off" }, (b) => void b.tv.showArt("MY_F0002"));
    await waitFor(() => expect(screen.getByRole("button", { name: "vik-beach.jpg, selected" })).toBeInTheDocument());
    backend.tv.applyScenario("powered-off");

    await user.click(screen.getByRole("button", { name: "tram-28.jpg" }));
    expect(await screen.findByText("The TV isn't awake")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "vik-beach.jpg, selected" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "tram-28.jpg" })).not.toHaveAttribute("aria-busy");

    await user.click(screen.getByRole("button", { name: "Show art" }));
    expect(await screen.findByRole("button", { name: "tram-28.jpg, selected" })).toBeInTheDocument();
    expect(backend.tv.status.showingArt).toBe(true);
  });
});

describe("Artwork details", () => {
  it("edits user tags and shows system tags read-only", async () => {
    const { user, backend } = await gallery("/gallery");
    const sheet = await openDetails(user, "vik-beach.jpg");
    const id = backend.library.artworks.find((a) => a.sources[0]?.path.endsWith("vik-beach.jpg"))!.id;

    const system = within(sheet).getByRole("list", { name: "System tags" });
    expect(within(system).getByText("On the TV")).toBeInTheDocument();
    expect(within(system).queryByRole("button")).not.toBeInTheDocument();

    await user.type(within(sheet).getByRole("textbox", { name: "New tag" }), "sunset{Enter}");
    await waitFor(() => expect(backend.library.getArtwork(id).tags.map((t) => t.name)).toContain("sunset"));
    await user.click(await within(sheet).findByRole("button", { name: "Remove tag favorites" }));
    await waitFor(() => expect(backend.library.getArtwork(id).tags.map((t) => t.name)).not.toContain("favorites"));

    await user.type(within(sheet).getByRole("textbox", { name: "New tag" }), "a/b");
    expect(within(sheet).getByRole("button", { name: "Add tag" })).toBeDisabled();
    expect(within(sheet).getByText(/reserved for vigo's own tags/)).toBeInTheDocument();
  });

  it("explains what delete removes, and hides it for Art Store", async () => {
    const { user, backend } = await gallery("/gallery");
    const sheet = await openDetails(user, "vik-beach.jpg");
    await user.click(within(sheet).getByRole("button", { name: "Delete" }));
    const confirm = await screen.findByRole("alertdialog");
    expect(confirm).toHaveTextContent(/deletes the file 2024\/iceland\/vik-beach\.jpg from \/srv\/photos and removes it from the TV/);
    const spy = vi.spyOn(backend.library, "deleteArtwork");
    await user.click(within(confirm).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(spy).toHaveBeenCalledOnce());

    await user.type(screen.getByRole("searchbox", { name: "Search" }), "SAM-F0101");
    await waitFor(() => expect(screen.getAllByRole("button", { name: /^Details for/ })).toHaveLength(1));
    const store = await openDetails(user, "SAM-F0101");
    expect(within(store).queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
  });

  it("lists every copy when the same image is in several places", async () => {
    const { user, backend } = await gallery("/gallery");
    const sheet = await openDetails(user, "water-lilies.jpg");
    await user.click(within(sheet).getByRole("button", { name: "Delete" }));
    const confirm = await screen.findByRole("alertdialog");
    expect(confirm).toHaveTextContent("stored in 2 places. All of these files will be deleted");
    const files = within(confirm).getByRole("list", { name: "Files to delete" });
    expect(within(files).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "/srv/photos/paintings/water-lilies.jpg",
      "/mnt/nas/family/art/water-lilies.jpg",
    ]);
    const spy = vi.spyOn(backend.library, "deleteArtwork");
    await user.click(within(confirm).getByRole("button", { name: "Delete all 2" }));
    await waitFor(() => expect(spy).toHaveBeenCalledOnce());
  });

  it("words delete for TV-only uploads", async () => {
    const { user } = await gallery("/gallery");
    await user.type(screen.getByRole("searchbox", { name: "Search" }), "MY_F0001");
    await waitFor(() => expect(screen.getAllByRole("button", { name: /^Details for/ })).toHaveLength(1));
    const sheet = await openDetails(user, "MY_F0001");
    await user.click(within(sheet).getByRole("button", { name: "Delete" }));
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("removes it from the TV");
  });

  it("crops to 16:9 in original pixels and warns that it overwrites the file", async () => {
    const { user, backend } = await gallery("/gallery");
    const spy = vi.spyOn(backend.library, "cropArtwork");
    const sheet = await openDetails(user, "alfama.jpg");
    await user.click(within(sheet).getByRole("button", { name: "Crop" }));

    const editor = await screen.findByRole("dialog", { name: "Crop to 16:9" });
    expect(within(editor).getByText(/overwrites the original file/)).toBeInTheDocument();
    expect(within(editor).getByTestId("crop-size")).toHaveTextContent("3024 × 1701 px of 3024 × 4032");

    within(editor).getByRole("group", { name: "Crop frame" }).focus();
    await user.keyboard("--");
    const [, w, h] = /(\d+) × (\d+) px/.exec(within(editor).getByTestId("crop-size").textContent!)!.map(Number);
    expect(w).toBeLessThan(3024);
    expect(w % 16).toBe(0);
    expect(h).toBe((w / 16) * 9);

    await user.click(within(editor).getByRole("button", { name: "Overwrite original" }));
    await waitFor(() => expect(spy).toHaveBeenCalledOnce());
    const rect = spy.mock.calls[0][1]!;
    expect(rect.width / rect.height).toBe(16 / 9);
    expect(rect.width).toBe(w);
    expect(rect.x + rect.width).toBeLessThanOrEqual(3024);
    expect(rect.y + rect.height).toBeLessThanOrEqual(4032);
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Crop to 16:9" })).not.toBeInTheDocument());
  });

  it("offers crop only for wrong-ratio artworks in editable sources", async () => {
    const { user } = await gallery("/gallery");
    const ok = await openDetails(user, "tram-28.jpg");
    expect(within(ok).queryByRole("button", { name: "Crop" })).not.toBeInTheDocument();
  });
});

describe("Upload", () => {
  it("picks a destination and folder", async () => {
    const { user, backend } = await gallery("/gallery");
    const spy = vi.spyOn(backend.art, "upload");
    await user.click(screen.getByRole("button", { name: "Upload" }));
    const dialog = await screen.findByRole("dialog", { name: "Add photos" });

    const dest = within(dialog).getByRole("radiogroup", { name: "Destination" });
    expect(within(dest).getAllByRole("radio")).toHaveLength(2);
    expect(within(dest).getByRole("radio", { name: /\/srv\/photos/ })).toHaveAttribute("aria-checked", "true");
    await user.click(within(dest).getByRole("radio", { name: /\/mnt\/nas\/family/ }));
    await user.type(within(dialog).getByRole("combobox", { name: "Folder (optional)" }), "kids/2025");

    const file = new File(["png"], "drawing.png", { type: "image/png" });
    await user.upload(within(dialog).getByLabelText("Photos to upload"), file);
    await user.click(within(dialog).getByRole("button", { name: "Upload" }));

    await waitFor(() => expect(spy).toHaveBeenCalledOnce());
    expect(spy.mock.calls[0][0]).toBe("family");
    expect(spy.mock.calls[0][1]).toBe(file);
    expect(spy.mock.calls[0][2]).toMatchObject({ path: "kids/2025" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Add photos" })).not.toBeInTheDocument());
    const added = backend.library.artworks.find((a) => a.sources[0]?.path === "kids/2025/drawing.png");
    expect(added?.tags.map((t) => t.name)).toContain("folder/kids/2025");
  });

  it("needs no picker with one writable source", async () => {
    const { user } = await gallery("/gallery", { sources: "single" });
    await user.click(screen.getByRole("button", { name: "Upload" }));
    const dialog = await screen.findByRole("dialog", { name: "Add photos" });
    expect(within(dialog).queryByRole("radiogroup")).not.toBeInTheDocument();
    expect(within(dialog).getByText("/srv/photos")).toBeInTheDocument();
  });

  it("is disabled with an explanation when no source is writable", async () => {
    await gallery("/gallery", { sources: "read-only" });
    await waitFor(() => expect(screen.getByText(/No source accepts uploads/)).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Upload" })).toBeDisabled();
  });
});

describe("Upload errors", () => {
  it("validates the folder like the server", async () => {
    const { user } = await gallery("/gallery");
    await user.click(screen.getByRole("button", { name: "Upload" }));
    const dialog = await screen.findByRole("dialog", { name: "Add photos" });
    await user.upload(within(dialog).getByLabelText("Photos to upload"), new File(["x"], "a.png", { type: "image/png" }));
    const folder = within(dialog).getByRole("combobox", { name: "Folder (optional)" });

    await user.type(folder, "../outside");
    expect(folder).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getByText(/must stay inside the source/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Upload" })).toBeDisabled();

    await user.clear(folder);
    await user.type(folder, "2024/.cache");
    expect(within(dialog).getByText(/hidden/)).toBeInTheDocument();
    await user.clear(folder);
    expect(within(dialog).getByRole("button", { name: "Upload" })).toBeEnabled();
  });

  it.each([
    [Code.AlreadyExists, "a.png already exists in 2024", "That name is already taken", /rename the photo or choose another folder/],
    [Code.ResourceExhausted, "file is larger than 64 MiB", "Too large", /larger than 64 MiB/],
    [Code.InvalidArgument, "Only JPEG and PNG files are accepted.", "Couldn't upload", /Only JPEG and PNG/],
    [Code.FailedPrecondition, "the source is read-only", "Couldn't upload right now", /read-only/],
    [Code.Unavailable, "", "Couldn't upload", /couldn't be reached/],
  ])("explains %s in a toast and on the file", async (code, message, title, detail) => {
    const { user, backend } = await gallery("/gallery");
    vi.spyOn(backend.art, "upload").mockRejectedValue(new ConnectError(message, code));
    await user.click(screen.getByRole("button", { name: "Upload" }));
    const dialog = await screen.findByRole("dialog", { name: "Add photos" });
    await user.upload(within(dialog).getByLabelText("Photos to upload"), new File(["x"], "a.png", { type: "image/png" }));
    await user.click(within(dialog).getByRole("button", { name: "Upload" }));

    const toast = await screen.findByText(title, { selector: "[data-title]" });
    expect(toast.closest("[data-sonner-toast]")).toHaveTextContent(detail);
    expect(within(dialog).getByRole("list", { name: "Photos" })).toHaveTextContent(detail);
  });
});

describe("Sources", () => {
  it("lists sources and syncs on demand", async () => {
    const { user, backend } = await gallery("/gallery", { transitionMs: 200 });
    const row = document.querySelector('[data-source="photos"]') as HTMLElement;
    expect(row).toHaveTextContent("/srv/photos");
    expect(row).toHaveTextContent("12 artworks");
    await user.click(within(row).getByRole("button", { name: "Sync now" }));
    expect(await within(row).findByRole("button", { name: "Syncing…" })).toBeDisabled();
    await waitFor(() => expect(within(row).getByRole("button", { name: "Sync now" })).toBeEnabled(), { timeout: 2000 });
    expect(backend.library.sources[0].syncing).toBe(false);
  });

  it("refetches when the library changes", async () => {
    const { backend } = await gallery("/gallery");
    const before = backend.library.requests.length;
    const art = backend.library.artworks.find((a) => a.syncState === SyncState.IN_SYNC && a.sources.length)!;
    backend.library.updateTags(art.id, ["new"], []);
    await waitFor(() => expect(backend.library.requests.length).toBeGreaterThan(before));
  });
});
