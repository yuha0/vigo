import { ImageDown, LoaderCircle, Trash2, X } from "lucide-react";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import type { LibraryArtwork, Source } from "@/gen/vigo/core/v1/library_pb";
import { batchToast } from "@/features/gallery/batch";
import { useDeleteArtworks, useRefreshThumbnails } from "@/features/gallery/library-queries";
import { artworkName, canDelete, canRefresh } from "@/features/gallery/tags";

/** The action bar of select mode, pinned to the bottom of the screen. */
export function SelectionBar({
  selected,
  sources,
  selectableCount,
  onSelectAll,
  onClear,
  onCancel,
  onDone,
}: {
  selected: LibraryArtwork[];
  sources: Source[];
  selectableCount: number;
  onSelectAll: () => void;
  onClear: () => void;
  onCancel: () => void;
  onDone: () => void;
}) {
  const del = useDeleteArtworks();
  const refresh = useRefreshThumbnails();
  const deletable = selected.filter(canDelete);
  const refreshable = selected.filter(canRefresh);
  const skipped = selected.length - deletable.length;
  const name = (id: string) => {
    const a = selected.find((x) => x.id === id);
    return a ? artworkName(a) : id;
  };
  const display = (id: string) => sources.find((s) => s.id === id)?.display ?? id;
  const multiCopy = deletable.filter((a) => a.sources.length > 1);

  return (
    <div
      role="toolbar"
      aria-label="Selection"
      className="sticky bottom-[max(0.75rem,env(safe-area-inset-bottom))] z-20 mt-8 rounded-2xl border border-border/70 bg-popover/95 p-3 shadow-[0_12px_40px_-12px_rgb(0_0_0/0.35)] backdrop-blur-xl animate-in fade-in slide-in-from-bottom-4"
    >
      <div className="flex flex-wrap items-center gap-2">
        <p className="min-w-0 flex-1 basis-40 px-1 text-[0.9375rem] font-medium" aria-live="polite">
          {selected.length} selected
          {skipped > 0 && (
            <span className="block text-xs font-normal text-muted-foreground">
              {skipped} can't be deleted (Art Store or already deleting)
            </span>
          )}
        </p>
        <Button variant="ghost" onClick={onSelectAll} disabled={selected.length === selectableCount}>
          Select all
        </Button>
        <Button variant="ghost" onClick={onClear} disabled={!selected.length}>
          Clear
        </Button>
        <Button variant="ghost" size="icon" onClick={onCancel} aria-label="Cancel selecting">
          <X />
        </Button>
      </div>
      <div className="mt-2 flex flex-wrap gap-2 border-t border-border/70 pt-3">
        <Button
          variant="outline"
          className="flex-1"
          disabled={!refreshable.length || refresh.isPending}
          title="Downloads these thumbnails from the TV again. vigo keeps the TV's thumbnail and doesn't re-download it unless asked."
          onClick={() =>
            refresh.mutate(
              refreshable.map((a) => a.id),
              { onSuccess: (results) => batchToast("Refreshed", "thumbnail", results, name) },
            )
          }
        >
          {refresh.isPending ? <LoaderCircle className="animate-spin" /> : <ImageDown />}
          Refresh thumbnail{refreshable.length === 1 ? "" : "s"}
          {refreshable.length > 0 && <span className="tabular-nums opacity-60">{refreshable.length}</span>}
        </Button>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="destructive-outline" className="flex-1" disabled={!deletable.length || del.isPending}>
              {del.isPending ? <LoaderCircle className="animate-spin" /> : <Trash2 />}
              Delete
              {deletable.length > 0 && <span className="tabular-nums opacity-70">{deletable.length}</span>}
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                Delete {deletable.length} artwork{deletable.length === 1 ? "" : "s"}?
              </AlertDialogTitle>
              <AlertDialogDescription asChild>
                <div className="grid gap-2">
                  <p>
                    Files are deleted from their sources and removed from the TV. Uploads made elsewhere, which have no source file, are only
                    removed from the TV. This can't be undone.
                  </p>
                  {skipped > 0 && <p>{skipped} selected item{skipped === 1 ? " is" : "s are"} skipped: Art Store art can't be deleted.</p>}
                  {multiCopy.length > 0 && (
                    <>
                      <p>These are stored in more than one place; every copy will be deleted:</p>
                      <ul className="grid max-h-40 gap-1.5 overflow-y-auto rounded-xl bg-muted/60 px-3.5 py-2.5" aria-label="Files to delete">
                        {multiCopy.flatMap((a) =>
                          a.sources.map((r) => (
                            <li key={`${a.id}/${r.sourceId}/${r.path}`} className="font-mono text-[0.8125rem] break-all text-foreground">
                              {display(r.sourceId)}/{r.path}
                            </li>
                          )),
                        )}
                      </ul>
                    </>
                  )}
                </div>
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Keep</AlertDialogCancel>
              <AlertDialogAction
                onClick={() =>
                  del.mutate(
                    deletable.map((a) => a.id),
                    {
                      onSuccess: (results) => {
                        batchToast("Deleted", "artwork", results, name);
                        onDone();
                      },
                    },
                  )
                }
              >
                Delete {deletable.length}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    </div>
  );
}
