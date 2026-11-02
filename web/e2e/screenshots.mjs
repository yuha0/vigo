// Screenshots of a page in phone and desktop sizes, light and dark.
// Usage: E2E_BASE_URL=http://localhost:5173 [SECTION="Art settings"] node e2e/screenshots.mjs <name> [path]
// SECTION captures just the page section with that heading; PAGE waits for that page heading and captures the viewport.
import { mkdirSync } from "node:fs";

import { chromium } from "playwright";

const base = process.env.E2E_BASE_URL ?? "http://localhost:5173";
const [name = "shot", path = "/gallery"] = process.argv.slice(2);
const dir = new URL("../screenshots/", import.meta.url).pathname;
mkdirSync(dir, { recursive: true });

const browser = await chromium.launch();
for (const [device, viewport] of [
  ["phone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
]) {
  for (const scheme of ["light", "dark"]) {
    const page = await browser.newPage({ viewport, colorScheme: scheme, deviceScaleFactor: device === "phone" ? 2 : 1 });
    await page.goto(base + path);
    const section = process.env.SECTION
      ? page.locator("section", { has: page.getByRole("heading", { name: process.env.SECTION, exact: true }) }).first()
      : null;
    const ready = process.env.PAGE
      ? page.getByRole("heading", { name: process.env.PAGE, level: 1 })
      : page.getByRole("list", { name: "Artworks" }).first();
    await (section ?? ready).waitFor();
    await page.waitForTimeout(1200);
    const file = `${dir}${name}-${device}-${scheme}.png`;
    if (section) await section.screenshot({ path: file });
    else await page.screenshot({ path: file });
    if (process.env.SCROLL) {
      await page.mouse.wheel(0, Number(process.env.SCROLL));
      await page.waitForTimeout(600);
      await page.screenshot({ path: file.replace(/\.png$/, "-scrolled.png") });
    }
    console.log(file.replace(/.*\/web\//, "web/"));
    await page.close();
  }
}
await browser.close();
