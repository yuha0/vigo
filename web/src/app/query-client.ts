import { Code, ConnectError } from "@connectrpc/connect";
import { MutationCache, QueryClient } from "@tanstack/react-query";

import type { Status } from "@/gen/vigo/core/v1/core_pb";
import { toastError } from "@/lib/errors";
import { qk } from "@/lib/queries";

const noRetry = new Set([Code.FailedPrecondition, Code.InvalidArgument, Code.NotFound, Code.Unavailable, Code.Unimplemented]);

export function createQueryClient(): QueryClient {
  const qc: QueryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: (count, err) => count < 2 && !noRetry.has(ConnectError.from(err).code),
        refetchOnWindowFocus: false,
        staleTime: 15_000,
      },
      mutations: { retry: false },
    },
    mutationCache: new MutationCache({
      onError: (err, _vars, _ctx, mutation) => {
        if (mutation.meta?.silent) return;
        toastError(err, { action: mutation.meta?.action, status: qc.getQueryData<Status | null>(qk.status) });
      },
    }),
  });
  return qc;
}
