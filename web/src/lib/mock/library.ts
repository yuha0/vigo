import { clone, create } from "@bufbuild/protobuf";
import { timestampFromMs, timestampNow } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { ContentType, LibraryChangedSchema } from "@/gen/vigo/core/v1/core_pb";
import {
  LibraryArtworkSchema,
  ListArtworksRequestSchema,
  SourceSchema,
  SyncState,
  TagOrigin,
  TagSchema,
  type LibraryArtwork,
  type ListArtworksRequest,
  type Rect,
  type Source,
  type Tag,
} from "@/gen/vigo/core/v1/library_pb";
import type { MockTv } from "@/lib/mock/tv";

export type MockSources = "default" | "single" | "read-only";

export type MockLibraryOptions = {
  sources: MockSources;
  transitionMs: number;
};

function fail(code: Code, message: string): never {
  throw new ConnectError(message, code);
}

function hex(seed: string): string {
  let h1 = 0x811c9dc5;
  let h2 = 0x01000193;
  for (let i = 0; i < seed.length; i++) {
    h1 = Math.imul(h1 ^ seed.charCodeAt(i), 16777619) >>> 0;
    h2 = Math.imul(h2 ^ seed.charCodeAt(i), 2246822519) >>> 0;
  }
  return (h1.toString(16).padStart(8, "0") + h2.toString(16).padStart(8, "0")).repeat(4);
}

const sys = (name: string) => create(TagSchema, { name, origin: TagOrigin.SYSTEM });
const user = (name: string) => create(TagSchema, { name, origin: TagOrigin.USER });

function issueTags(width: number, height: number): Tag[] {
  const tags: Tag[] = [];
  const longSide = Math.max(width, height);
  const shortSide = Math.min(width, height);
  if (longSide < 3840 || shortSide < 2160) tags.push(sys("issue/low-resolution"));
  if (Math.abs(width / height / (16 / 9) - 1) > 0.01) tags.push(sys("issue/aspect-ratio"));
  return tags;
}

type Seed = {
  path: string;
  width: number;
  height: number;
  state?: SyncState;
  error?: string;
  userTags?: string[];
  /** The same bytes elsewhere: identical content is one artwork with several refs. */
  copies?: { sourceId: string; path: string }[];
};

const photoSeeds: Seed[] = [
  { path: "2024/iceland/vik-beach.jpg", width: 6000, height: 3375, userTags: ["favorites"] },
  { path: "2024/iceland/skogafoss.jpg", width: 4032, height: 3024, userTags: ["winter"] },
  { path: "2024/iceland/aurora.jpg", width: 5472, height: 3078, userTags: ["favorites", "winter"] },
  { path: "2024/lisbon/alfama.jpg", width: 3024, height: 4032 },
  { path: "2024/lisbon/tram-28.jpg", width: 3840, height: 2160 },
  { path: "2023/kyoto/fushimi-inari.jpg", width: 2048, height: 1365 },
  { path: "2023/kyoto/arashiyama.jpg", width: 3840, height: 2160, state: SyncState.PENDING_UPLOAD },
  { path: "2023/kyoto/kinkaku-ji.jpg", width: 5184, height: 2916, state: SyncState.UPLOADING },
  { path: "scans/grandmother-1962.png", width: 2400, height: 1800, state: SyncState.ERROR, error: "the TV didn't answer the upload in time" },
  { path: "scans/wedding-1988.jpg", width: 3840, height: 2160, state: SyncState.PENDING_DELETE },
  { path: "scans/harbour.jpg", width: 3840, height: 2160, state: SyncState.DELETING },
  {
    path: "paintings/water-lilies.jpg",
    width: 4000,
    height: 2250,
    userTags: ["favorites"],
    copies: [{ sourceId: "family", path: "art/water-lilies.jpg" }],
  },
];

