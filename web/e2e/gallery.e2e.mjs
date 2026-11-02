// End-to-end check of the Gallery against a running vigo-core, through the Vite dev proxy.
// Usage: E2E_BASE_URL=http://localhost:5173 node e2e/gallery.e2e.mjs
// It changes the library: it uploads, crops and deletes one generated image under "e2e/".
import { mkdirSync } from "node:fs";

import { chromium } from "playwright";

const base = process.env.E2E_BASE_URL ?? "http://localhost:5173";
const shots = new URL("../screenshots/", import.meta.url).pathname;
mkdirSync(shots, { recursive: true });

const results = [];
const check = async (name, fn) => {
  try {
    const detail = await fn();
    results.push(["PASS", name, detail ?? ""]);
  } catch (err) {
    results.push(["FAIL", name, String(err?.message ?? err).split("\n")[0]]);
  }
};

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
const consoleErrors = [];
page.on("console", (m) => m.type() === "error" && consoleErrors.push(m.text()));
page.on("pageerror", (e) => consoleErrors.push(String(e)));

const tiles = () => page.getByRole("list", { name: "Artworks" }).getByRole("listitem");
const rpc = async (method, body = {}) =>
  page.evaluate(
    async ([m, b]) => (await fetch(`/vigo.core.v1.LibraryService/${m}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(b) })).json(),
    [method, body],
  );

await page.goto(`${base}/gallery`);

await check("lists artworks", async () => {
  await page.getByRole("list", { name: "Artworks" }).waitFor();
  const api = await rpc("ListArtworks", { includeDeleting: true, pageSize: 60 });
  const n = await tiles().count();
  if (n !== api.artworks.length) throw new Error(`${n} tiles, API returned ${api.artworks.length}`);
  return `${n} tiles of ${api.total}`;
});

await check("thumbnails load", async () => {
  await page.waitForTimeout(1500);
  const r = await page.evaluate(() => {
    const imgs = [...document.querySelectorAll('ul[aria-label="Artworks"] img')].filter((i) => i.getBoundingClientRect().top < innerHeight);
    return { total: imgs.length, ok: imgs.filter((i) => i.complete && i.naturalWidth > 0).length };
  });
  if (!r.total || r.ok < r.total) throw new Error(`${r.ok}/${r.total} visible thumbnails loaded`);
  return `${r.ok}/${r.total} visible`;
});
await page.screenshot({ path: `${shots}gallery-desktop.png`, fullPage: false });

await check("tag filter maps to the API", async () => {
  const tags = (await rpc("ListTags")).tags;
  const issue = tags.find((t) => t.tag.name === "issue/low-resolution");
  if (!issue) return "no low-resolution artworks to filter";
  await page.getByRole("button", { name: /^Filters/ }).click();
  await page.getByRole("dialog", { name: "Filter" }).getByTitle("issue/low-resolution").click();
  await page.getByRole("button", { name: "Show results" }).click();
  const api = await rpc("ListArtworks", { includeDeleting: true, tags: ["issue/low-resolution"] });
  await page.waitForFunction((n) => document.querySelectorAll('ul[aria-label="Artworks"] > li[data-artwork-id]').length === n, api.artworks.length, { timeout: 5000 });
  await page.getByRole("button", { name: /^Clear$/ }).click();
  return `${api.artworks.length} match`;
});

await check("search maps to the API", async () => {
  await page.getByRole("searchbox", { name: "Search" }).fill("harbour");
  const api = await rpc("ListArtworks", { includeDeleting: true, query: "harbour" });
  await page.waitForFunction((n) => document.querySelectorAll('ul[aria-label="Artworks"] > li[data-artwork-id]').length === n, api.artworks.length, { timeout: 5000 });
  await page.getByRole("searchbox", { name: "Search" }).fill("");
  return `${api.artworks.length} match`;
});

const name = `e2e-${Date.now()}.png`;
await check("upload into a folder", async () => {
  const dataUrl = await page.evaluate(() => {
    const c = document.createElement("canvas");
    c.width = 1600;
    c.height = 1200;
    const g = c.getContext("2d");
    const grad = g.createLinearGradient(0, 0, 1600, 1200);
    grad.addColorStop(0, `hsl(${Math.random() * 360} 60% 60%)`);
    grad.addColorStop(1, "#203040");
    g.fillStyle = grad;
    g.fillRect(0, 0, 1600, 1200);
    g.fillStyle = "#fff";
    g.font = "120px sans-serif";
    g.fillText("vigo e2e", 420, 640);
    return c.toDataURL("image/png");
  });
  await page.getByRole("button", { name: "Upload" }).click();
  const dialog = page.getByRole("dialog", { name: "Add photos" });
  await dialog.getByRole("combobox", { name: "Folder (optional)" }).fill("e2e");
  await dialog.getByLabel("Photos to upload").setInputFiles({ name, mimeType: "image/png", buffer: Buffer.from(dataUrl.split(",")[1], "base64") });
  await page.screenshot({ path: `${shots}upload-dialog.png` });
  await dialog.getByRole("button", { name: "Upload", exact: true }).click();
  await dialog.waitFor({ state: "detached", timeout: 15000 });
  await page.getByRole("searchbox", { name: "Search" }).fill("e2e-");
  await page.getByRole("button", { name: new RegExp(`^${name.replace(/\./g, "\\.")}`) }).waitFor({ timeout: 10000 });
  const states = [];
  for (let i = 0; i < 20; i++) {
    const s = await page.locator(`li:has(button[aria-label^="${name}"])`).getAttribute("data-sync-state");
    if (states.at(-1) !== s) states.push(s);
    if (s === "IN_SYNC" || s === "ERROR") break;
    await page.waitForTimeout(500);
  }
  return `states seen: ${states.join(" → ")}`;
});
await page.screenshot({ path: `${shots}after-upload.png` });

let croppedName = name.replace(/\.png$/, ".jpg");
await check("crop to 16:9", async () => {
  await page.getByRole("button", { name: `Details for ${name}` }).click();
  const sheet = page.getByRole("dialog", { name });
  await sheet.getByRole("button", { name: "Crop" }).click();
  const editor = page.getByRole("dialog", { name: "Crop to 16:9" });
  await editor.waitFor({ timeout: 5000 });
  await editor.getByTestId("crop-area").locator("img").evaluate((img) => img.decode());
  await page.screenshot({ path: `${shots}crop-editor.png` });
  const size = await editor.getByTestId("crop-size").textContent();
  await editor.getByRole("button", { name: "Overwrite original" }).click();
  await editor.waitFor({ state: "detached", timeout: 10000 });
  const after = await rpc("ListArtworks", { includeDeleting: true, query: name.replace(/\.png$/, "") });
  const a = after.artworks[0];
  if (!a) throw new Error("artwork gone after crop");
  croppedName = a.sources[0].path.split("/").pop();
  if (Math.abs(a.width / a.height - 16 / 9) > 0.001) throw new Error(`now ${a.width}×${a.height}`);
  return `${size} → ${a.width}×${a.height}, ${a.sources[0].path}`;
});

await check("delete with confirmation", async () => {
  await page.getByRole("searchbox", { name: "Search" }).fill(croppedName.replace(/\.\w+$/, ""));
  await page.getByRole("button", { name: `Details for ${croppedName}` }).click();
  await page.getByRole("dialog", { name: croppedName }).getByRole("button", { name: "Delete" }).click();
  const confirm = page.getByRole("alertdialog");
  const text = await confirm.textContent();
  await confirm.getByRole("button", { name: /^Delete/ }).click();
  const states = [];
  for (let i = 0; i < 20; i++) {
    const li = page.locator(`li:has(button[aria-label^="${croppedName}"])`);
    const s = (await li.count()) ? await li.getAttribute("data-sync-state") : "gone";
    if (states.at(-1) !== s) states.push(s);
    if (s === "gone") break;
    await page.waitForTimeout(500);
  }
  return `"${text.slice(0, 90)}…" states: ${states.join(" → ")}`;
});

await check("phone layout", async () => {
  await page.keyboard.press("Escape");
  await page.screenshot({ path: `${shots}end-state.png` });
  await page.getByRole("searchbox", { name: "Search" }).fill("", { timeout: 3000 });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${shots}gallery-phone.png` });
});

await browser.close();
for (const [s, n, d] of results) console.log(`${s}  ${n}${d ? `  (${d})` : ""}`);
if (consoleErrors.length) console.log(`console errors:\n  ${consoleErrors.slice(0, 10).join("\n  ")}`);
process.exitCode = results.some(([s]) => s === "FAIL") ? 1 : 0;
