import { QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { createBrowserRouter, createMemoryRouter, RouterProvider } from "react-router";

import { MockPanel } from "@/app/MockPanel";
import { createQueryClient } from "@/app/query-client";
import { routes } from "@/app/routes";
import { Toaster } from "@/components/ui/sonner";
import { BackendProvider, type Backend } from "@/lib/backend";
import { LiveProvider } from "@/lib/live";

export function App({ backend, initialEntries }: { backend: Backend; initialEntries?: string[] }) {
  const [queryClient] = useState(createQueryClient);
  const [router] = useState(() => (initialEntries ? createMemoryRouter(routes, { initialEntries }) : createBrowserRouter(routes)));

  return (
    <QueryClientProvider client={queryClient}>
      <BackendProvider backend={backend}>
        <LiveProvider>
          <RouterProvider router={router} />
          <Toaster />
          {backend.mock && import.meta.env.MODE !== "test" && <MockPanel control={backend.mock} />}
        </LiveProvider>
      </BackendProvider>
    </QueryClientProvider>
  );
}
