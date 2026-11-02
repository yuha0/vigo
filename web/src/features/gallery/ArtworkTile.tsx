import { Check, CircleAlert, Clock, Crop, Ellipsis, Folder, LoaderCircle, Smartphone, Store, Trash2, UploadCloud, ZoomIn } from "lucide-react";
import { memo, type ReactNode } from "react";

import { ArtImage } from "@/components/ArtImage";
import { SyncState, type LibraryArtwork } from "@/gen/vigo/core/v1/library_pb";
import { ASPECT, artworkName, hasTag, isDeleting, issueText, LOW_RES, provenance } from "@/features/gallery/tags";
import { useArtApi } from "@/lib/backend";
import { cn } from "@/lib/utils";

function Pill({ tone = "dark", children, title }: { tone?: "dark" | "error"; children: ReactNode; title?: string }) {
  return (
    <span
      title={title}
      className={cn(
        "inline-flex max-w-full items-center gap-1 overflow-hidden rounded-full px-2 py-1 text-[0.6875rem] font-medium whitespace-nowrap text-white backdrop-blur-md [&_svg]:size-3 [&_svg]:shrink-0 [&>span]:truncate",
        tone === "error" ? "bg-destructive/85" : "bg-black/50",
      )}
    >
      {children}
    </span>
  );
}

export function SyncBadge({ artwork: a }: { artwork: LibraryArtwork }) {
  switch (a.syncState) {
    case SyncState.PENDING_UPLOAD:
      return (
        <Pill>
          <Clock /> <span>Waiting to upload</span>
        </Pill>
      );
    case SyncState.UPLOADING:
      return (
        <Pill>
          <LoaderCircle className="animate-spin" /> <span>Uploading</span>
        </Pill>
      );
    case SyncState.ERROR:
      return (
        <Pill tone="error" title={a.syncError}>
          <CircleAlert /> <span>Sync error</span>
        </Pill>
      );
    case SyncState.PENDING_DELETE:
      return (
        <Pill>
          <Trash2 /> <span>Waiting to delete</span>
        </Pill>
      );
    case SyncState.DELETING:
      return (
        <Pill>
          <LoaderCircle className="animate-spin" /> <span>Deleting</span>
        </Pill>
      );
    default:
      return null;
  }
}

const unmanagedBadge = {
  "art-store": { label: "Art Store", icon: Store },
  elsewhere: { label: "Elsewhere", icon: Smartphone },
} as const;

/** A light corner badge on art vigo doesn't manage, readable on any artwork and in either theme. */
export function ProvenanceBadge({ artwork }: { artwork: LibraryArtwork }) {
  const p = provenance(artwork);
  if (p === "managed") return null;
  const { label, icon: Icon } = unmanagedBadge[p];
  return (
    <span
      data-provenance={p}
      title={`${label}: not managed by vigo`}
      className="inline-flex items-center gap-1 rounded-full bg-white/90 p-1.5 text-[0.6875rem] font-semibold text-neutral-800 shadow-sm ring-1 ring-black/10 backdrop-blur-md sm:px-2 sm:py-1"
    >
      <Icon className="size-3" aria-hidden="true" />
      {/* Narrow phone tiles get the icon only; the section heading and caption carry the words. */}
      <span className="sr-only sm:not-sr-only">{label}</span>
    </span>
  );
}

function caption(a: LibraryArtwork): string {
  const path = a.sources[0]?.path ?? "";
  return path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "Top folder";
}