const familySeeds: Seed[] = [
  { path: "kids/first-day.jpg", width: 4032, height: 2268 },
  { path: "kids/beach-day.jpg", width: 4032, height: 2268 },
  { path: "garden.jpg", width: 3840, height: 2160 },
];

/** A realistic stand-in for vigo-core's library: sources, TV mirror, tags and sync states. */
export class MockLibrary {
  artworks: LibraryArtwork[] = [];
  sources: Source[] = [];
  /** Every ListArtworks request, for tests. */
  readonly requests: ListArtworksRequest[] = [];
  private timers: ReturnType<typeof setTimeout>[] = [];

  constructor(
    private tv: MockTv,
    private opts: MockLibraryOptions,
  ) {
    const writable = opts.sources !== "read-only";
    this.sources = [
      create(SourceSchema, {
        id: "photos",
        kind: "file",
        display: "/srv/photos",
        writable,
        editable: writable,
        lastSyncedAt: timestampFromMs(Date.now() - 4 * 60_000),
      }),
    ];
    if (opts.sources !== "single") {
      this.sources.push(
        create(SourceSchema, {
          id: "family",
          kind: "file",
          display: "/mnt/nas/family",
          writable,
          editable: false,
          lastSyncedAt: timestampFromMs(Date.now() - 52 * 60_000),
        }),
      );
    }
    for (const seed of photoSeeds) this.artworks.push(this.fromSeed("photos", seed));
    if (opts.sources !== "single") for (const seed of familySeeds) this.artworks.push(this.fromSeed("family", seed));
    for (const t of tv.artworks) {
      const store = t.contentType === ContentType.SERVER || t.contentType === ContentType.PREINSTALL;
      this.artworks.push(
        create(LibraryArtworkSchema, {
          id: `tv:${t.contentId}`,
          tvContentId: t.contentId,
          contentType: t.contentType,
          width: store ? t.width : t.height,
          height: store ? t.height : t.width,
          tags: [
            sys(store ? "source/art-store" : "source/upload"),
            ...t.categoryIds.map((c) => sys(`tv-category/${c}`)),
            sys("status/on-tv"),
          ],
          syncState: SyncState.TV_ONLY,
          updatedAt: timestampFromMs(Date.now() - 30 * 86_400_000),
        }),
      );
    }
    this.recount();
  }

  private fromSeed(sourceId: string, seed: Seed): LibraryArtwork {
    const state = seed.state ?? SyncState.IN_SYNC;
    const onTv = state === SyncState.IN_SYNC || state === SyncState.PENDING_DELETE || state === SyncState.DELETING;
    const tvContentId = onTv ? this.tv.addUpload(seed.width, seed.height) : "";
    return this.make(sourceId, seed.path, seed.width, seed.height, {
      state,
      tvContentId,
      error: seed.error,
      userTags: seed.userTags,
      size: Math.round(seed.width * seed.height * 0.35),
      copies: this.opts.sources === "single" ? [] : seed.copies,
    });
  }

  private make(
    sourceId: string,
    path: string,
    width: number,
    height: number,
    o: {
      state: SyncState;
      tvContentId?: string;
      error?: string;
      userTags?: string[];
      size: number;
      seed?: string;
      copies?: { sourceId: string; path: string }[];
    },
  ): LibraryArtwork {
    const refs = [{ sourceId, path }, ...(o.copies ?? [])];
    const dirOf = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");
    return create(LibraryArtworkSchema, {
      id: `sha256:${hex(o.seed ?? `${sourceId}/${path}`)}`,
      tvContentId: o.tvContentId ?? "",
      contentType: o.tvContentId ? ContentType.MOBILE : ContentType.UNSPECIFIED,
      width,
      height,
      sizeBytes: BigInt(o.size),
      sources: refs,
      tags: [
        ...[...new Set(refs.map((r) => `source/${r.sourceId}`))].map(sys),
        ...[...new Set(refs.map((r) => dirOf(r.path)).filter(Boolean))].map((d) => sys(`folder/${d}`)),
        ...(o.tvContentId ? [sys("status/on-tv")] : []),
        ...issueTags(width, height),
        ...(o.userTags ?? []).map(user),
      ],
      syncState: o.state,
      syncError: o.error ?? "",
      updatedAt: timestampNow(),
    });
  }

