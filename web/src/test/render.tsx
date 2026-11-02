import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { App } from "@/app/App";
import { createMockBackend, type MockBackend, type MockBackendOptions } from "@/lib/mock";

export function renderApp(path: string, options: MockBackendOptions = {}, setup?: (backend: MockBackend) => void) {
  const backend = createMockBackend(options);
  setup?.(backend);
  const user = userEvent.setup();
  const utils = render(<App backend={backend} initialEntries={[path]} />);
  return { ...utils, backend, user };
}
