import { create } from "@bufbuild/protobuf";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { HomeService, ListAccessoriesResponseSchema, type ListAccessoriesResponse, type ListPairingGroupsResponse } from "@/gen/vigo/home/v1/home_pb";
import { useClient } from "@/lib/backend";
import { qk } from "@/lib/queries";

export function useProvider() {
  const client = useClient(HomeService);
  return useQuery({
    queryKey: qk.homeProvider,
    queryFn: async ({ signal }) => (await client.getProvider({}, { signal })).provider ?? null,
    refetchOnWindowFocus: true,
  });
}

export function useGroups(enabled: boolean) {
  const client = useClient(HomeService);
  return useQuery({
    queryKey: qk.homeGroups,
    queryFn: ({ signal }) => client.listPairingGroups({}, { signal }),
    enabled,
    refetchOnWindowFocus: true,
    // Pairing happens in the Home app, so look for it while this screen is open.
    refetchInterval: 10_000,
  });
}

export function useAccessories(enabled: boolean) {
  const client = useClient(HomeService);
  return useQuery({
    queryKey: qk.homeAccessories,
    queryFn: ({ signal }) => client.listAccessories({}, { signal }),
    enabled,
  });
}

export function useSetAccessoryEnabled() {
  const client = useClient(HomeService);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; enabled: boolean }) => client.setAccessoryEnabled(v),
    onMutate: async ({ id, enabled }) => {
      await qc.cancelQueries({ queryKey: qk.homeAccessories });
      const prev = qc.getQueryData<ListAccessoriesResponse>(qk.homeAccessories);
      if (prev) {
        qc.setQueryData(
          qk.homeAccessories,
          create(ListAccessoriesResponseSchema, { accessories: prev.accessories.map((a) => (a.id === id ? { ...a, enabled } : a)) }),
        );
      }
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(qk.homeAccessories, ctx.prev);
    },
    onSettled: () => {
      // Enabling the first bridge accessory starts advertising the bridge, and vice versa.
      void qc.invalidateQueries({ queryKey: qk.homeGroups });
      void qc.invalidateQueries({ queryKey: qk.homeAccessories });
    },
    meta: { action: "Change the accessory" },
  });
}

export function useResetPairing() {
  const client = useClient(HomeService);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (groupId: string) => client.resetPairing({ groupId }),
    onSuccess: (res) => {
      const group = res.group;
      if (!group) return;
      qc.setQueryData<ListPairingGroupsResponse>(qk.homeGroups, (old) =>
        old ? { ...old, groups: old.groups.map((g) => (g.id === group.id ? group : g)) } : old,
      );
    },
    meta: { action: "Reset pairing" },
  });
}
