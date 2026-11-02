/** Decodes an image with its EXIF orientation applied. */
export function decodeOriented(file: Blob): Promise<ImageBitmap> {
  return createImageBitmap(file, { imageOrientation: "from-image" });
}

/** Draws `img` into a w×h frame keeping the whole image, with black bars, as vigo-core pads uploads for the TV. */
export function drawPadded(ctx: CanvasRenderingContext2D, img: ImageBitmap, w: number, h: number) {
  ctx.fillStyle = "#000";
  ctx.fillRect(0, 0, w, h);
  const scale = Math.min(w / img.width, h / img.height);
  const dw = img.width * scale;
  const dh = img.height * scale;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(img, (w - dw) / 2, (h - dh) / 2, dw, dh);
}
