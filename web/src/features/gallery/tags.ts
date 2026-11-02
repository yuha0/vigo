import { ContentType } from "@/gen/vigo/core/v1/core_pb";
import { SyncState, type LibraryArtwork, type Source } from "@/gen/vigo/core/v1/library_pb";

export type TagGroupId = "source" | "folder" | "status" | "issue" | "user" | "other" | "tv-category";

export const tagGroups: { id: TagGroupId; title: string; advanced?: boolean }[] = [
  { id: "source", title: "Source" },
  { id: "folder", title: "Folder" },
  { id: "status", title: "Status" },
  { id: "issue", title: "Issues" },
  { id: "user", title: "Your tags" },
  { id: "other", title: "Other" },
  // The TV's category ids are opaque (it reports no names), so they stay out of the way.
  { id: "tv-category", title: "TV category", advanced: true },
];

export const LOW_RES = "issue/low-resolution";
export const ASPECT = "issue/aspect-ratio";

export const issueText: Record<string, string> = {
  [LOW_RES]: "Low resolution: the TV will upscale",
  [ASPECT]: "Not 16:9",
};

export function tagGroup(name: string): TagGroupId {
  const slash = name.indexOf("/");
  if (slash < 0) return "user";
  const ns = name.slice(0, slash);
  return ns === "source" || ns === "folder" || ns === "status" || ns === "issue" || ns === "tv-category" ? ns : "other";
}

export function tagLabel(name: string, sources: readonly Source[] = []): string {
  const slash = name.indexOf("/");
  if (slash < 0) return name;
  const ns = name.slice(0, slash);
  const value = name.slice(slash + 1);
  switch (ns) {
    case "source":
      if (value === "art-store") return "Art Store";
      if (value === "upload") return "Uploaded elsewhere";
      return sources.find((s) => s.id === value)?.display || value;
    case "status":
      return value === "on-tv" ? "On the TV" : value.replace(/-/g, " ").replace(/^./, (c) => c.toUpperCase());
    case "issue":
      return value === "low-resolution" ? "Low resolution" : value === "aspect-ratio" ? "Not 16:9" : value;
    default:
      return value;
  }
}

export function hasTag(a: LibraryArtwork, name: string): boolean {
  return a.tags.some((t) => t.name === name);
}

export function isArtStore(a: LibraryArtwork): boolean {
  return a.contentType === ContentType.SERVER || a.contentType === ContentType.PREINSTALL || hasTag(a, "source/art-store");
}

export type Provenance = "managed" | "art-store" | "elsewhere";

/** Managed artworks come from a vigo source; the rest only exist on the TV and vigo doesn't manage them. */
export function provenance(a: LibraryArtwork): Provenance {
  if (a.sources.length > 0) return "managed";
  return isArtStore(a) ? "art-store" : "elsewhere";
}

export function isDeleting(a: LibraryArtwork): boolean {
  return a.syncState === SyncState.PENDING_DELETE || a.syncState === SyncState.DELETING;
}

export function artworkName(a: LibraryArtwork): string {
  const path = a.sources[0]?.path;
  if (path) return path.slice(path.lastIndexOf("/") + 1);
  return a.tvContentId || a.id.replace(/^sha256:(.{12}).*/, "$1");
}

/** Whether the crop editor applies: wrong aspect ratio and an editable first source. */
export function canCrop(a: LibraryArtwork, sources: readonly Source[]): boolean {
  const first = a.sources[0];
  return hasTag(a, ASPECT) && !!first && !!sources.find((s) => s.id === first.sourceId)?.editable && !isDeleting(a);
}

/** Art Store art can't be deleted; managed artworks and uploads made elsewhere can. */
export function canDelete(a: LibraryArtwork): boolean {
  return !isArtStore(a) && !isDeleting(a);
}

/** Only TV-only artworks have a TV thumbnail to download again. */
export function canRefresh(a: LibraryArtwork): boolean {
  return provenance(a) !== "managed" && !isDeleting(a);
}
