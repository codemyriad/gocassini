// Real shell and shared controls against a synthetic API. No live files change.
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { createServer } from "vite";

process.chdir(fileURLToPath(new URL("..", import.meta.url)));
const defaults = () => ({ version: 3, revision: 0, schedule: { time: "02:00", timezone: "UTC" },
  recordings: { forever: true }, current: { forever: true }, logs: { forever: true },
  history: { mode: "group", fine_initialized: false, policy: { forever: true },
    fine: Object.fromEntries(["failed_capture", "failed_build", "superseded", "failed_publish"].map(k => [k, { forever: true }])) } });
const server = await createServer({ server: { host: "127.0.0.1", port: 0 } });
let browser;
try {
  await server.listen();
  const origin = server.resolvedUrls.local[0];
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
  page.setDefaultTimeout(15000);
  let saved = defaults(), admin = true, failGet = false, failPut = false, failAccount = false;
  let puts = 0, gets = 0, releaseSave;
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  const access = { ok: true, state: "ready", service_account: { exists: true }, setup: [] };
  await page.route("**/setup-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html>
    <html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><div id="app"></div>
    <script type="module">
      import { mount } from '/node_modules/.vite/deps/svelte.js';
      import App from '/src/App.svelte';
      import '/src/app.css';
      window.__CASSINI_CONFIG__ = {operatorBasePath:'/operator'};
      // An optimistic admin hint must never bypass the real API probe.
      window.OC = {isUserAdmin: () => true};
      mount(App, {target: document.getElementById('app')});
    </script></body></html>` }));
  await page.route(`${origin}operator/**`, async route => {
    const req = route.request(), path = new URL(req.url()).pathname;
    if (path === "/operator/status") return route.fulfill({ status: admin ? 200 : 403, json: admin ? { ok: true, recordings_access: access } : { error: "Forbidden" } });
    if (path === "/operator/setup") return route.fulfill({ json: { ok: true, state: "ready", mode: "direct_shares", features: { summaries: false, insights: false } } });
    if (path === "/operator/storage/retention") {
      if (req.method() === "GET") {
        gets++;
        return route.fulfill({ status: failGet ? 503 : 200, json: failGet ? { error: "Synthetic retention read failure" } : saved });
      }
      puts++;
      if (failPut) return route.fulfill({ status: 500, json: { error: "Could not persist retention settings" } });
      if (req.headers()["if-match"] !== `"${saved.revision}"`) return route.fulfill({ status: 412, json: { error: "Settings changed; reload before saving" } });
      if (releaseSave) await new Promise(resolve => { releaseSave = resolve; });
      saved = { ...req.postDataJSON(), revision: saved.revision + 1 };
      return route.fulfill({ json: saved });
    }
    if (path === "/operator/storage") return route.fulfill({ status: failAccount ? 503 : 200, json: failAccount ? { error: "Synthetic account check failure" } : access });
    if (path.startsWith("/operator/storage/usage")) return route.fulfill({ json: { measured_at: new Date().toISOString(), categories: [], published: [], directories: [] } });
    if (path === "/operator/events") return route.fulfill({ contentType: "text/event-stream", body: ": ready\n\n" });
    return route.fulfill({ json: { jobs: [], attempts: [] } });
  });
  await page.route("**/published/**", route => route.fulfill({ json: { version: 1, meetings: [] } }));
  await page.route("**/annotations/tags", route => route.fulfill({ json: { tags: [], meetings: [], revision: 0 } }));
  await page.route("**/insights", route => route.fulfill({ json: { insights: [] } }));
  const dialog = page.getByRole("dialog", { name: "Choose what Cassini keeps" });
  const source = page.getByRole("group", { name: "Source recordings", exact: true });
  const open = async (hash = "") => {
    await page.goto("about:blank");
    await page.goto(`${origin}setup-fixture${hash}`);
    await dialog.waitFor();
    await page.getByRole("button", { name: "Save and continue", exact: true }).waitFor();
  };

  await open();
  await page.getByRole("heading", { name: "Who can see recordings" }).waitFor();
  assert.equal(await page.getByRole("checkbox").count(), 4);
  assert.equal(puts, 0, "opening never writes");
  await page.getByRole("button", { name: "Set up later" }).focus();
  await page.keyboard.press("Tab");
  assert.equal(await page.getByRole("button", { name: "Check this Nextcloud again" }).evaluate(el => el === document.activeElement), true);
  await page.keyboard.press("Shift+Tab");
  assert.equal(await page.getByRole("button", { name: "Set up later" }).evaluate(el => el === document.activeElement), true);
  await source.getByRole("button", { name: "30 days", exact: true }).click();
  await page.getByRole("button", { name: "Set up later" }).click();
  await page.getByRole("alertdialog").waitFor();
  await page.getByRole("button", { name: "Stay", exact: true }).click();
  assert.equal(await source.getByRole("button", { name: "30 days", exact: true }).getAttribute("aria-pressed"), "true");
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Leave", exact: true }).click();
  await dialog.waitFor({ state: "detached" });
  assert.equal(puts, 0, "Later does not confirm or save");
  await open();
  assert.equal(await source.getByRole("checkbox").isChecked(), true);

  // A failed write or stale draft cannot dismiss the review.
  failPut = true;
  await page.getByRole("button", { name: "Save and continue" }).click();
  await page.getByRole("alert").filter({ hasText: "Could not persist" }).waitFor();
  assert.equal(await dialog.isVisible(), true);
  failPut = false;
  saved.revision++;
  await source.getByRole("button", { name: "60 days", exact: true }).click();
  await page.getByRole("button", { name: "Save and continue" }).click();
  await page.getByRole("alert").filter({ hasText: "Settings changed" }).waitFor();
  await page.getByRole("button", { name: "Reload saved settings" }).click();
  await page.getByRole("button", { name: "Leave", exact: true }).click();
  await source.getByRole("checkbox").waitFor();
  await page.waitForFunction(() => document.querySelector('#retention-policies input[type="checkbox"]')?.checked);

  await source.getByRole("button", { name: "Custom days", exact: true }).click();
  await source.getByLabel("Source recordings days").fill("0");
  const beforeInvalid = puts;
  await page.getByRole("button", { name: "Save and continue" }).click();
  assert.equal(puts, beforeInvalid, "native validation prevents invalid save");
  await source.getByLabel("Source recordings days").fill("45");
  await page.getByLabel("Policy control").selectOption("fine");
  await page.getByRole("group", { name: "Failed recordings", exact: true }).getByRole("button", { name: "7 days", exact: true }).click();
  await page.getByLabel("Sweep time", { exact: true }).fill("15:45");
  await page.getByLabel("Timezone", { exact: true }).fill("Europe/Zagreb");
  releaseSave = true;
  await page.getByRole("button", { name: "Save and continue" }).click();
  assert.equal(await page.getByRole("button", { name: "Set up later" }).isDisabled(), true);
  assert.equal(await page.getByRole("button", { name: "Check this Nextcloud again" }).isDisabled(), true);
  while (typeof releaseSave !== "function") await new Promise(resolve => setTimeout(resolve, 10));
  releaseSave(); releaseSave = null;
  await dialog.waitFor({ state: "detached" });
  assert.equal(saved.recordings.count, 45);
  assert.equal(saved.history.fine.failed_capture.count, 7);
  assert.equal(saved.schedule.timezone, "Europe/Zagreb");
  await page.goto(`${origin}setup-fixture#surface=operator&panel=storage`);
  await page.getByRole("heading", { name: "Retention policies", exact: true }).waitFor();
  assert.equal(await dialog.count(), 0, "persisted revision suppresses review");
  assert.equal(await source.getByLabel("Source recordings days").inputValue(), "45");
  assert.equal(await page.getByLabel("Policy control").inputValue(), "fine");
  assert.equal(await page.getByLabel("Sweep time", { exact: true }).inputValue(), "15:45");

  // An unchanged confirmation still persists completion.
  saved = defaults();
  failAccount = true;
  await open("#surface=operator&panel=storage");
  assert.equal(await page.getByRole("heading", { name: "Retention policies", exact: true }).count(), 1, "only one editor owns unsaved state");
  await page.getByRole("alert").filter({ hasText: "Synthetic account check failure" }).waitFor();
  await page.getByRole("button", { name: "Save and continue" }).click();
  await dialog.waitFor({ state: "detached" });
  assert.equal(saved.revision, 1);
  assert.equal(saved.recordings.forever, true);
  failAccount = false;

  // Browser-side account creation may use Nextcloud's own password dialog.
  // A health refresh after account creation must leave the retention draft intact.
  saved = defaults();
  access.ok = false;
  access.service_account = { exists: false, user: "cassini" };
  access.setup = [{ id: "account", action: "create_user", browser: true, args: { user: "cassini" } }];
  await page.route("**/ocs/v2.php/cloud/users?format=json", route => {
    access.ok = true;
    access.service_account.exists = true;
    access.setup = [];
    return route.fulfill({ json: { ocs: { meta: { statuscode: 100 }, data: {} } } });
  });
  await open();
  await page.evaluate(() => {
    window.OC.PasswordConfirmation = {
      requiresPasswordConfirmation: () => true,
      requirePasswordConfirmation: callback => {
        const host = document.createElement("div");
        host.id = "nc-password-fixture";
        host.innerHTML = '<input aria-label="Nextcloud password"><button>Confirm Nextcloud password</button>';
        document.body.append(host);
        host.querySelector("input").focus();
        host.querySelector("button").onclick = () => { host.remove(); callback(); };
      },
    };
  });
  await page.getByRole("button", { name: "Check this Nextcloud again" }).click();
  await source.getByRole("button", { name: "90 days", exact: true }).click();
  const beforeAccountGets = gets;
  await page.getByRole("button", { name: "Create recordings account", exact: true }).click();
  assert.equal(await page.getByRole("button", { name: "Save and continue" }).isDisabled(), true);
  assert.equal(await page.getByRole("button", { name: "Set up later" }).isDisabled(), true);
  await page.keyboard.press("Tab");
  assert.equal(await page.getByRole("button", { name: "Confirm Nextcloud password" }).evaluate(el => el === document.activeElement), true);
  await page.getByRole("button", { name: "Confirm Nextcloud password" }).click();
  await page.getByText("Nextcloud sharing is ready.", { exact: true }).waitFor();
  assert.equal(await source.getByRole("button", { name: "90 days", exact: true }).getAttribute("aria-pressed"), "true");
  assert.equal(gets, beforeAccountGets, "account health refresh does not reload retention");
  await page.getByRole("button", { name: "Save and continue" }).click();
  await dialog.waitFor({ state: "detached" });

  // Read failure offers recovery, never invents defaults or blocks browsing.
  failGet = true;
  await page.goto(`${origin}setup-fixture`);
  await page.getByRole("button", { name: "Review retention in Storage" }).click();
  await page.getByRole("alert").filter({ hasText: "Synthetic retention read failure" }).waitFor();
  assert.equal(await dialog.count(), 0);
  failGet = false;

  // Non-admin cannot reach the retention API even with an optimistic admin hint.
  admin = false;
  const beforeNonAdmin = gets;
  await Promise.all([
    page.waitForResponse(r => r.url().endsWith("/operator/status")),
    page.goto(`${origin}setup-fixture`),
  ]);
  await page.waitForTimeout(100);
  assert.equal(gets, beforeNonAdmin);
  assert.equal(await dialog.count(), 0);
  assert.equal(await page.getByRole("button", { name: "Operator", exact: true }).count(), 0);

  admin = true; saved = defaults();
  await open();
  const output = fileURLToPath(new URL("../../scratch/retention-setup/", import.meta.url));
  await mkdir(output, { recursive: true });
  for (const [name, width, theme] of [["desktop", 1280, "light"], ["mobile", 390, "light"], ["dark-mobile", 390, "dark"]]) {
    await page.setViewportSize({ width, height: 900 });
    await page.locator(".cassini-retention-setup").evaluate((el, theme) => el.setAttribute("data-theme", theme), theme);
    await page.locator(".setup-backdrop").evaluate(el => el.scrollTo(0, 0));
    await page.screenshot({ path: `${output}${name}.png` });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no horizontal overflow");
    assert.equal(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth), true, "dialog content fits");
  }
  assert.deepEqual(errors, []);
  console.log("Retention setup checks passed: shared controls, admin gating, save parity, unchanged confirmation, errors, stale saves, Later, focus, mobile and dark mode.");
} finally {
  await browser?.close();
  await server.close();
}
