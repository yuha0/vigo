import type { DescService } from "@bufbuild/protobuf";
import { createClient, type Client, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { createContext, use, useMemo, type ReactNode } from "react";

import { createHttpArtApi, type ArtApi } from "@/lib/art-api";
import type { MockControl } from "@/lib/mock/control";

export type Backend = {
  transport: Transport;
  art: ArtApi;
  /** Present only when running against the in-browser mock. */
  mock?: MockControl;
};

export function createHttpBackend(baseUrl = window.location.origin): Backend {
  return {
    transport: createConnectTransport({ baseUrl, useBinaryFormat: true }),
    art: createHttpArtApi(""),
  };
}

const BackendContext = createContext<Backend | null>(null);

export function BackendProvider({ backend, children }: { backend: Backend; children: ReactNode }) {
  return <BackendContext value={backend}>{children}</BackendContext>;
}

export function useBackend(): Backend {
  const backend = use(BackendContext);
  if (!backend) throw new Error("useBackend outside BackendProvider");
  return backend;
}

export function useClient<T extends DescService>(service: T): Client<T> {
  const { transport } = useBackend();
  return useMemo(() => createClient(service, transport), [service, transport]);
}

export function useArtApi(): ArtApi {
  return useBackend().art;
}
