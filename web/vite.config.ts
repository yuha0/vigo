/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";
import { VitePWA } from "vite-plugin-pwa";

// Paths vigo-core serves itself; everything else is the SPA.
const backendPaths = ["^/vigo\\.core\\.v1\\.", "^/vigo\\.home\\.v1\\.", "^/art(/|$)", "^/library/", "^/sources/", "^/healthz$"];

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const target = env.VIGO_CORE_URL || "http://localhost:8080";
  // No changeOrigin: vigo-core rejects state-changing requests whose Origin
  // doesn't match Host, so the browser's Host (the dev server) must pass through.
  const proxy = Object.fromEntries(backendPaths.map((p) => [p, { target }]));

  return {
    resolve: {
      alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
    },
    plugins: [
      react(),
      tailwindcss(),
      !process.env.VITEST &&
        VitePWA({
        registerType: "autoUpdate",
        injectRegister: false,
        pwaAssets: { config: true, overrideManifestIcons: true },
        manifest: {
          name: "vigo",
          short_name: "vigo",
          description: "Your Frame, at hand.",
          display: "standalone",
          orientation: "any",
          start_url: "/",
          scope: "/",
          background_color: "#f5f3ee",
          theme_color: "#f5f3ee",
          categories: ["utilities", "lifestyle"],
        },
        workbox: {
          globPatterns: ["**/*.{js,css,html,svg,png,ico,woff2}"],
          // Only precache the Latin font subsets; others still load on demand.
          globIgnores: ["**/*-{cyrillic,cyrillic-ext,greek,greek-ext,vietnamese}-*.woff2"],
          navigateFallback: "/index.html",
          navigateFallbackDenylist: backendPaths.map((p) => new RegExp(p)),
          runtimeCaching: [
            {
              // Content-addressed: the bytes behind a sha256 id never change.
              urlPattern: ({ url }) => /^\/library\/sha256(:|%3A)[^/]+\/(thumbnail|image)$/i.test(url.pathname),
              handler: "CacheFirst",
              options: {
                cacheName: "library-immutable",
                expiration: { maxEntries: 600, maxAgeSeconds: 60 * 60 * 24 * 90 },
                // Never keep a 401 (signed out) or any other error in place of the image.
                cacheableResponse: { statuses: [200] },
              },
            },
            {
              urlPattern: ({ url }) => /^\/(art|library)\/[^/]+\/thumbnail$/.test(url.pathname),
              handler: "StaleWhileRevalidate",
              options: {
                cacheName: "thumbnails",
                cacheableResponse: { statuses: [200] },
                expiration: { maxEntries: 400, maxAgeSeconds: 60 * 60 * 24 * 30 },
              },
            },
          ],
        },
      }),
    ],
    server: { proxy },
    preview: { proxy },
    build: {
      outDir: "dist",
      emptyOutDir: true,
      rolldownOptions: {
        output: {
          codeSplitting: {
            groups: [
              { name: (id: string) => (/node_modules\/(react|react-dom|scheduler|react-router)\//.test(id) ? "react" : null) },
              { name: (id: string) => (/node_modules\/@(bufbuild|connectrpc)\//.test(id) ? "rpc" : null) },
              { name: (id: string) => (/node_modules\//.test(id) ? "vendor" : null) },
            ],
          },
        },
      },
    },
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./src/test/setup.ts"],
      css: false,
      restoreMocks: true,
    },
  };
});
