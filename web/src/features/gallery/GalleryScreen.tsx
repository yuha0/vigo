import { Folder, ImagePlus, ListChecks, LoaderCircle, RefreshCw, Search, SlidersHorizontal, Tv, X } from "lucide-react";
import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { Page, PageHeader, Section } from "@/components/Page";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ArtworkSheet } from "@/features/gallery/ArtworkSheet";
import { ArtworkTile } from "@/features/gallery/ArtworkTile";
import { CropEditor } from "@/features/gallery/CropEditor";
import { FilterSheet } from "@/features/gallery/FilterSheet";
import { useArtworks, useScopeCounts, useSources, useTags } from "@/features/gallery/library-queries";
import { scopeOrigin, useScope } from "@/features/gallery/scope";
import { ScopeTabs } from "@/features/gallery/ScopeTabs";
import { SourcesPanel } from "@/features/gallery/SourcesPanel";
import { SelectionBar } from "@/features/gallery/SelectionBar";
import { isDeleting, provenance, tagLabel } from "@/features/gallery/tags";
import { UploadDialog } from "@/features/gallery/UploadDialog";
import { useSelectArtwork } from "@/features/gallery/useSelectArtwork";
import { describeError } from "@/lib/errors";
import { useStatus } from "@/lib/queries";
import { tvSelection } from "@/lib/tv-state";

const SEARCH_DEBOUNCE_MS = 250;

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

