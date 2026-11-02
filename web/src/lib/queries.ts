import { timestampMs } from "@bufbuild/protobuf/wkt";
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import { TVService, type Status } from "@/gen/vigo/core/v1/core_pb";
import { useClient } from "@/lib/backend";

declare module "@tanstack/react-query" {
  interface Register {
    mutationMeta: {
      /** Verb phrase for error toasts, e.g. "Show art". */
      action?: string;
      /** The caller reports errors itself. */
      silent?: boolean;
    };
  }
}

export const qk = {
  status: ["tv", "status"],
  settings: ["tv", "settings"],
  library: ["library"],
  libraryArtworks: ["library", "artworks"],
  libraryTags: ["library", "tags"],
  librarySources: ["library", "sources"],
  pairing: ["pairing"],
  config: ["config"],
  home: ["home"],
  homeProvider: ["home", "provider"],
  homeGroups: ["home", "groups"],
  homeAccessories: ["home", "accessories"],
} as const;

function updatedMs(s: Status | null | undefined): number {
  return s?.updatedAt ? timestampMs(s.updatedAt) : 0;
}

/** Keeps whichever of two snapshots is newer; the stream and GetStatus can race. */
export function setStatus(qc: QueryClient, next: Status | undefined) {
  if (!next) return;
  qc.setQueryData<Status | null>(qk.status, (prev) => (prev && updatedMs(prev) > updatedMs(next) ? prev : next));
}

export function useStatus() {
  const client = useClient(TVService);
  const qc = useQueryClient();
  return useQuery({
    queryKey: qk.status,
    queryFn: async ({ signal }) => {
      const res = await client.getStatus({}, { signal });
      const cached = qc.getQueryData<Status | null>(qk.status);
      if (cached && res.status && updatedMs(cached) > updatedMs(res.status)) return cached;
      return res.status ?? null;
    },
    staleTime: Infinity,
  });
}

export function useShowArt() {
  const client = useClient(TVService);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (contentId: string = "") => client.showArt({ contentId }),
    onSuccess: (res) => setStatus(qc, res.status),
    meta: { action: "Show art" },
  });
}

export function useSetArtMode() {
  const client = useClient(TVService);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (on: boolean) => client.setArtMode({ on }),
    onSuccess: (res) => setStatus(qc, res.status),
    meta: { action: "Change Art Mode" },
  });
}
