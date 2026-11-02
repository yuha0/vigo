import { defineConfig } from "@vite-pwa/assets-generator/config";

// The source icon is full-bleed with its artwork inside the maskable safe zone,
// so no variant needs extra padding.
export default defineConfig({
  headLinkOptions: { preset: "2023" },
  preset: {
    transparent: { sizes: [64, 192, 512], favicons: [[48, "favicon.ico"]], padding: 0 },
    maskable: { sizes: [512], padding: 0 },
    apple: { sizes: [180], padding: 0 },
  },
  images: ["public/icon.svg"],
});
