import { Crop, Folder, ImageDown, LoaderCircle, Plus, Smartphone, Sparkles, Store, Trash2, X } from "lucide-react";
import { useId, useState, type FormEvent } from "react";
import { toast } from "sonner";

import { ArtImage } from "@/components/ArtImage";
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
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { SyncState, TagOrigin, type LibraryArtwork, type Source } from "@/gen/vigo/core/v1/library_pb";
import { SyncBadge } from "@/features/gallery/ArtworkTile";
import { batchToast } from "@/features/gallery/batch";
import { useDeleteArtwork, useRefreshThumbnails, useUpdateTags } from "@/features/gallery/library-queries";
import { artworkName, canCrop, isArtStore, isDeleting, issueText, provenance, tagLabel } from "@/features/gallery/tags";
import { useArtApi } from "@/lib/backend";
import { cn } from "@/lib/utils";

const syncText: Record<SyncState, string> = {
  [SyncState.UNSPECIFIED]: "Unknown",
  [SyncState.IN_SYNC]: "On the TV and in sync",
  [SyncState.PENDING_UPLOAD]: "Waiting to upload to the TV",
  [SyncState.UPLOADING]: "Uploading to the TV",
  [SyncState.PENDING_DELETE]: "Waiting to be deleted from the TV",
  [SyncState.DELETING]: "Being deleted from the TV",
  [SyncState.ERROR]: "Sync failed",
  [SyncState.TV_ONLY]: "On the TV only",
};

function megabytes(n: bigint): string {
  const mb = Number(n) / 1_000_000;
  return mb >= 10 ? `${mb.toFixed(0)} MB` : `${mb.toFixed(1)} MB`;
}

