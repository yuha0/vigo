// End-to-end check of password auth against a running vigo-core, through the Vite dev proxy.
// Needs a core with no password set (`vigo-core reset-password`).
// Usage: E2E_BASE_URL=http://localhost:5173 node e2e/auth.e2e.mjs [setup-only]
import { chromium } from "playwright";

const base = process.env.E2E_BASE_URL ?? "http://localhost:5173";
const first = "lantern-river-quiet-1";
const second = "harbour-lantern-quiet-2";
const results = [];
const check = async (name, fn) => {
  try {
    results.push(["PASS", name, (await fn()) ?? ""]);
  } catch (err) {
    results.push(["FAIL", name, String(err?.message ?? err).split("\n")[0]]);
  }
};

const browser = await chromium.launch();
const report = async () => {
  await browser.close();
  for (const [s, n, d] of results) console.log(`${s}  ${n}${d ? `  (${d})` : ""}`);
  process.exitCode = results.some(([s]) => s === "FAIL") ? 1 : 0;
};

if (process.argv[2] === "setup-only") {
  const page = await (await browser.newContext()).newPage();
  await check("setup screen after reset-password", async () => {
    await page.goto(base);
    await page.getByRole("heading", { name: "Set a password for vigo" }).waitFor({ timeout: 10000 });
  });
  await report();
  process.exit();
}

const ctxA = await browser.newContext();
const a = await ctxA.newPage();
const rail = (p) => p.getByRole("navigation", { name: "Main" });
const password = (p, label = "Password") => p.getByLabel(label, { exact: true });

await check("first-run setup", async () => {
  await a.goto(base);
  await a.getByRole("heading", { name: "Set a password for vigo" }).waitFor({ timeout: 10000 });
  await password(a).fill(first);
  await password(a, "Confirm password").fill(first);
  await a.getByRole("button", { name: "Set password" }).click();
  await rail(a).waitFor({ timeout: 10000 });
  const cookies = await ctxA.cookies();
  return `cookies: ${cookies.map((c) => `${c.name}${c.httpOnly ? " HttpOnly" : ""}${c.secure ? " Secure" : ""} SameSite=${c.sameSite}`).join(", ")}`;
});

await check("gallery loads with thumbnails", async () => {
  await rail(a).getByRole("link", { name: "Gallery" }).click();
  await a.getByRole("list", { name: "Artworks" }).waitFor({ timeout: 10000 });
  await a.waitForTimeout(1500);
  const r = await a.evaluate(() => {
    const imgs = [...document.querySelectorAll('ul[aria-label="Artworks"] img')].filter((i) => i.getBoundingClientRect().top < innerHeight);
    return { total: imgs.length, ok: imgs.filter((i) => i.complete && i.naturalWidth > 0).length };
  });
  if (!r.total || r.ok < r.total) throw new Error(`${r.ok}/${r.total} visible thumbnails loaded`);
  return `${r.ok}/${r.total} visible thumbnails`;
});

let thumbPath = "";
await check("thumbnail needs the session cookie", async () => {
  const id = await a.evaluate(async () => {
    const res = await fetch("/vigo.core.v1.LibraryService/ListArtworks", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ pageSize: 1 }),
    });
    return (await res.json()).artworks[0].id;
  });
  thumbPath = `/library/${encodeURIComponent(id)}/thumbnail`;
  const withCookie = (await a.request.get(base + thumbPath)).status();
  const anon = await browser.newContext();
  const without = (await anon.request.get(base + thumbPath)).status();
  await anon.close();
  if (withCookie !== 200 || without !== 401) throw new Error(`with cookie ${withCookie}, without ${without}`);
  return `with cookie ${withCookie}, without ${without}`;
});

await check("sign out shows the login screen", async () => {
  await rail(a).getByRole("button", { name: "Sign out" }).click();
  await a.getByRole("button", { name: "Sign in" }).waitFor({ timeout: 5000 });
  const after = (await a.request.get(base + thumbPath)).status();
  if (after !== 401) throw new Error(`thumbnail after sign-out: ${after}`);
  return `thumbnail after sign-out: ${after}`;
});

await check("wrong password, then the right one", async () => {
  await password(a).fill("not-the-password");
  await a.getByRole("button", { name: "Sign in" }).click();
  await a.getByText("That password isn't right.").waitFor({ timeout: 5000 });
  await password(a).fill(first);
  await a.getByRole("button", { name: "Sign in" }).click();
  await rail(a).waitFor({ timeout: 5000 });
});

const ctxB = await browser.newContext();
const b = await ctxB.newPage();
await check("second device signs in", async () => {
  await b.goto(`${base}/tv`);
  await password(b).fill(first);
  await b.getByRole("button", { name: "Sign in" }).click();
  await rail(b).waitFor({ timeout: 5000 });
});

await check("change password", async () => {
  await a.goto(`${base}/tv`);
  const form = a.getByRole("form", { name: "Change password" });
  await form.waitFor({ timeout: 10000 });
  await form.getByLabel("Current password", { exact: true }).fill(first);
  await form.getByLabel("New password", { exact: true }).fill(second);
  await form.getByLabel("Confirm new password", { exact: true }).fill(second);
  await form.getByRole("button", { name: "Change password" }).click();
  await a.getByText("Password changed").waitFor({ timeout: 5000 });
});

await check("the other device is signed out", async () => {
  await rail(b).getByRole("link", { name: "Gallery" }).click();
  await b.getByText("Your session ended. Sign in again to continue.").waitFor({ timeout: 10000 });
});

await check("this device stays signed in", async () => {
  await a.reload();
  await rail(a).waitFor({ timeout: 10000 });
  if (await a.getByRole("button", { name: "Sign in" }).count()) throw new Error("login screen shown");
});

await check("old password no longer works, new one does", async () => {
  // vigo-core allows 5 attempts per client, then one per 10s; this is the 6th and 7th.
  await b.waitForTimeout(11000);
  await password(b).fill(first);
  await b.getByRole("button", { name: "Sign in" }).click();
  await b.getByRole("alert").first().waitFor({ timeout: 5000 });
  const shown = (await b.getByRole("alert").allTextContents()).join(" | ");
  if (!shown.includes("That password isn't right.")) throw new Error(`login said: ${shown}`);
  await b.waitForTimeout(11000);
  await password(b).fill(second);
  await b.getByRole("button", { name: "Sign in" }).click();
  await rail(b).waitFor({ timeout: 5000 });
});

await report();