export function GalleryScreen() {
  const { data: status } = useStatus();
  // Only a selection the TV confirmed gets the "Selected" badge; an unconfirmed report is a best guess.
  const sel = tvSelection(status);
  const selectedId = sel?.confirmed ? sel.id : "";
  const guessId = sel && !sel.confirmed ? sel.id : "";
  const { selectArtwork, pendingId } = useSelectArtwork();

  const [tags, setTags] = useState<string[]>([]);
  const [search, setSearch] = useState("");
  const query = useDebounced(search, SEARCH_DEBOUNCE_MS);
  const { scope, toggle: toggleScope } = useScope();
  const artworks = useArtworks({ tags, query, origin: scopeOrigin(scope) });
  const tagCounts = useTags();
  const scopeCounts = useScopeCounts();
  const both = scope.store && scope.local;
  const sourcesQuery = useSources();
  const sources = useMemo(() => sourcesQuery.data ?? [], [sourcesQuery.data]);
  const writable = sources.filter((s) => s.writable);

  const [filtersOpen, setFiltersOpen] = useState(false);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [openId, setOpenId] = useState<string | null>(null);
  const [cropId, setCropId] = useState<string | null>(null);

  const items = useMemo(() => artworks.data?.pages.flatMap((p) => p.artworks) ?? [], [artworks.data]);
  const total = artworks.data?.pages[0]?.total ?? 0;
  const find = (id: string | null) => (id ? (items.find((a) => a.id === id) ?? null) : null);
  const opened = find(openId);
  const cropping = find(cropId);
  const folders = (tagCounts.data ?? []).flatMap((t) => (t.tag?.name.startsWith("folder/") ? [t.tag.name.slice(7)] : []));

  const toggleTag = useCallback((t: string) => setTags((cur) => (cur.includes(t) ? cur.filter((x) => x !== t) : [...cur, t])), []);
  const onOpen = useCallback((id: string) => setOpenId(id), []);

  const sentinel = useRef<HTMLDivElement>(null);
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = artworks;
  useEffect(() => {
    const el = sentinel.current;
    if (!el || !hasNextPage || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting) && !isFetchingNextPage) void fetchNextPage();
    }, { rootMargin: "800px 0px" });
    io.observe(el);
    return () => io.disconnect();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  const filtering = tags.length > 0 || query.trim() !== "";

  // Multi-select. The selection belongs to one view: changing tabs, tags or search starts it over.
  const viewKey = JSON.stringify([scopeOrigin(scope), [...tags].sort(), query.trim()]);
  const [selecting, setSelecting] = useState(false);
  const [selection, setSelection] = useState<{ view: string; ids: string[]; anchor: string | null }>({ view: "", ids: [], anchor: null });
  const checkedIds = useMemo(() => new Set(selection.view === viewKey ? selection.ids : []), [selection, viewKey]);
  const selectable = useMemo(() => items.filter((a) => !isDeleting(a)), [items]);
  const selectedItems = useMemo(() => selectable.filter((a) => checkedIds.has(a.id)), [selectable, checkedIds]);
  const exitSelect = useCallback(() => {
    setSelecting(false);
    setSelection({ view: "", ids: [], anchor: null });
  }, []);
  const toggleChecked = useCallback(
    (id: string, range: boolean) =>
      setSelection((cur) => {
        const ids = new Set(cur.view === viewKey ? cur.ids : []);
        const anchor = cur.view === viewKey ? cur.anchor : null;
        if (range && anchor && anchor !== id) {
          const order = selectable.map((a) => a.id);
          const [from, to] = [order.indexOf(anchor), order.indexOf(id)].sort((x, y) => x - y);
          if (from >= 0) for (const x of order.slice(from, to + 1)) ids.add(x);
        } else if (ids.has(id)) ids.delete(id);
        else ids.add(id);
        return { view: viewKey, ids: [...ids], anchor: id };
      }),
    [viewKey, selectable],
  );
  const startSelect = useCallback(
    (id?: string) => {
      setSelecting(true);
      setSelection({ view: viewKey, ids: id ? [id] : [], anchor: id ?? null });
    },
    [viewKey],
  );
  useEffect(() => {
    if (!selecting) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || document.querySelector('[role="dialog"], [role="alertdialog"], [role="listbox"]')) return;
      exitSelect();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [selecting, exitSelect]);

  // Unfiltered, the grid is split by who manages the art; filtered, it's one list ranked by the server.
  const groups = useMemo(() => {
    if (filtering || !both) return [{ id: "all", title: "", description: "", icon: Folder, items }];
    const managed = items.filter((a) => provenance(a) === "managed");
    const other = items.filter((a) => provenance(a) !== "managed");
    return [
      {
        id: "managed",
        title: "From your folders",
        description: "Managed by vigo and kept in sync with the TV.",
        icon: Folder,
        items: managed,
      },
      {
        id: "unmanaged",
        title: "On the TV only",
        description: "Samsung Art Store and uploads made elsewhere. vigo can show them but doesn't manage them.",
        icon: Tv,
        items: other,
      },
    ].filter((g) => g.items.length > 0);
  }, [filtering, both, items]);

  return (
    <Page className="max-w-6xl">
      <PageHeader
        title="Gallery"
        description={
          artworks.data
            ? `${total} artwork${total === 1 ? "" : "s"}${filtering ? " match" : ""}. Tap one to select it on the TV.`
            : "Your art, on the TV and in your folders."
        }
        actions={
          <div className="flex flex-col items-end gap-1.5">
            <div className="flex gap-2">
              <Button variant={selecting ? "secondary" : "outline"} aria-pressed={selecting} onClick={() => (selecting ? exitSelect() : startSelect())}>
                <ListChecks /> {selecting ? "Done" : "Select"}
              </Button>
              <Button onClick={() => setUploadOpen(true)} disabled={!writable.length}>
                <ImagePlus /> Upload
              </Button>
            </div>
            {sourcesQuery.isSuccess && !writable.length && (
              <p className="max-w-56 text-right text-xs text-muted-foreground">No source accepts uploads. Add a writable folder to vigo-core.</p>
            )}
          </div>
        }
      />

      <ScopeTabs scope={scope} counts={scopeCounts.data} onToggle={toggleScope} />

      <div className="mb-4 flex gap-2">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search names and tags"
            aria-label="Search"
            className="pl-10"
            enterKeyHint="search"
          />
        </div>
        <Button variant="outline" onClick={() => setFiltersOpen(true)} aria-label={`Filters${tags.length ? `, ${tags.length} active` : ""}`}>
          <SlidersHorizontal />
          <span className="hidden sm:inline">Filters</span>
          {tags.length > 0 && (
            <span className="flex size-5 items-center justify-center rounded-full bg-primary text-[0.6875rem] text-primary-foreground tabular-nums">{tags.length}</span>
          )}
        </Button>
      </div>

      {tags.length > 0 && (
        <ul className="mb-6 flex flex-wrap gap-2" aria-label="Active filters">
          {tags.map((t) => (
            <li key={t}>
              <button
                type="button"
                onClick={() => toggleTag(t)}
                aria-label={`Remove filter ${tagLabel(t, sources)}`}
                className="inline-flex h-9 items-center gap-1.5 rounded-full bg-primary pr-2.5 pl-3.5 text-sm font-medium text-primary-foreground outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 [@media(pointer:coarse)]:h-11"
              >
                {tagLabel(t, sources)}
                <X className="size-3.5 opacity-70" />
              </button>
            </li>
          ))}
          <li>
            <Button variant="ghost" size="sm" onClick={() => setTags([])} className="[@media(pointer:coarse)]:h-11">
              Clear
            </Button>
          </li>
        </ul>
      )}

      {artworks.isPending ? (
        <ul className="grid grid-cols-2 gap-x-3 gap-y-6 sm:grid-cols-3 lg:grid-cols-4" aria-label="Loading artworks">
          {Array.from({ length: 12 }, (_, i) => (
            <li key={i}>
              <Skeleton className="aspect-video w-full" />
              <Skeleton className="mt-2 h-3 w-2/3" />
            </li>
          ))}
        </ul>
      ) : artworks.isError ? (
        <Card>
          <CardContent className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <p className="font-medium">{describeError(artworks.error)?.title ?? "Couldn't load the gallery"}</p>
              <p className="mt-1 text-sm text-muted-foreground">{describeError(artworks.error)?.description}</p>
            </div>
            <Button variant="outline" onClick={() => void artworks.refetch()}>
              <RefreshCw /> Retry
            </Button>
          </CardContent>
        </Card>
      ) : items.length === 0 ? (
        <p className="py-16 text-center text-muted-foreground">
          {filtering ? "Nothing matches these filters." : both ? "No artworks yet." : scope.local ? "No artworks from your folders yet." : "Nothing from the Art Store or elsewhere on the TV."}
        </p>
      ) : (
        <>
          <ul className="grid grid-cols-2 gap-x-3 gap-y-4 sm:grid-cols-3 sm:gap-x-4 sm:gap-y-6 lg:grid-cols-4" aria-label="Artworks">
            {groups.map((g) => (
              <Fragment key={g.id}>
                {g.title && (
                  <li role="none" className="col-span-full pt-6 first:pt-0" data-group={g.id}>
                    <h2 className="flex items-center gap-2 font-display text-xl">
                      <g.icon className="size-[1.125rem] text-muted-foreground" strokeWidth={1.75} aria-hidden="true" />
                      {g.title}
                    </h2>
                    <p className="mt-1 text-sm text-muted-foreground">{g.description}</p>
                  </li>
                )}
                {g.items.map((a) => (
                  <ArtworkTile
                    key={a.id}
                    artwork={a}
                    selected={!!a.tvContentId && a.tvContentId === selectedId}
                    guess={!!a.tvContentId && a.tvContentId === guessId}
                    pending={!!a.tvContentId && a.tvContentId === pendingId}
                    busy={!!pendingId}
                    onSelect={selectArtwork}
                    onOpen={onOpen}
                    selectMode={selecting}
                    checked={checkedIds.has(a.id)}
                    onToggle={toggleChecked}
                    onLongPress={startSelect}
                  />
                ))}
              </Fragment>
            ))}
          </ul>
          <div ref={sentinel} className="flex justify-center pt-8">
            {hasNextPage && (
              <Button variant="outline" onClick={() => void fetchNextPage()} disabled={isFetchingNextPage}>
                {isFetchingNextPage && <LoaderCircle className="animate-spin" />}
                Load more
              </Button>
            )}
          </div>
        </>
      )}

      <Section title="Sources" description="Folders vigo keeps in sync with the TV." className="mt-14">
        <SourcesPanel sources={sources} />
      </Section>

      <FilterSheet
        open={filtersOpen}
        onOpenChange={setFiltersOpen}
        tags={tagCounts.data ?? []}
        sources={sources}
        selected={tags}
        onToggle={toggleTag}
        onClear={() => setTags([])}
      />
      <ArtworkSheet
        artwork={opened}
        sources={sources}
        selected={!!opened?.tvContentId && opened.tvContentId === selectedId}
        guess={!!opened?.tvContentId && opened.tvContentId === guessId}
        pending={!!opened?.tvContentId && opened.tvContentId === pendingId}
        busy={!!pendingId}
        onOpenChange={(o) => !o && setOpenId(null)}
        onSelect={selectArtwork}
        onCrop={(id) => {
          setOpenId(null);
          setCropId(id);
        }}
      />
      <CropEditor artwork={cropping} sources={sources} onOpenChange={(o) => !o && setCropId(null)} onSaved={() => setCropId(null)} />
      {selecting && (
        <SelectionBar
          selected={selectedItems}
          sources={sources}
          selectableCount={selectable.length}
          onSelectAll={() => setSelection({ view: viewKey, ids: selectable.map((a) => a.id), anchor: null })}
          onClear={() => setSelection({ view: viewKey, ids: [], anchor: null })}
          onCancel={exitSelect}
          onDone={exitSelect}
        />
      )}

      <UploadDialog open={uploadOpen} onOpenChange={setUploadOpen} sources={writable} folders={folders} />
    </Page>
  );
}
