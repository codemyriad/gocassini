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
  await page.locator(".cassini-shell").waitFor({ state: "visible" });
  checks.embedded_app = true;
  await page.locator(".cassini-word").first().waitFor({ state: "visible" });
  checks.transcript = true;
  const audio = page.locator("audio").first();
  await audio.waitFor({ state: "attached" });
  // A fresh installed app opens its one-time setup dialog over the viewer.
  // The harness has already created the recordings account, so acknowledge
  // the participant rule before checking playback behind the dialog.
  const firstRun = page.getByRole("button", { name: "Start recording", exact: true });
  if (await firstRun.isVisible()) {
    await firstRun.click();
    await firstRun.waitFor({ state: "hidden" });
    checks.first_run_acknowledged = true;
  }
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
