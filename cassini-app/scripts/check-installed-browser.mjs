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
  // Fresh installs require retention confirmation. Exercise the real UI,
  // including the scroll gate, before testing the recording underneath it.
  const review = page.getByRole("dialog", { name: "Choose what Cassini keeps", exact: true });
  await review.waitFor({ state: "visible" });
  const retentionURL = new URL("/index.php/apps/app_api/proxy/gocassini/operator/storage/retention", base).href;
  const beforeResponse = await page.request.get(retentionURL, { headers: { "Cache-Control": "no-cache" } });
  assert.equal(beforeResponse.status(), 200);
  const before = await beforeResponse.json();
  assert.equal(before.revision, 0, "fresh installs must start with unconfirmed settings");
  const scroller = review.getByRole("region", { name: "Setup settings", exact: true });
  const confirm = review.getByRole("button", { name: "Save and continue", exact: true });
  const scrolls = await scroller.evaluate(el => el.scrollHeight > el.clientHeight);
  if (scrolls) assert.equal(await confirm.isDisabled(), true, "confirmation requires reviewing the bottom");
  await page.keyboard.press("Escape");
  assert.equal(await review.isVisible(), true, "Escape must not dismiss setup");
  const bounds = await review.boundingBox();
  await page.mouse.click(bounds.x - 5, bounds.y + 5);
  assert.equal(await review.isVisible(), true, "the backdrop must not dismiss setup");
  await page.screenshot({ path: `${out}/retention-review.png`, fullPage: true });
  const footerBefore = await confirm.boundingBox();
  await scroller.evaluate(el => el.scrollTo(0, el.scrollHeight));
  assert.equal((await confirm.boundingBox()).y, footerBefore.y, "footer must stay fixed while scrolling");
  const [savedResponse] = await Promise.all([
    page.waitForResponse(r => r.url() === retentionURL && r.request().method() === "PUT"),
    confirm.click(),
  ]);
  assert.equal(savedResponse.status(), 200, "retention confirmation must persist");
  const saved = await savedResponse.json();
  assert.equal(saved.revision, before.revision + 1);
  assert.deepEqual({ ...saved, revision: before.revision }, before, "confirmation must preserve every policy");
  await review.waitFor({ state: "hidden" });
  checks.retention_review = true;
  // Wait for the app's retention read after reload, rather than its optimistic
  // shell, to verify that the confirmation really persisted.
  const [reloadedResponse] = await Promise.all([
    page.waitForResponse(r => r.url() === retentionURL && r.request().method() === "GET"),
    page.reload(),
  ]);
  assert.equal(reloadedResponse.status(), 200);
  assert.deepEqual(await reloadedResponse.json(), saved);
  assert.equal(await review.count(), 0, "saved review must not return after reload");
  checks.retention_review_persisted = true;
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