export function ArtworkSheet({
  artwork,
  sources,
  selected,
  guess = false,
  pending,
  busy,
  onOpenChange,
  onSelect,
  onCrop,
}: {
  artwork: LibraryArtwork | null;
  sources: Source[];
  selected: boolean;
  guess?: boolean;
  pending: boolean;
  busy: boolean;
  onOpenChange: (open: boolean) => void;
  onSelect: (tvContentId: string) => void;
  onCrop: (id: string) => void;
}) {
  return (
    <Dialog open={!!artwork} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        {artwork && (
          <Details
            artwork={artwork}
            sources={sources}
            selected={selected}
            guess={guess}
            pending={pending}
            busy={busy}
            onSelect={onSelect}
            onCrop={onCrop}
            onDeleted={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

function Details({
  artwork: a,
  sources,
  selected,
  guess,
  pending,
  busy,
  onSelect,
  onCrop,
  onDeleted,
}: {
  artwork: LibraryArtwork;
  sources: Source[];
  selected: boolean;
  guess?: boolean;
  pending: boolean;
  busy: boolean;
  onSelect: (tvContentId: string) => void;
  onCrop: (id: string) => void;
  onDeleted: () => void;
}) {
  const art = useArtApi();
  const name = artworkName(a);
  const ref = a.sources[0];
  const source = sources.find((s) => s.id === ref?.sourceId);
  const deleting = isDeleting(a);
  const issues = a.tags.filter((t) => issueText[t.name]);
  const ratio = a.width > 0 && a.height > 0 ? a.width / a.height : 16 / 9;

  return (
    <>
      <DialogHeader>
        <DialogTitle className="break-all">{name}</DialogTitle>
        <DialogDescription className="break-all">
          {ref ? (
            <>
              <span className="font-mono text-[0.8125rem]">{ref.path}</span> in {source?.display ?? ref.sourceId}
            </>
          ) : isArtStore(a) ? (
            "Samsung Art Store"
          ) : (
            "Uploaded to the TV outside vigo"
          )}
        </DialogDescription>
      </DialogHeader>

      <div className="relative">
        <ArtImage
          src={art.thumbnailUrl(a.id, provenance(a) !== "managed" && a.updatedAt ? String(a.updatedAt.seconds) : undefined)}
          alt={name}
          className="aspect-video w-full rounded-xl bg-neutral-900"
          imgClassName={Math.abs(ratio / (16 / 9) - 1) > 0.01 ? "object-contain" : undefined}
        />
        <span className="absolute bottom-2 left-2">
          <SyncBadge artwork={a} />
        </span>
      </div>

      <ProvenanceNote artwork={a} sources={sources} />

      {guess && !selected && (
        <p className="rounded-xl bg-muted/60 px-3.5 py-2.5 text-sm text-muted-foreground">
          <span className="font-medium text-foreground">Best guess.</span> The TV reports this artwork as current, but that report can be out of
          date. Select it on the TV to make sure.
        </p>
      )}

      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
        <dt className="text-muted-foreground">Sync</dt>
        <dd>
          {syncText[a.syncState]}
          {a.syncState === SyncState.ERROR && a.syncError && <span className="mt-0.5 block text-destructive">{a.syncError}</span>}
        </dd>
        {a.width > 0 && (
          <>
            <dt className="text-muted-foreground">Size</dt>
            <dd className="tabular-nums">
              {a.width} × {a.height}
              {a.sizeBytes > 0n && <span className="text-muted-foreground"> · {megabytes(a.sizeBytes)}</span>}
            </dd>
          </>
        )}
        {a.tvContentId && (
          <>
            <dt className="text-muted-foreground">On the TV</dt>
            <dd className="font-mono text-[0.8125rem]">{a.tvContentId}</dd>
          </>
        )}
      </dl>

      {issues.length > 0 && (
        <ul className="grid gap-2">
          {issues.map((t) => (
            <li key={t.name} className="flex items-center justify-between gap-3 rounded-xl bg-warning-surface px-3.5 py-2.5 text-sm text-warning-foreground">
              <span>{issueText[t.name]}</span>
              {t.name === "issue/aspect-ratio" && canCrop(a, sources) && (
                <Button variant="outline" className="bg-card" onClick={() => onCrop(a.id)}>
                  <Crop /> Crop
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}

      {provenance(a) !== "managed" && !deleting && <RefreshThumbnailButton artwork={a} />}

      <TagEditor artwork={a} sources={sources} disabled={deleting} />

      <div className="flex flex-col-reverse gap-2 border-t border-border/70 pt-5 sm:flex-row sm:justify-between">
        {!isArtStore(a) && !deleting ? <DeleteButton artwork={a} sources={sources} onDeleted={onDeleted} /> : <span />}
        <Button onClick={() => onSelect(a.tvContentId)} disabled={!a.tvContentId || deleting || selected || busy}>
          {pending ? <LoaderCircle className="animate-spin" /> : <Sparkles />}
          {selected ? "Selected on the TV" : pending ? "Selecting…" : a.tvContentId ? "Select on the TV" : "Not on the TV yet"}
        </Button>
      </div>
    </>
  );
}

function RefreshThumbnailButton({ artwork: a }: { artwork: LibraryArtwork }) {
  const refresh = useRefreshThumbnails();
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl bg-muted/60 px-3.5 py-2.5 text-sm">
      <p className="min-w-0 flex-1 basis-48 text-muted-foreground">
        vigo keeps the TV's thumbnail and doesn't download it again unless you ask.
      </p>
      <Button
        variant="outline"
        className="bg-card"
        disabled={refresh.isPending}
        title="vigo keeps the TV's thumbnail and doesn't download it again unless you ask."
        onClick={() => refresh.mutate([a.id], { onSuccess: (results) => batchToast("Refreshed", "thumbnail", results, () => artworkName(a)) })}
      >
        {refresh.isPending ? <LoaderCircle className="animate-spin" /> : <ImageDown />}
        Refresh thumbnail
      </Button>
    </div>
  );
}

function ProvenanceNote({ artwork: a, sources }: { artwork: LibraryArtwork; sources: Source[] }) {
  const p = provenance(a);
  const display = (id: string) => sources.find((s) => s.id === id)?.display ?? id;
  const where = [...new Set(a.sources.map((r) => display(r.sourceId)))].join(", ");
  const [Icon, title, body] =
    p === "managed"
      ? [Folder, `Managed by vigo, from ${where}`, "Changes sync both ways: editing or deleting it here changes the file, and the TV follows."]
      : p === "art-store"
        ? [Store, "Samsung Art Store, not managed by vigo", "It can't be deleted or edited here. You can still select it on the TV."]
        : [
            Smartphone,
            "Uploaded to the TV elsewhere, not managed by vigo",
            "It has no source file, so it can't be edited here. Deleting removes it from the TV.",
          ];
  return (
    <div
      data-provenance={p}
      className={cn(
        "flex gap-3 rounded-xl px-3.5 py-3 text-sm",
        p === "managed" ? "bg-[#e9e4da]/60 dark:bg-[#3a3631]/60" : "bg-muted ring-1 ring-border",
      )}
    >
      <Icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <div>
        <p className="font-medium">{title}</p>
        <p className="mt-0.5 text-muted-foreground">{body}</p>
      </div>
    </div>
  );
}

function TagEditor({ artwork: a, sources, disabled }: { artwork: LibraryArtwork; sources: Source[]; disabled: boolean }) {
  const update = useUpdateTags();
  const [draft, setDraft] = useState("");
  const inputId = useId();
  const system = a.tags.filter((t) => t.origin !== TagOrigin.USER);
  const userTags = a.tags.filter((t) => t.origin === TagOrigin.USER);
  const value = draft.trim();
  const invalid = value.includes("/");

  const add = (e: FormEvent) => {
    e.preventDefault();
    if (!value || invalid || userTags.some((t) => t.name === value)) return;
    update.mutate({ artworkId: a.id, add: [value] }, { onSuccess: () => setDraft("") });
  };

  return (
    <section aria-label="Tags" className="grid gap-3">
      <h3 className="text-xs font-medium tracking-[0.14em] text-muted-foreground uppercase">Tags</h3>
      <ul className="flex flex-wrap gap-1.5" aria-label="Your tags">
        {userTags.map((t) => (
          <li key={t.name}>
            <span className="inline-flex h-8 items-center gap-1 rounded-full bg-accent pr-1 pl-3 text-sm">
              {t.name}
              <button
                type="button"
                aria-label={`Remove tag ${t.name}`}
                disabled={disabled || update.isPending}
                onClick={() => update.mutate({ artworkId: a.id, remove: [t.name] })}
                className="relative flex size-6 items-center justify-center rounded-full outline-none before:absolute before:-inset-2.5 hover:bg-black/10 focus-visible:ring-2 focus-visible:ring-ring dark:hover:bg-white/10"
              >
                <X className="size-3.5" />
              </button>
            </span>
          </li>
        ))}
      </ul>
      <form onSubmit={add} className="flex gap-2">
        <label htmlFor={inputId} className="sr-only">
          New tag
        </label>
        <Input
          id={inputId}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Add a tag"
          aria-invalid={invalid || undefined}
          disabled={disabled}
          autoCapitalize="off"
          enterKeyHint="done"
        />
        <Button type="submit" variant="outline" size="icon" aria-label="Add tag" disabled={disabled || !value || invalid || update.isPending}>
          <Plus />
        </Button>
      </form>
      {invalid && <p className="text-sm text-destructive">Tags can't contain “/”; that's reserved for vigo's own tags.</p>}
      {system.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label="System tags">
          {system.map((t) => (
            <li key={t.name} title={t.name} className="inline-flex h-7 items-center rounded-full border border-border px-2.5 text-xs text-muted-foreground">
              {tagLabel(t.name, sources)}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function DeleteButton({ artwork: a, sources, onDeleted }: { artwork: LibraryArtwork; sources: Source[]; onDeleted: () => void }) {
  const del = useDeleteArtwork();
  const name = artworkName(a);
  const refs = a.sources;
  const display = (id: string) => sources.find((s) => s.id === id)?.display ?? id;
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="destructive-outline">
          <Trash2 /> Delete
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{refs.length ? `Delete ${name}?` : `Delete ${name} from the TV?`}</AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="grid gap-2">
              {refs.length > 1 ? (
                <>
                  <p>The same image is stored in {refs.length} places. All of these files will be deleted, and it will be removed from the TV:</p>
                  <ul className="grid gap-1 rounded-xl bg-muted/60 px-3.5 py-2.5" aria-label="Files to delete">
                    {refs.map((r) => (
                      <li key={`${r.sourceId}/${r.path}`} className="font-mono text-[0.8125rem] break-all text-foreground">
                        {display(r.sourceId)}/{r.path}
                      </li>
                    ))}
                  </ul>
                  <p>This can't be undone.</p>
                </>
              ) : refs.length === 1 ? (
                <p>
                  This deletes the file <span className="font-mono text-[0.8125rem] break-all">{refs[0].path}</span> from {display(refs[0].sourceId)} and
                  removes it from the TV. It can't be undone.
                </p>
              ) : (
                <p>It was uploaded to the TV outside vigo and has no source file, so this removes it from the TV. It can't be undone.</p>
              )}
            </div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep</AlertDialogCancel>
          <AlertDialogAction
            onClick={() =>
              del.mutate(a.id, {
                onSuccess: () => {
                  toast.success(`Deleting ${name}`, { description: "It stays greyed out until the TV confirms." });
                  onDeleted();
                },
              })
            }
          >
            {refs.length > 1 ? `Delete all ${refs.length}` : "Delete"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
