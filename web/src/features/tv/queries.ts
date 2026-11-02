import { create } from "@bufbuild/protobuf";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  ConfigService,
  ListSettingsResponseSchema,
  PairingService,
  TVService,
  type Config,
  type ListSettingsResponse,
  type Pairing,
} from "@/gen/vigo/core/v1/core_pb";
import { withValue, type SettingValue } from "@/features/tv/setting-meta";
import { useClient } from "@/lib/backend";
import { qk } from "@/lib/queries";

export function useSettings(enabled: boolean) {
  const client = useClient(TVService);
  return useQuery({
    queryKey: qk.settings,
    queryFn: ({ signal }) => client.listSettings({}, { signal }),
    enabled,
  });
}

export function useUpdateSetting() {
  const client = useClient(TVService);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ key, value }: { key: string; value: SettingValue }) => client.updateSetting({ key, value }),
    onMutate: async ({ key, value }) => {
      await qc.cancelQueries({ queryKey: qk.settings });
      const prev = qc.getQueryData<ListSettingsResponse>(qk.settings);
      if (prev) {
        qc.setQueryData(
          qk.settings,
          create(ListSettingsResponseSchema, { settings: prev.settings.map((s) => (s.key === key ? withValue(s, value) : s)) }),
        );
      }
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(qk.settings, ctx.prev);
    },
    onSuccess: (res) => {
      const setting = res.setting;
      if (!setting) return;
      qc.setQueryData<ListSettingsResponse>(qk.settings, (old) =>
        old ? create(ListSettingsResponseSchema, { settings: old.settings.map((s) => (s.key === setting.key ? setting : s)) }) : old,
      );
    },
    meta: { action: "Change the setting" },
  });
}

export function usePairing() {
  const client = useClient(PairingService);
  return useQuery({
    queryKey: qk.pairing,
    queryFn: async ({ signal }) => (await client.getPairing({}, { signal })).pairing ?? null,
    staleTime: Infinity,
  });
}

export function useStartPairing() {
  const client = useClient(PairingService);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => client.startPairing({}),
    onSuccess: (res) => res.pairing && qc.setQueryData<Pairing>(qk.pairing, res.pairing),
    meta: { action: "Start pairing" },
  });
}

export function useCancelPairing() {
  const client = useClient(PairingService);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => client.cancelPairing({}),
    onSuccess: (res) => res.pairing && qc.setQueryData<Pairing>(qk.pairing, res.pairing),
    meta: { action: "Cancel pairing" },
  });
}

export function useConfig() {
  const client = useClient(ConfigService);
  return useQuery({
    queryKey: qk.config,
    queryFn: async ({ signal }) => (await client.getConfig({}, { signal })).config ?? null,
  });
}

export function useUpdateConfig() {
  const client = useClient(ConfigService);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (config: Config) => client.updateConfig({ config }),
    onSuccess: (res) => qc.setQueryData(qk.config, res.config ?? null),
    meta: { action: "Save the connection" },
  });
}