  private recount() {
    for (const s of this.sources) {
      s.artworkCount = this.artworks.filter((a) => a.sources.some((r) => r.sourceId === s.id)).length;
    }
  }

  private changed(artworkIds: string[], sourceIds: string[] = []) {
    this.recount();
    this.tv.emit({ case: "libraryChanged", value: create(LibraryChangedSchema, { artworkIds, sourceIds }) });
  }

  private later(fn: () => void, steps = 1) {
    this.timers.push(setTimeout(fn, Math.max(this.opts.transitionMs, 1) * steps));
  }

  private find(id: string): LibraryArtwork {
    const a = this.artworks.find((x) => x.id === id);
    if (!a) fail(Code.NotFound, `no artwork ${id}`);
    return a;
  }

  // ---- reads -------------------------------------------------------------------

  listArtworks(req: ListArtworksRequest) {
    this.requests.push(clone(ListArtworksRequestSchema, req));
    const q = req.query.trim().toLowerCase();
    const matches = this.artworks.filter((a) => {
      if (!req.includeDeleting && (a.syncState === SyncState.PENDING_DELETE || a.syncState === SyncState.DELETING)) return false;
      const names = a.tags.map((t) => t.name);
      for (const want of req.tags) {
        if (want.endsWith("/") ? !names.some((n) => n.startsWith(want)) : !names.includes(want)) return false;
      }
      if (q) {
        const hay = [...a.sources.map((s) => s.path), ...names, a.tvContentId].join("\n").toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
    const size = req.pageSize > 0 ? req.pageSize : 100;
    const start = Number(req.pageToken || 0);
    const page = matches.slice(start, start + size);
    const next = start + size < matches.length ? String(start + size) : "";
    return { artworks: page.map((a) => clone(LibraryArtworkSchema, a)), nextPageToken: next, total: matches.length };
  }

  getArtwork(id: string) {
    return clone(LibraryArtworkSchema, this.find(id));
  }

  listTags(prefix: string) {
    const counts = new Map<string, { tag: Tag; count: number }>();
    for (const a of this.artworks) {
      for (const t of a.tags) {
        if (prefix && !t.name.startsWith(prefix)) continue;
        const e = counts.get(t.name) ?? { tag: clone(TagSchema, t), count: 0 };
        e.count++;
        counts.set(t.name, e);
      }
    }
    return [...counts.values()].sort((x, y) => x.tag.name.localeCompare(y.tag.name));
  }

  listSources() {
    return this.sources.map((s) => clone(SourceSchema, s));
  }

  // ---- writes ------------------------------------------------------------------

  updateTags(id: string, add: string[], remove: string[]) {
    const a = this.find(id);
    for (const name of [...add, ...remove]) {
      if (!name.trim()) fail(Code.InvalidArgument, "tags can't be empty");
      if (name.includes("/")) fail(Code.InvalidArgument, `"${name}" is a system tag and can't be changed`);
    }
    const existing = new Set(a.tags.map((t) => t.name));
    a.tags = a.tags.filter((t) => !remove.includes(t.name));
    for (const name of add) if (!existing.has(name)) a.tags.push(user(name));
    a.updatedAt = timestampNow();
    this.changed([id]);
    return clone(LibraryArtworkSchema, a);
  }

  deleteArtwork(id: string) {
    const a = this.find(id);
    if (a.contentType === ContentType.SERVER || a.contentType === ContentType.PREINSTALL) {
      fail(Code.InvalidArgument, "Art Store artworks can't be deleted");
    }
    if (a.syncState === SyncState.PENDING_DELETE || a.syncState === SyncState.DELETING) return clone(LibraryArtworkSchema, a);
    a.syncState = a.sources.length ? SyncState.PENDING_DELETE : SyncState.DELETING;
    this.changed([id]);
    this.later(() => {
      a.syncState = SyncState.DELETING;
      this.changed([id]);
      this.later(() => {
        if (a.tvContentId) this.tv.removeContent(a.tvContentId);
        this.artworks = this.artworks.filter((x) => x !== a);
        this.changed([id]);
      });
    });
    return clone(LibraryArtworkSchema, a);
  }

  cropArtwork(id: string, rect: Rect | undefined) {
    const a = this.find(id);
    const ref = a.sources[0];
    const source = ref && this.sources.find((s) => s.id === ref.sourceId);
    if (!source?.editable) fail(Code.FailedPrecondition, "this artwork's source doesn't support editing");
    if (!rect || rect.width <= 0 || rect.height <= 0) fail(Code.InvalidArgument, "the crop rectangle is empty");
    if (rect.x < 0 || rect.y < 0 || rect.x + rect.width > a.width || rect.y + rect.height > a.height) {
      fail(Code.InvalidArgument, "the crop rectangle is outside the image");
    }
    const userTags = a.tags.filter((t) => t.origin === TagOrigin.USER).map((t) => t.name);
    const next = this.make(ref.sourceId, ref.path, rect.width, rect.height, {
      state: SyncState.PENDING_UPLOAD,
      userTags,
      size: Math.round(rect.width * rect.height * 0.35),
      seed: `${id}@${rect.x},${rect.y},${rect.width},${rect.height}`,
    });
    const oldTv = a.tvContentId;
    this.artworks = this.artworks.map((x) => (x === a ? next : x));
    this.changed([id, next.id]);
    this.startUpload(next, () => oldTv && this.tv.removeContent(oldTv));
    return clone(LibraryArtworkSchema, next);
  }

  private startUpload(a: LibraryArtwork, onDone?: () => void) {
    this.later(() => {
      a.syncState = SyncState.UPLOADING;
      this.changed([a.id]);
      this.later(() => {
        a.tvContentId = this.tv.addUpload(a.width, a.height);
        a.contentType = ContentType.MOBILE;
        a.tags.push(sys("status/on-tv"));
        a.syncState = SyncState.IN_SYNC;
        onDone?.();
        this.changed([a.id]);
      }, 2);
    });
  }

  syncSource(id: string) {
    const s = this.sources.find((x) => x.id === id);
    if (!s) fail(Code.NotFound, `no source ${id}`);
    s.syncing = true;
    this.changed([], [id]);
    this.later(() => {
      s.syncing = false;
      s.lastSyncedAt = timestampNow();
      s.lastError = "";
      this.changed([], [id]);
    }, 3);
  }

  /** POST /sources/{id}/files */
  addFile(sourceId: string, file: File, path: string, width: number, height: number) {
    const s = this.sources.find((x) => x.id === sourceId);
    if (!s) fail(Code.NotFound, `no source ${sourceId}`);
    if (!s.writable) fail(Code.FailedPrecondition, "this source is read-only");
    if (!/^image\/(jpeg|png)$/.test(file.type)) fail(Code.InvalidArgument, "only JPEG and PNG files can be added");
    const clean = path.replace(/^\/+|\/+$/g, "");
    if (clean.split("/").includes("..")) fail(Code.InvalidArgument, "the folder must stay inside the source");
    const rel = clean ? `${clean}/${file.name}` : file.name;
    const a = this.make(sourceId, rel, width, height, {
      state: SyncState.PENDING_UPLOAD,
      size: file.size,
      seed: `${sourceId}/${rel}/${file.size}/${file.lastModified}`,
    });
    this.artworks.unshift(a);
    this.changed([a.id], [sourceId]);
    this.startUpload(a);
    return clone(LibraryArtworkSchema, a);
  }

  dispose() {
    this.timers.forEach(clearTimeout);
  }
}
