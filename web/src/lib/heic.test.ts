import { afterEach, describe, expect, it, vi } from "vitest";

import { isHeic, MAX_LONG_SIDE, prepareForUpload } from "@/lib/heic";

function heicBytes(): Uint8Array<ArrayBuffer> {
  const b = new Uint8Array(32);
  b.set([0, 0, 0, 24], 0);
  b.set([..."ftypheic"].map((c) => c.charCodeAt(0)), 4);
  return b;
}

function stubCanvas() {
  const drawImage = vi.fn();
  const canvases: HTMLCanvasElement[] = [];
  const realCreate = document.createElement.bind(document);
  vi.spyOn(document, "createElement").mockImplementation((tag: string, opts?: ElementCreationOptions) => {
    const el = realCreate(tag, opts);
    if (tag === "canvas") {
      const canvas = el as HTMLCanvasElement;
      canvases.push(canvas);
      canvas.getContext = (() => ({ drawImage, imageSmoothingQuality: "low" })) as unknown as HTMLCanvasElement["getContext"];
      canvas.toBlob = (cb: BlobCallback, type?: string) => cb(new Blob(["jpeg"], { type }));
    }
    return el;
  });
  return { drawImage, canvases };
}

describe("HEIC handling", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("detects HEIC by type, extension and magic bytes", async () => {
    expect(await isHeic(new File(["x"], "a", { type: "image/heic" }))).toBe(true);
    expect(await isHeic(new File(["x"], "IMG_1.HEIF"))).toBe(true);
    expect(await isHeic(new File([heicBytes()], "photo", { type: "" }))).toBe(true);
    expect(await isHeic(new File(["\xff\xd8\xff\xe0 jpeg data"], "a.jpg", { type: "image/jpeg" }))).toBe(false);
  });

  it("converts HEIC to JPEG with createImageBitmap, capping the long side", async () => {
    const close = vi.fn();
    const createImageBitmap = vi.fn(async () => ({ width: 6048, height: 8064, close }));
    vi.stubGlobal("createImageBitmap", createImageBitmap);
    const { drawImage, canvases } = stubCanvas();

    const input = new File([heicBytes()], "IMG_0042.HEIC", { type: "image/heic" });
    const out = await prepareForUpload(input);

    expect(createImageBitmap).toHaveBeenCalledWith(input, { imageOrientation: "from-image" });
    expect(out.name).toBe("IMG_0042.jpg");
    expect(out.type).toBe("image/jpeg");
    expect(canvases[0].height).toBe(MAX_LONG_SIDE);
    expect(canvases[0].width).toBe(3072);
    expect(drawImage).toHaveBeenCalledOnce();
    expect(close).toHaveBeenCalled();
  });

  it("passes other images through untouched", async () => {
    const createImageBitmap = vi.fn();
    vi.stubGlobal("createImageBitmap", createImageBitmap);
    const jpeg = new File(["\xff\xd8\xff"], "a.jpg", { type: "image/jpeg" });
    expect(await prepareForUpload(jpeg)).toBe(jpeg);
    expect(createImageBitmap).not.toHaveBeenCalled();
  });
});
