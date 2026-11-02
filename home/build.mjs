import { build } from "esbuild";

await build({
  entryPoints: ["src/main.ts"],
  outfile: "dist/main.js",
  bundle: true,
  platform: "node",
  format: "esm",
  target: "node26",
  // Dependencies are shipped as node_modules; hap-nodejs does not bundle cleanly.
  packages: "external",
  sourcemap: true,
  logLevel: "info",
});
