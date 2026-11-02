import { QueryClientProvider, type QueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { createBrowserRouter, createMemoryRouter, RouterProvider } from "react-router";

import { MockPanel } from "@/app/MockPanel";
import { createQueryClient } from "@/app/query-client";
import { routes } from "@/app/routes";
import { Toaster } from "@/components/ui/sonner";
import { AuthGate } from "@/features/auth/AuthGate";
import { AuthProvider } from "@/lib/auth";
import { BackendProvider, type Backend } from "@/lib/backend";
import { LiveProvider } from "@/lib/live";

export function App({ backend, initialEntries, queryClient: given }: { backend: Backend; initialEntries?: string[]; queryClient?: QueryClient }) {
  const [queryClient] = useState(() => given ?? createQueryClient());
  const [router] = useState(() => (initialEntries ? createMemoryRouter(routes, { initialEntries }) : createBrowserRouter(routes)));

  return (
    <QueryClientProvider client={queryClient}>
      <BackendProvider backend={backend}>
        <AuthProvider>
          <AuthGate>
            <LiveProvider>
              <RouterProvider router={router} />
            </LiveProvider>
          </AuthGate>
          <Toaster />
          {backend.mock && import.meta.env.MODE !== "test" && <MockPanel control={backend.mock} />}
        </AuthProvider>
      </BackendProvider>
    </QueryClientProvider>
  );
}
