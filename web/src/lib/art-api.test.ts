import { Code } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { createHttpArtApi } from "@/lib/art-api";

describe("createHttpArtApi", () => {
  it("builds image URLs", () => {
    const api = createHttpArtApi();
    expect(api.tvThumbnailUrl("MY_F0001")).toBe("/art/MY_F0001/thumbnail");
    expect(api.thumbnailUrl("sha256:ab")).toBe("/library/sha256%3Aab/thumbnail");
    expect(api.imageUrl("tv:SAM-F0101")).toBe("/library/tv%3ASAM-F0101/image");
  });

  it("posts to the source and maps error bodies to Connect codes", async () => {
    const OriginalXHR = globalThis.XMLHttpRequest;
    const opened: string[] = [];
    const sent: FormData[] = [];
    class FakeXHR {
      status = 412;
      responseText = JSON.stringify({ code: "failed_precondition", message: "source is read-only" });
      responseType = "";
      upload = { onprogress: null };
      onload: (() => void) | null = null;
      open(_m: string, url: string) {
        opened.push(url);
      }
      send(body: FormData) {
        sent.push(body);
        queueMicrotask(() => this.onload?.());
      }
      abort() {}
    }
    globalThis.XMLHttpRequest = FakeXHR as unknown as typeof XMLHttpRequest;
    try {
      const err = await createHttpArtApi()
        .upload("photos", new File(["x"], "a.jpg", { type: "image/jpeg" }), { path: "2024/iceland" })
        .catch((e: unknown) => e);
      expect(err).toMatchObject({ code: Code.FailedPrecondition, rawMessage: "source is read-only" });
      expect(opened).toEqual(["/sources/photos/files"]);
      expect(sent[0].get("path")).toBe("2024/iceland");
    } finally {
      globalThis.XMLHttpRequest = OriginalXHR;
    }
  });
});
