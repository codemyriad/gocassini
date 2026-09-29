// Render real operator UI with synthetic API data; never touches real recordings.
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { createServer } from "vite";

process.chdir(fileURLToPath(new URL("..", import.meta.url)));
const server = await createServer({ server: { host: "127.0.0.1", port: 0 } });
await server.listen();
const origin = server.resolvedUrls.local[0];
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
const errors = [];
page.on("pageerror", e => errors.push(e.message));
const stamp = "2026-01-01T10:00:00Z";
const job = (id, expired) => ({
  id, source_expired: expired, provider: "nextcloud-talk", request_json: "{}",
  stage: "done", state: "succeeded", current_attempt_number: 1, rerun_count: 0,
  artifact_run_path: `/current/${id}.run`, room_name: id,
  created_at: stamp, updated_at: stamp, completed_at: stamp,
  record_started_at: stamp, record_finished_at: stamp,
  build_started_at: stamp, build_finished_at: stamp,
  seal_started_at: stamp, seal_finished_at: stamp,
  publish_started_at: stamp, publish_finished_at: stamp,
});
const jobs = [job("Expired recording", true), job("Available recording", false)];
await page.route("**/eviction-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><div id="app"></div><script type="module">
window.__CASSINI_CONFIG__ = {operatorBasePath: '/operator'};
window.EventSource = undefined;
import { mount } from '/node_modules/.vite/deps/svelte.js';
import Operator from '/src/Operator.svelte';
import '/src/app.css';
mount(Operator, {target: document.getElementById('app')});
</script></body></html>` }));
await page.route(`${origin}operator/**`, route => {
  const path = decodeURIComponent(new URL(route.request().url()).pathname);
  if (path === "/operator/jobs") return route.fulfill({ json: jobs });
  const selected = jobs.find(j => path === `/operator/jobs/${j.id}`);
  if (selected) return route.fulfill({ json: {
    job: selected, attempts: [],
    availability: { source: selected.source_expired ? "expired" : "present", output: "present", published_attempt: 1,
      rerun_blocked_reason: selected.source_expired ? "Source recording is expired; this job can no longer be rerun." : "" },
  } });
  return route.fulfill({ json: {} });
});
try {
  await page.goto(new URL("eviction-fixture", origin).href);
  const expired = page.locator(".run-card").filter({ hasText: "Expired recording" });
  const available = page.locator(".run-card").filter({ hasText: "Available recording" });
  await expired.getByText("Recording deleted", { exact: true }).waitFor();
  assert.equal(await available.getByText("Recording deleted", { exact: true }).count(), 0);
  await expired.click();
  const detail = page.locator(".op-detail-pane");
  await detail.getByText("The source recording was deleted by the retention policy. This job can no longer be rerun.", { exact: true }).waitFor();
  assert.equal(await detail.getByRole("button", { name: "Rerun", exact: true }).isDisabled(), true);
  await detail.getByText("Record (deleted)", { exact: true }).waitFor();
  const bars = detail.locator(".run-detail-card span.h-1\\.5");
  assert.match(await bars.first().getAttribute("class"), /bg-base-content\/12/);
  assert.match(await bars.nth(1).getAttribute("class"), /bg-success/);
  await available.click();
  await page.waitForFunction(() => !document.querySelector('.op-detail-pane button[title]')?.disabled);
  assert.equal(await detail.getByRole("button", { name: "Rerun", exact: true }).isDisabled(), false);
  assert.match(await bars.first().getAttribute("class"), /bg-success/);
  await expired.click();
  await page.setViewportSize({ width: 390, height: 900 });
  await detail.getByText("The source recording was deleted by the retention policy. This job can no longer be rerun.", { exact: true }).waitFor();
  assert.deepEqual(errors, []);
  console.log("Eviction UI checks passed: list badges, empty Record bar, explanation, disabled rerun, available recording, mobile detail.");
} finally { await browser.close(); await server.close(); }
