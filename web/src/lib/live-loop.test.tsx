import { create } from "@bufbuild/protobuf";
import { timestampNow } from "@bufbuild/protobuf/wkt";
import { act, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LibraryChangedSchema, StatusSchema } from "@/gen/vigo/core/v1/core_pb";
import { renderApp } from "@/test/render";

const sleep = (ms: number) => act(() => new Promise<void>((r) => setTimeout(r, ms)));
const count = (calls: string[], name: string) => calls.filter((c) => c === name).length;

async function gallery(options: Parameters<typeof renderApp>[1] = {}) {
  localStorage.clear();
  const r = renderApp("/gallery", options);
  await screen.findByRole("list", { name: "Artworks" });
  await sleep(300);
  return r;
}

// Regression for the production loop: each WatchEvents ended right after its first Status,
// the client reconnected every ~0.6 s and refetched (and cancelled) the whole library each time.
describe("live updates don't loop", () => {
  afterEach(() => vi.restoreAllMocks());

  it("keeps one stream and doesn't refetch the library on Status snapshots", async () => {
    const { backend } = await gallery();
    const before = [...backend.calls];

    for (let i = 0; i < 10; i++) {
      backend.tv.emit({ case: "status", value: create(StatusSchema, { ...backend.tv.status, updatedAt: timestampNow() }) });
      await sleep(100);
    }

    expect(count(backend.calls, "TVService/WatchEvents")).toBe(1);
    const after = backend.calls.slice(before.length);
    expect(after.filter((c) => c.startsWith("LibraryService/"))).toEqual([]);
  });

  it("coalesces LibraryChanged bursts instead of cancelling and restarting fetches", async () => {
    const { backend } = await gallery({ latencyMs: 150 });
    const before = count(backend.calls, "LibraryService/ListArtworks");

    for (let i = 0; i < 10; i++) {
      backend.tv.emit({ case: "libraryChanged", value: create(LibraryChangedSchema, { artworkIds: ["x"] }) });
      await sleep(20);
    }
    await sleep(600);

    expect(count(backend.calls, "TVService/WatchEvents")).toBe(1);
    // Ten events inside one fetch round trip: in-flight fetches are kept, so this stays near one refetch
    // (page + two tab counts) per in-flight window rather than three per event.
    expect(count(backend.calls, "LibraryService/ListArtworks") - before).toBeLessThanOrEqual(9);
  });

  it("backs off, and doesn't refetch the library, when every stream ends after its first Status", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const { backend } = await gallery({ dropStreamAfterFirst: true });
    const listsBefore = count(backend.calls, "LibraryService/ListArtworks");
    await sleep(3000);

    // 0.5 s, 1 s, 2 s (±25%) between attempts: at most four opens in ~3.3 s. Before the fix, ~7.
    const opens = count(backend.calls, "TVService/WatchEvents");
    expect(opens).toBeGreaterThanOrEqual(2);
    expect(opens).toBeLessThanOrEqual(4);
    expect(count(backend.calls, "LibraryService/ListArtworks")).toBe(listsBefore);
    expect(screen.getByRole("list", { name: "Artworks" })).toBeInTheDocument();
    expect(console.warn).toHaveBeenCalledWith("vigo: live updates disconnected; retrying", expect.anything());
  }, 10_000);
});
