#!/usr/bin/env node
// Real Nextcloud session, AppAPI embedded script/CSP and published recording.
// No mocked network responses; the fixture is the just-completed Talk scenario.
import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const [summaryPath, out] = process.argv.slice(2);
assert(summaryPath && out, "Usage: check-installed-browser.mjs VALIDATOR_SUMMARY OUTPUT_DIR");
const summary = JSON.parse(await readFile(summaryPath, "utf8"));
assert.equal(summary.result, "passed");
assert(summary.last_job_id, "validator did not identify its recording");
const base = new URL(summary.nextcloud);
assert(["127.0.0.1", "localhost"].includes(base.hostname), "browser test requires a local disposable fixture");
await mkdir(out, { recursive: true });
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage();
page.setDefaultTimeout(60_000);
// Fresh Nextcloud accounts show an animated Hub welcome dialog. Wait for its
// real Close button when it obstructs an action; Escape also reaches Cassini's
// recording viewer, so it would close the very recording being tested.
await page.addLocatorHandler(page.locator(".first-run-wizard"), async (dialog) => {
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
});
const errors = [];
page.on("pageerror", (error) => errors.push(error.message));
const checks = {};
try {
  await page.goto(new URL("/login", base).href);
  await page.locator('input[name="user"]').fill("admin");
  await page.locator('input[name="password"]').fill("admin");
  await page.locator('button[type="submit"], input[type="submit"]').first().click();
  await page.waitForURL((url) => !url.pathname.includes("/login"));
  checks.login = true;
  const url = new URL("/index.php/apps/app_api/embedded/gocassini/viewer", base);
  url.hash = new URLSearchParams({ meeting: summary.last_job_id }).toString();
  const response = await page.goto(url.href);
  assert.equal(response.status(), 200);
  // Playwright locators pierce the app's open shadow root.
  // The recording must be usable before the admin acknowledges retention.
  // A meeting sheet makes shell chrome inert but does not remove the reminder.
  const reminder = page.locator(".retention-reminder");
  await reminder.waitFor({ state: "visible" });
  const retentionURL = new URL("/index.php/apps/app_api/proxy/gocassini/operator/storage/retention", base).href;
  const beforeResponse = await page.request.get(retentionURL, { headers: { "Cache-Control": "no-cache" } });
  assert.equal(beforeResponse.status(), 200);
  const before = await beforeResponse.json();
  assert.equal(before.revision, 0, "fresh installs must start with unacknowledged settings");
  assert.equal(await page.getByRole("dialog", { name: "Choose what Cassini keeps" }).count(), 0);
  await page.locator(".cassini-shell").waitFor({ state: "visible" });
  checks.embedded_app = true;
  await page.locator(".cassini-word").first().waitFor({ state: "visible" });
  checks.transcript = true;
  const audio = page.locator("audio").first();
  await audio.waitFor({ state: "attached" });
  await audio.evaluate((element) => { element.muted = true; });
  await page.getByRole("button", { name: "Play", exact: true }).click();
  await page.waitForFunction(() => {
    const find = (root) => {
      for (const e of root.querySelectorAll("*")) {
        if (e.tagName === "AUDIO" && e.currentTime > 0 && e.readyState >= 2) return true;
        if (e.shadowRoot && find(e.shadowRoot)) return true;
      }
      return false;
    };
    return find(document);
  });
  checks.recording_playback = true;
  const stillUnconfirmed = await (await page.request.get(retentionURL)).json();
  assert.equal(stillUnconfirmed.revision, 0, "playback must not acknowledge retention");
  // Navigate out of the meeting sheet through its normal Close action first.
  await page.locator('button[aria-label="Close the meeting"][title="Close (Esc)"]').click();
  await reminder.getByRole("button", { name: "Review settings", exact: true }).click();
  const [savedResponse] = await Promise.all([
    page.waitForResponse(r => r.url() === retentionURL && r.request().method() === "PUT"),
    page.getByRole("button", { name: "Save retention settings", exact: true }).click(),
  ]);
  assert.equal(savedResponse.status(), 200);
  const saved = await savedResponse.json();
  assert.deepEqual(saved, { ...before, revision: before.revision + 1 });
  await reminder.waitFor({ state: "hidden" });
  checks.retention_review = true;
  const [reloadedResponse] = await Promise.all([
    page.waitForResponse(r => r.url() === retentionURL && r.request().method() === "GET"),
    page.reload(),
  ]);
  assert.equal(reloadedResponse.status(), 200);
  assert.deepEqual(await reloadedResponse.json(), saved);
  assert.equal(await reminder.count(), 0);
  checks.retention_review_persisted = true;
  await page.goto(url.href);
  await page.locator(".cassini-word").first().waitFor({ state: "visible" });
  assert.equal(await reminder.count(), 0);
  await page.getByRole("button", { name: "Play", exact: true }).click();
  await page.waitForFunction(() => {
    const roots = [document];
    while (roots.length) {
      for (const element of roots.pop().querySelectorAll("*")) {
        if (element.tagName === "AUDIO" && element.currentTime > 0) return true;
        if (element.shadowRoot) roots.push(element.shadowRoot);
      }
    }
    return false;
  });
  checks.recording_playback_after_review = true;

  assert.deepEqual(errors, [], "uncaught browser errors");
  await page.screenshot({ path: `${out}/installed.png`, fullPage: true });
  await writeFile(`${out}/result.json`, JSON.stringify({ result: "passed", checks, job_id: summary.last_job_id }, null, 2));
} catch (error) {
  await page.screenshot({ path: `${out}/failed.png`, fullPage: true }).catch(() => {});
  await writeFile(`${out}/result.json`, JSON.stringify({ result: "failed", checks, error: String(error), page_errors: errors }, null, 2));
  throw error;
} finally {
  await browser.close();
}
