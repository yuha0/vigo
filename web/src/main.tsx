import "@fontsource-variable/inter/opsz.css";
import "@fontsource-variable/fraunces/opsz.css";
import "./index.css";

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "@/app/App";
import { createHttpBackend, type Backend } from "@/lib/backend";

async function createBackend(): Promise<Backend> {
  if (import.meta.env.VITE_MOCK !== "1") return createHttpBackend();
  const [{ createMockBackend }, { scenarios, homeModes }] = await Promise.all([import("@/lib/mock"), import("@/lib/mock/control")]);
  const params = new URLSearchParams(window.location.search);
  const scenario = scenarios.find((s) => s === params.get("scenario")) ?? "art";
  const homeMode = homeModes.find((m) => m === params.get("home")) ?? "online";
  const sources = (["default", "single", "read-only"] as const).find((s) => s === params.get("sources")) ?? "default";
  return createMockBackend({
    scenario,
    homeMode,
    sources,
    latencyMs: 220,
    transitionMs: 900,
    pairingDelayMs: 7000,
    motionMinuteMs: 8000,
  });
}

const backend = await createBackend();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App backend={backend} />
  </StrictMode>,
);

if (import.meta.env.PROD && !backend.mock) {
  const { registerSW } = await import("virtual:pwa-register");
  // Browsers only look for a new service worker on navigation, so an open
  // tab or a resumed PWA would keep running an old build indefinitely. With
  // autoUpdate, a found update activates and reloads the page.
  registerSW({
    immediate: true,
    onRegisteredSW(_url, registration) {
      if (!registration) return;
      const check = () => {
        if (document.visibilityState === "visible" && navigator.onLine) void registration.update();
      };
      document.addEventListener("visibilitychange", check);
      setInterval(check, 5 * 60_000);
    },
  });
}
