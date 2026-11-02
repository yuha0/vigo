import { Check, CircleAlert, FolderOpen, ImagePlus, LoaderCircle, Upload, X } from "lucide-react";
import { useEffect, useId, useRef, useState, type DragEvent } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import type { Source } from "@/gen/vigo/core/v1/library_pb";
import { folderPath } from "@/features/gallery/folder-path";
import { describeError, toastError } from "@/lib/errors";
import { prepareForUpload } from "@/lib/heic";
import { useArtApi } from "@/lib/backend";
import { cn } from "@/lib/utils";

type Item = {
  key: string;
  file: File;
  status: "queued" | "converting" | "uploading" | "done" | "error";
  progress: number;
  error?: string;
};

export function UploadDialog({
  open,
  onOpenChange,
  sources,
  folders,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Writable sources only. */
  sources: Source[];
  /** Known folders, offered as suggestions. */
  folders: string[];
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">{open && <UploadFlow sources={sources} folders={folders} onDone={() => onOpenChange(false)} />}</DialogContent>
    </Dialog>
  );
}

let seq = 0;

function UploadFlow({ sources, folders, onDone }: { sources: Source[]; folders: string[]; onDone: () => void }) {
  const art = useArtApi();
  const [destination, setDestination] = useState(sources[0]?.id ?? "");
  const [path, setPath] = useState("");
  const [items, setItems] = useState<Item[]>([]);
  const [running, setRunning] = useState(false);
  const [dragging, setDragging] = useState(false);
  const abort = useRef<AbortController | null>(null);
  const ids = { input: useId(), path: useId(), folders: useId() };
  const source = sources.find((s) => s.id === destination);
  const folder = folderPath(path);

  useEffect(() => () => abort.current?.abort(), []);

  const add = (files: FileList | File[] | null) => {
    if (!files) return;
    const next = [...files].map((file) => ({ key: `f${seq++}`, file, status: "queued" as const, progress: 0 }));
    setItems((cur) => [...cur.filter((i) => i.status !== "done"), ...next]);
  };

  const patch = (key: string, p: Partial<Item>) => setItems((cur) => cur.map((i) => (i.key === key ? { ...i, ...p } : i)));

  const start = async () => {
    if (!source || folder.error) return;
    setRunning(true);
    abort.current = new AbortController();
    const signal = abort.current.signal;
    let ok = 0;
    let failed = 0;
    for (const item of items.filter((i) => i.status === "queued" || i.status === "error")) {
      if (signal.aborted) break;
      try {
        patch(item.key, { status: "converting", progress: 0, error: undefined });
        const file = await prepareForUpload(item.file);
        patch(item.key, { status: "uploading" });
        await art.upload(source.id, file, { path: folder.value, signal, onProgress: (p) => patch(item.key, { progress: p }) });
        patch(item.key, { status: "done", progress: 1 });
        ok++;
      } catch (err) {
        if (signal.aborted) break;
        failed++;
        if (err instanceof Error && !("code" in err)) {
          patch(item.key, { status: "error", error: "This browser couldn't read the photo. Try exporting it as JPEG." });
          toastError(new Error("This browser couldn't read the photo. Try exporting it as JPEG."), { action: "Upload" });
        } else {
          const d = describeError(err, { action: "Upload" });
          patch(item.key, { status: "error", error: [d?.title, d?.description].filter(Boolean).join(". ") });
          toastError(err, { action: "Upload" });
        }
      }
    }
    setRunning(false);
    if (ok && !failed) {
      toast.success(ok === 1 ? "Photo added" : `${ok} photos added`, {
        description: `Saved to ${source.display}. They appear on the TV once uploaded.`,
      });
      onDone();
    }
  };

  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setDragging(false);
    add(e.dataTransfer.files);
  };

  const pending = items.filter((i) => i.status !== "done").length;

  return (
    <>
      <DialogHeader>
        <DialogTitle>Add photos</DialogTitle>
        <DialogDescription>
          Photos are saved to a source and then uploaded to the TV, padded to 16:9. To fill the screen instead, crop them afterwards.
        </DialogDescription>
      </DialogHeader>

      <div className="grid gap-5">
        {sources.length > 1 ? (
          <fieldset className="grid gap-2">
            <legend className="mb-2 text-[0.9375rem] font-medium">Save to</legend>
            <div role="radiogroup" aria-label="Destination" className="grid gap-2">
              {sources.map((s) => (
                <button
                  key={s.id}
                  type="button"
                  role="radio"
                  aria-checked={destination === s.id}
                  disabled={running}
                  onClick={() => setDestination(s.id)}
                  className={cn(
                    "flex min-h-12 items-center justify-between gap-3 rounded-xl px-4 py-2.5 text-left outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50",
                    destination === s.id ? "bg-accent ring-2 ring-brass" : "ring-1 ring-border hover:bg-muted/60",
                  )}
                >
                  <span className="min-w-0">
                    <span className="block truncate font-mono text-[0.8125rem]">{s.display}</span>
                    <span className="block text-xs text-muted-foreground">
                      {s.artworkCount} artworks{s.editable ? " · crop supported" : ""}
                    </span>
                  </span>
                  {destination === s.id && <Check className="size-4 shrink-0 text-brass" />}
                </button>
              ))}
            </div>
          </fieldset>
        ) : (
          <p className="text-sm text-muted-foreground">
            Saving to <span className="font-mono text-[0.8125rem] text-foreground">{source?.display}</span>
          </p>
        )}

        <div className="grid gap-2">
          <Label htmlFor={ids.path}>Folder (optional)</Label>
          <div className="relative">
            <FolderOpen className="pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              id={ids.path}
              value={path}
              onChange={(e) => setPath(e.target.value)}
              list={ids.folders}
              placeholder="e.g. 2024/iceland"
              className="pl-10"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              disabled={running}
              aria-invalid={!!folder.error || undefined}
              aria-describedby={`${ids.path}-hint`}
            />
            <datalist id={ids.folders}>
              {folders.map((f) => (
                <option key={f} value={f} />
              ))}
            </datalist>
          </div>
          <p id={`${ids.path}-hint`} className={cn("text-sm", folder.error ? "text-destructive" : "text-muted-foreground")}>
            {folder.error ?? "Relative to the source. Leave empty for its top level."}
          </p>
        </div>

        <label
          htmlFor={ids.input}
          onDragOver={(e) => {
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={onDrop}
          className={cn(
            "flex min-h-28 cursor-pointer flex-col items-center justify-center gap-2 rounded-2xl border-2 border-dashed border-border bg-muted/40 px-6 py-5 text-center transition-colors hover:bg-muted",
            dragging && "border-brass bg-accent",
            running && "pointer-events-none opacity-50",
          )}
        >
          <ImagePlus className="size-7 text-muted-foreground" strokeWidth={1.5} />
          <span className="font-medium">{items.length ? "Add more photos" : "Choose photos"}</span>
          <span className="text-sm text-muted-foreground">JPEG, PNG or HEIC. HEIC is converted to JPEG on this device.</span>
          <input
            id={ids.input}
            type="file"
            accept="image/*"
            multiple
            className="sr-only"
            aria-label="Photos to upload"
            disabled={running}
            onChange={(e) => {
              add(e.target.files);
              e.target.value = "";
            }}
          />
        </label>

        {items.length > 0 && (
          <ul className="grid max-h-56 gap-2 overflow-y-auto" aria-label="Photos">
            {items.map((i) => (
              <li key={i.key} className="grid gap-1.5 rounded-xl bg-muted/50 px-3.5 py-2.5">
                <div className="flex items-center gap-2 text-sm">
                  {i.status === "done" ? (
                    <Check className="size-4 shrink-0 text-success" />
                  ) : i.status === "error" ? (
                    <CircleAlert className="size-4 shrink-0 text-destructive" />
                  ) : i.status === "queued" ? (
                    <ImagePlus className="size-4 shrink-0 text-muted-foreground" />
                  ) : (
                    <LoaderCircle className="size-4 shrink-0 animate-spin text-muted-foreground" />
                  )}
                  <span className="min-w-0 flex-1 truncate">{i.file.name}</span>
                  <span className="shrink-0 text-xs text-muted-foreground">
                    {i.status === "converting" ? "Converting…" : i.status === "uploading" ? `${Math.round(i.progress * 100)}%` : i.status === "done" ? "Added" : ""}
                  </span>
                  {!running && i.status !== "done" && (
                    <button
                      type="button"
                      aria-label={`Remove ${i.file.name}`}
                      onClick={() => setItems((cur) => cur.filter((x) => x.key !== i.key))}
                      className="relative -my-2 -mr-2 flex size-9 items-center justify-center rounded-full text-muted-foreground before:absolute before:-inset-1 hover:bg-black/5 dark:hover:bg-white/10"
                    >
                      <X className="size-4" />
                    </button>
                  )}
                </div>
                {i.status === "uploading" && <Progress value={Math.round(i.progress * 100)} aria-label={`Uploading ${i.file.name}`} />}
                {i.error && <p className="text-xs text-destructive">{i.error}</p>}
              </li>
            ))}
          </ul>
        )}
      </div>

      <DialogFooter>
        {running ? (
          <Button variant="outline" onClick={() => abort.current?.abort()}>
            Stop
          </Button>
        ) : (
          <Button onClick={() => void start()} disabled={!pending || !source || !!folder.error}>
            <Upload /> {pending > 1 ? `Upload ${pending} photos` : "Upload"}
          </Button>
        )}
      </DialogFooter>
    </>
  );
}
