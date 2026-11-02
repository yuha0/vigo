import { decodeOriented } from "@/lib/image";

/** iOS caps canvas size; 4096 is safely under it and still above the TV's 3840. */
export const MAX_LONG_SIDE = 4096;

const heicBrands = new Set(["heic", "heix", "hevc", "hevx", "heim", "heis", "hevm", "hevs", "mif1", "msf1"]);

async function head(file: Blob, n: number): Promise<Uint8Array> {
  const slice = file.slice(0, n);
  if (typeof slice.arrayBuffer === "function") return new Uint8Array(await slice.arrayBuffer());
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(new Uint8Array(r.result as ArrayBuffer));
    r.onerror = () => reject(r.error);
    r.readAsArrayBuffer(slice);
  });
}

/** True for HEIC/HEIF, judged by MIME type, extension, or the ISO-BMFF "ftyp" brand. */
export async function isHeic(file: File): Promise<boolean> {
  if (/^image\/hei[cf](-sequence)?$/i.test(file.type)) return true;
  if (/\.hei[cf]$/i.test(file.name)) return true;
  const b = await head(file, 12);
  if (b.length < 12) return false;
  const ftyp = String.fromCharCode(b[4], b[5], b[6], b[7]);
  const brand = String.fromCharCode(b[8], b[9], b[10], b[11]);
  return ftyp === "ftyp" && heicBrands.has(brand);
}

/**
 * vigo-core can't decode HEIC, which iOS hands to installed web apps, so the
 * browser converts it to JPEG first. Orientation is baked in by the decoder.
 */
export async function heicToJpeg(file: File, maxLongSide = MAX_LONG_SIDE): Promise<File> {
  const bmp = await decodeOriented(file);
  try {
    const scale = Math.min(1, maxLongSide / Math.max(bmp.width, bmp.height));
    const w = Math.max(1, Math.round(bmp.width * scale));
    const h = Math.max(1, Math.round(bmp.height * scale));
    const canvas = document.createElement("canvas");
    canvas.width = w;
    canvas.height = h;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("no 2D canvas");
    ctx.imageSmoothingQuality = "high";
    ctx.drawImage(bmp, 0, 0, w, h);
    const blob = await new Promise<Blob | null>((r) => canvas.toBlob(r, "image/jpeg", 0.92));
    if (!blob) throw new Error("JPEG encoding failed");
    const name = file.name.replace(/\.[^.]+$/, "") + ".jpg";
    return new File([blob], name, { type: "image/jpeg", lastModified: file.lastModified });
  } finally {
    bmp.close();
  }
}

/** Returns the file to upload: HEIC converted to JPEG, anything else unchanged. */
export async function prepareForUpload(file: File): Promise<File> {
  return (await isHeic(file)) ? heicToJpeg(file) : file;
}
