import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { ArtworkOrigin, LibraryService, type Rect } from "@/gen/vigo/core/v1/library_pb";
import { useClient } from "@/lib/backend";
import { qk } from "@/lib/queries";

export const PAGE_SIZE = 60;

export type ArtworkFilter = { tags: string[]; query: string; origin: ArtworkOrigin };

export function useArtworks(filter: ArtworkFilter) {
  const client = useClient(LibraryService);
  const tags = [...filter.tags].sort();
  const query = filter.query.trim();
  const origin = filter.origin;
  return useInfiniteQuery({
    queryKey: [...qk.libraryArtworks, tags, query, origin],
    queryFn: ({ pageParam, signal }) =>
      client.listArtworks({ tags, query, origin, includeDeleting: true, pageSize: PAGE_SIZE, pageToken: pageParam }, { signal }),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextPageToken || undefined,
    placeholderData: keepPreviousData,
  });
}

export function useTags() {
  const client = useClient(LibraryService);
  return useQuery({
    queryKey: qk.libraryTags,
    queryFn: async ({ signal }) => (await client.listTags({}, { signal })).tags,
  });
}

export function useSources() {
  const client = useClient(LibraryService);
  return useQuery({
    queryKey: qk.librarySources,
    queryFn: async ({ signal }) => (await client.listSources({}, { signal })).sources,
  });
}

function useLibraryMutation<V, R>(fn: (v: V) => Promise<R>, action: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSettled: () => qc.invalidateQueries({ queryKey: qk.library }),
    meta: { action },
  });
}

export function useUpdateTags() {
  const client = useClient(LibraryService);
  return useLibraryMutation(
    (v: { artworkId: string; add?: string[]; remove?: string[] }) => client.updateTags({ artworkId: v.artworkId, add: v.add ?? [], remove: v.remove ?? [] }),
    "Change the tags",
  );
}

export function useDeleteArtwork() {
  const client = useClient(LibraryService);
  return useLibraryMutation((id: string) => client.deleteArtwork({ id }), "Delete the artwork");
}

export function useCropArtwork() {
  const client = useClient(LibraryService);
  return useLibraryMutation((v: { id: string; rect: Rect }) => client.cropArtwork({ id: v.id, rect: v.rect }), "Crop the artwork");
}

export function useSyncSource() {
  const client = useClient(LibraryService);
  return useLibraryMutation((sourceId: string) => client.syncSource({ sourceId }), "Sync the source");
}

/** Library-wide counts for the Art Store / Local tabs, exact from the server's origin filter. */
export function useScopeCounts() {
  const client = useClient(LibraryService);
  return useQuery({
    queryKey: [...qk.library, "scope-counts"],
    queryFn: async ({ signal }) => {
      const total = (origin: ArtworkOrigin) => client.listArtworks({ origin, includeDeleting: true, pageSize: 1 }, { signal }).then((r) => r.total);
      const [store, local] = await Promise.all([total(ArtworkOrigin.TV_ONLY), total(ArtworkOrigin.SOURCES)]);
      return { store, local };
    },
  });
}

export function useDeleteArtworks() {
  const client = useClient(LibraryService);
  return useLibraryMutation(async (ids: string[]) => (await client.deleteArtworks({ ids })).results, "Delete the artworks");
}

export function useRefreshThumbnails() {
  const client = useClient(LibraryService);
  return useLibraryMutation(async (ids: string[]) => (await client.refreshThumbnails({ ids })).results, "Refresh the thumbnails");
}
