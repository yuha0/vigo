// Counts requests per endpoint while the app sits idle on a page, to catch
// re-subscribe or refetch loops. Against a running vigo-core, via the Vite dev proxy.
// Usage: E2E_BASE_URL=http://localhost:5173 E2E_PASSWORD=… node e2e/request-count.mjs [chromium|firefox] [seconds]
import * as playwright from "playwright";

const base = process.env.E2E_BASE_URL ?? "http://localhost:5173";
const password = process.env.E2E_PASSWORD;
const browserName = process.argv[2] ?? "chromium";
const seconds = Number(process.argv[3] ?? 30);
const pages = ["/gallery", "/", "/tv"];

// Idle budgets per window: one stream, one Refresh on load, a handful of list calls.
const limits = { WatchEvents: 2, ListArtworks: 8, ListSources: 3, ListTags: 3 };

const browser = await playwright[browserName].launch();
const context = await browser.newContext({ ignoreHTTPSErrors: !!process.env.E2E_INSECURE });
const page = await context.newPage();

const endpoint = (url) => {
  const p = new URL(url).pathname;
  if (/^\/library\/[^/]+\/thumbnail$/.test(p)) return "thumbnail";
  if (/^\/art\/[^/]+\/thumbnail$/.test(p)) return "tv-thumbnail";
  const m = /^\/vigo\.[a-z]+\.v1\.\w+\/(\w+)$/.exec(p);
  return m ? m[1] : null;
};

let counting = false;
let counts = {};
let failed = {};
page.on("request", (r) => {
  const e = endpoint(r.url());
  if (counting && e) counts[e] = (counts[e] ?? 0) + 1;
});
page.on("requestfailed", (r) => {
  const e = endpoint(r.url());
  if (counting && e) failed[e] = (failed[e] ?? 0) + 1;
});
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));

await page.goto(base);
const setup = page.getByRole("button", { name: "Set password" });
const signIn = page.getByRole("button", { name: "Sign in" });
await Promise.race([setup.waitFor(), signIn.waitFor(), page.getByRole("navigation", { name: "Main" }).waitFor()]);
if (await setup.count()) {
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Confirm password", { exact: true }).fill(password);
  await setup.click();
} else if (await signIn.count()) {
  await page.getByLabel("Password", { exact: true }).fill(password);
  await signIn.click();
}
await page.getByRole("navigation", { name: "Main" }).waitFor();

let bad = false;
for (const path of pages) {
  counts = {};
  failed = {};
  counting = true;
  await page.goto(base + path);
  await page.waitForTimeout(seconds * 1000);
  counting = false;
  const errorCard = await page.getByText("Something went wrong").count();
  const over = Object.entries(limits).filter(([k, n]) => (counts[k] ?? 0) > n);
  if (over.length || errorCard) bad = true;
  console.log(`${browserName} ${path} over ${seconds}s (incl. initial load):`);
  for (const [k, n] of Object.entries(counts).sort()) console.log(`  ${k.padEnd(18)} ${String(n).padStart(4)}${failed[k] ? `  (${failed[k]} failed/aborted)` : ""}`);
  if (errorCard) console.log("  !! error card shown");
  for (const [k, n] of over) console.log(`  !! ${k} ${counts[k]} > ${n}`);
}
if (errors.length) console.log(`page errors:\n  ${errors.slice(0, 5).join("\n  ")}`);
await browser.close();
process.exitCode = bad ? 1 : 0;
