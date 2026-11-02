import { create } from "@bufbuild/protobuf";

import { RectSchema, type Rect } from "@/gen/vigo/core/v1/library_pb";

/**
 * A crop rectangle in fractions of the image (0..1). Its height is derived
 * from its width so it is always 16:9 in pixels: h = k·w with k = (W/H)·9/16.
 */
export type Crop = { x: number; y: number; w: number };

export type Corner = "nw" | "ne" | "sw" | "se";

export const MIN_WIDTH = 0.08;

export function ratioK(width: number, height: number): number {
  return (width / height) * (9 / 16);
}

export function cropHeight(c: Crop, k: number): number {
  return c.w * k;
}

function maxWidth(k: number): number {
  return Math.min(1, 1 / k);
}

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

/** The largest centered 16:9 rectangle. */
export function defaultCrop(width: number, height: number): Crop {
  const k = ratioK(width, height);
  const w = maxWidth(k);
  return { x: (1 - w) / 2, y: (1 - k * w) / 2, w };
}

export function moveCrop(c: Crop, k: number, dx: number, dy: number): Crop {
  return { w: c.w, x: clamp(c.x + dx, 0, 1 - c.w), y: clamp(c.y + dy, 0, 1 - k * c.w) };
}

/** Resizes by dragging a corner to (px, py), keeping the opposite corner fixed. */
export function resizeCrop(c: Crop, k: number, corner: Corner, px: number, py: number): Crop {
  const sx = corner.endsWith("e") ? 1 : -1;
  const sy = corner.startsWith("s") ? 1 : -1;
  const ax = sx > 0 ? c.x : c.x + c.w;
  const ay = sy > 0 ? c.y : c.y + k * c.w;
  const wanted = Math.max(sx * (px - ax), (sy * (py - ay)) / k);
  const limit = Math.min(sx > 0 ? 1 - ax : ax, (sy > 0 ? 1 - ay : ay) / k);
  const w = clamp(wanted, Math.min(MIN_WIDTH, limit), limit);
  return { w, x: sx > 0 ? ax : ax - w, y: sy > 0 ? ay : ay - k * w };
}

/** Scales around the rectangle's center (pinch, keyboard), then keeps it inside the image. */
export function scaleCrop(c: Crop, k: number, factor: number): Crop {
  const cx = c.x + c.w / 2;
  const cy = c.y + (k * c.w) / 2;
  const w = clamp(c.w * factor, MIN_WIDTH, maxWidth(k));
  return { w, x: clamp(cx - w / 2, 0, 1 - w), y: clamp(cy - (k * w) / 2, 0, 1 - k * w) };
}

/**
 * Maps the crop to pixels of the upright original. The width is a multiple of
 * 16 so the height is exactly 9/16 of it.
 */
export function toPixels(c: Crop, width: number, height: number): Rect {
  const fit = Math.min(width, Math.floor((height * 16) / 9));
  const w16 = Math.max(16, Math.min(Math.round(c.w * width), fit));
  const pw = Math.floor(w16 / 16) * 16;
  const ph = (pw / 16) * 9;
  return create(RectSchema, {
    x: clamp(Math.round(c.x * width), 0, width - pw),
    y: clamp(Math.round(c.y * height), 0, height - ph),
    width: pw,
    height: ph,
  });
}
