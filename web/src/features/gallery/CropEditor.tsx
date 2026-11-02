import { LoaderCircle, TriangleAlert } from "lucide-react";
import { useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import type { LibraryArtwork, Source } from "@/gen/vigo/core/v1/library_pb";
import {
  cropHeight,
  defaultCrop,
  moveCrop,
  ratioK,
  resizeCrop,
  scaleCrop,
  toPixels,
  type Corner,
  type Crop,
} from "@/features/gallery/crop-math";
import { useCropArtwork } from "@/features/gallery/library-queries";
import { artworkName } from "@/features/gallery/tags";
import { useArtApi } from "@/lib/backend";
import { cn } from "@/lib/utils";

type Gesture =
  | { kind: "move"; start: Crop; px: number; py: number }
  | { kind: "resize"; corner: Corner }
  | { kind: "pinch"; start: Crop; distance: number; mid: [number, number] };

const corners: Corner[] = ["nw", "ne", "sw", "se"];

export function CropEditor({
  artwork,
  sources,
  onOpenChange,
  onSaved,
}: {
  artwork: LibraryArtwork | null;
  sources: Source[];
  onOpenChange: (open: boolean) => void;
  onSaved: () => void;
}) {
  return (
    <Dialog open={!!artwork} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-3xl">
        {artwork && <Editor key={artwork.id} artwork={artwork} sources={sources} onSaved={onSaved} onCancel={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  );
}

function Editor({ artwork: a, sources, onSaved, onCancel }: { artwork: LibraryArtwork; sources: Source[]; onSaved: () => void; onCancel: () => void }) {
  const art = useArtApi();
  const crop = useCropArtwork();
  const k = ratioK(a.width, a.height);
  const [rect, setRect] = useState<Crop>(() => defaultCrop(a.width, a.height));
  const box = useRef<HTMLDivElement>(null);
  const pointers = useRef(new Map<number, [number, number]>());
  const gesture = useRef<Gesture | null>(null);
  const px = toPixels(rect, a.width, a.height);
  const ref = a.sources[0];
  const source = sources.find((s) => s.id === ref?.sourceId);

  const frac = (e: { clientX: number; clientY: number }): [number, number] => {
    const r = box.current!.getBoundingClientRect();
    return [(e.clientX - r.left) / (r.width || 1), (e.clientY - r.top) / (r.height || 1)];
  };

  const startPinch = () => {
    const [p1, p2] = [...pointers.current.values()];
    gesture.current = {
      kind: "pinch",
      start: rect,
      distance: Math.hypot(p2[0] - p1[0], p2[1] - p1[1]) || 1e-6,
      mid: [(p1[0] + p2[0]) / 2, (p1[1] + p2[1]) / 2],
    };
  };

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    const p = frac(e);
    pointers.current.set(e.pointerId, p);
    e.currentTarget.setPointerCapture?.(e.pointerId);
    if (pointers.current.size === 2) return startPinch();
    const corner = (e.target as HTMLElement).closest<HTMLElement>("[data-corner]")?.dataset.corner as Corner | undefined;
    if (corner) gesture.current = { kind: "resize", corner };
    else gesture.current = { kind: "move", start: rect, px: p[0], py: p[1] };
  };

  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    if (!pointers.current.has(e.pointerId)) return;
    const p = frac(e);
    pointers.current.set(e.pointerId, p);
    const g = gesture.current;
    if (!g) return;
    if (g.kind === "move") setRect(moveCrop(g.start, k, p[0] - g.px, p[1] - g.py));
    else if (g.kind === "resize") setRect((r) => resizeCrop(r, k, g.corner, p[0], p[1]));
    else if (pointers.current.size === 2) {
      const [p1, p2] = [...pointers.current.values()];
      const scaled = scaleCrop(g.start, k, Math.hypot(p2[0] - p1[0], p2[1] - p1[1]) / g.distance);
      setRect(moveCrop(scaled, k, (p1[0] + p2[0]) / 2 - g.mid[0], (p1[1] + p2[1]) / 2 - g.mid[1]));
    }
  };

  const onPointerUp = (e: PointerEvent<HTMLDivElement>) => {
    pointers.current.delete(e.pointerId);
    if (pointers.current.size === 1) {
      const [p] = [...pointers.current.values()];
      gesture.current = { kind: "move", start: rect, px: p[0], py: p[1] };
    } else if (pointers.current.size === 0) gesture.current = null;
  };

  const onKeyDown = (e: KeyboardEvent) => {
    const step = e.shiftKey ? 0.05 : 0.01;
    const moves: Record<string, [number, number]> = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] };
    if (moves[e.key]) setRect((r) => moveCrop(r, k, ...moves[e.key]));
    else if (e.key === "+" || e.key === "=") setRect((r) => scaleCrop(r, k, 1.05));
    else if (e.key === "-") setRect((r) => scaleCrop(r, k, 1 / 1.05));
    else return;
    e.preventDefault();
  };

  const save = () =>
    crop.mutate(
      { id: a.id, rect: px },
      {
        onSuccess: () => {
          toast.success("Cropped", { description: "The original file was replaced. The TV gets the new version on the next sync." });
          onSaved();
        },
      },
    );

  const h = cropHeight(rect, k);
  return (
    <>
      <DialogHeader>
        <DialogTitle>Crop to 16:9</DialogTitle>
        <DialogDescription>Drag the frame to move it, drag a corner or pinch to resize. Arrow keys and + / − work too.</DialogDescription>
      </DialogHeader>

      <div className="flex justify-center">
        <div
          ref={box}
          data-testid="crop-area"
          className="relative touch-none overflow-hidden rounded-lg bg-neutral-900 select-none"
          style={{ aspectRatio: `${a.width} / ${a.height}`, width: `min(100%, calc(56dvh * ${a.width / a.height}))` }}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerCancel={onPointerUp}
        >
          <img src={art.imageUrl(a.id)} alt={artworkName(a)} draggable={false} className="pointer-events-none absolute inset-0 size-full object-fill" />
          <div
            role="group"
            tabIndex={0}
            aria-label="Crop frame"
            onKeyDown={onKeyDown}
            data-testid="crop-rect"
            className="absolute cursor-move outline-none ring-2 ring-white shadow-[0_0_0_9999px_rgb(0_0_0/0.55)] focus-visible:ring-brass"
            style={{ left: `${rect.x * 100}%`, top: `${rect.y * 100}%`, width: `${rect.w * 100}%`, height: `${h * 100}%` }}
          >
            <div className="pointer-events-none absolute inset-0 grid grid-cols-3 grid-rows-3">
              {Array.from({ length: 9 }, (_, i) => (
                <span key={i} className="border-white/25 [&:nth-child(-n+6)]:border-b [&:not(:nth-child(3n))]:border-r" />
              ))}
            </div>
            {corners.map((c) => (
              <span
                key={c}
                data-corner={c}
                aria-hidden="true"
                className={cn(
                  "absolute flex size-11 items-center justify-center",
                  c.startsWith("n") ? "-top-[22px]" : "-bottom-[22px]",
                  c.endsWith("w") ? "-left-[22px]" : "-right-[22px]",
                  c === "nw" || c === "se" ? "cursor-nwse-resize" : "cursor-nesw-resize",
                )}
              >
                <span className="size-3.5 rounded-sm bg-white shadow" />
              </span>
            ))}
          </div>
        </div>
      </div>

      <p className="text-center text-sm text-muted-foreground tabular-nums" aria-live="polite" data-testid="crop-size">
        {px.width} × {px.height} px of {a.width} × {a.height}
      </p>

      <div role="alert" className="flex gap-3 rounded-2xl bg-warning-surface px-4 py-3 text-sm text-warning-foreground ring-1 ring-warning/30">
        <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
        <p>
          Saving <strong>overwrites the original file</strong>
          {ref && (
            <>
              {" "}
              <span className="font-mono text-[0.8125rem] break-all">{ref.path}</span>
              {source && <> in {source.display}</>}
            </>
          )}{" "}
          with the cropped image. vigo keeps no copy of the original.
        </p>
      </div>

      <DialogFooter>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button variant="destructive" onClick={save} disabled={crop.isPending}>
          {crop.isPending && <LoaderCircle className="animate-spin" />}
          Overwrite original
        </Button>
      </DialogFooter>
    </>
  );
}