export const ArtworkTile = memo(function ArtworkTile({
  artwork: a,
  selected,
  pending,
  busy,
  onSelect,
  onOpen,
}: {
  artwork: LibraryArtwork;
  selected: boolean;
  pending: boolean;
  /** Another selection is in flight; taps are ignored until it settles. */
  busy: boolean;
  onSelect: (tvContentId: string) => void;
  onOpen: (id: string) => void;
}) {
  const art = useArtApi();
  const name = artworkName(a);
  const deleting = isDeleting(a);
  const onTv = !!a.tvContentId;
  const ratio = a.width > 0 && a.height > 0 ? a.width / a.height : 16 / 9;
  const contain = Math.abs(ratio / (16 / 9) - 1) > 0.01;
  const issues = [LOW_RES, ASPECT].filter((t) => hasTag(a, t));
  const selectable = onTv && !deleting && !busy;
  const managed = provenance(a) === "managed";

  return (
    <li
      className={cn("group relative transition-[opacity,filter] duration-300", deleting && "pointer-events-none opacity-45 grayscale")}
      data-artwork-id={a.id}
      data-sync-state={SyncState[a.syncState]}
      data-provenance={provenance(a)}
      aria-disabled={deleting || undefined}
    >
      <button
        type="button"
        disabled={deleting}
        aria-pressed={selected}
        aria-busy={pending || undefined}
        aria-disabled={!selectable && !pending ? true : undefined}
        aria-label={`${name}${selected ? ", selected" : pending ? ", selecting" : deleting ? ", being deleted" : !onTv ? ", not on the TV yet" : ""}`}
        title={!onTv && !deleting ? "Not on the TV yet" : undefined}
        onClick={() => selectable && onSelect(a.tvContentId)}
        className={cn(
          "relative block w-full overflow-hidden text-left outline-none transition-[box-shadow,transform] duration-300 ease-[var(--ease-gallery)] focus-visible:ring-[3px] focus-visible:ring-ring",
          // Art from the user's own folders sits in a mat, like the frame on the TV; TV-only art doesn't.
          managed
            ? "rounded-[0.625rem] bg-[#e9e4da] p-[5px] shadow-[0_1px_2px_rgb(0_0_0/0.08),0_8px_20px_-12px_rgb(0_0_0/0.35)] dark:bg-[#3a3631]"
            : "rounded-xl",
          selectable && "active:scale-[0.98]",
          (!onTv || busy) && !pending && "cursor-default",
          selected ? "ring-2 ring-brass ring-offset-2 ring-offset-background" : "ring-1 ring-black/5 hover:shadow-lg dark:ring-white/5",
        )}
      >
        <ArtImage
          src={art.thumbnailUrl(a.id)}
          alt=""
          className={cn("aspect-video w-full", managed && "rounded-[3px]", contain && "bg-neutral-900")}
          imgClassName={cn(
            "transition-transform duration-700 ease-[var(--ease-gallery)] group-hover:scale-[1.03]",
            contain && "object-contain",
          )}
        />
        {pending && (
          <span className="absolute inset-0 flex items-center justify-center gap-2 bg-black/35 text-sm font-medium text-white backdrop-blur-[1px] animate-in fade-in">
            <LoaderCircle className="size-5 animate-spin" />
            Selecting…
          </span>
        )}
        <span className="absolute top-2 right-2">
          <ProvenanceBadge artwork={a} />
        </span>
        {selected && (
          <span className="absolute top-2 left-2 inline-flex items-center gap-1 rounded-full bg-black/45 px-2 py-1 text-[0.6875rem] font-medium tracking-wide text-white backdrop-blur-md">
            <Check className="size-3" /> Selected
          </span>
        )}
        <span className="absolute inset-x-2 bottom-2 flex items-end justify-between gap-2">
          <span className="min-w-0">
            <SyncBadge artwork={a} />
          </span>
          {issues.length > 0 && (
            <span className="flex shrink-0 gap-1">
              {issues.map((t) => (
                <span
                  key={t}
                  role="img"
                  aria-label={issueText[t]}
                  title={issueText[t]}
                  className="flex size-6 items-center justify-center rounded-full bg-amber-400/90 text-amber-950 [&_svg]:size-3.5"
                >
                  {t === LOW_RES ? <ZoomIn /> : <Crop />}
                </span>
              ))}
            </span>
          )}
        </span>
      </button>

      <div className="mt-1.5 flex min-h-9 items-start justify-between gap-1 pl-0.5">
        <div className="min-w-0">
          <p className="truncate text-[0.8125rem] font-medium">{name}</p>
          <p className="flex min-w-0 items-center gap-1 text-[0.6875rem] text-muted-foreground">
            {managed && <Folder className="size-3 shrink-0" aria-hidden="true" />}
            {managed ? (
              <span className="truncate">{caption(a)}</span>
            ) : (
              <span className="truncate">
                Not managed<span className="hidden sm:inline"> by vigo</span>
              </span>
            )}
            {managed && a.syncState === SyncState.IN_SYNC && (
              <UploadCloud className="size-3 shrink-0 opacity-60" aria-label="Synced to the TV" />
            )}
          </p>
        </div>
        <button
          type="button"
          onClick={() => onOpen(a.id)}
          disabled={deleting}
          aria-label={`Details for ${name}`}
          className="-mt-1.5 -mr-1.5 flex size-11 shrink-0 items-center justify-center rounded-full text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          <Ellipsis className="size-[1.125rem]" />
        </button>
      </div>
    </li>
  );
});
