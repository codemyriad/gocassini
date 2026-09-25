// Real Svelte controls + typed client, with an isolated synthetic API. No live
// archive is contacted and saving cannot delete any real files.
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { createServer } from "vite";

process.chdir(fileURLToPath(new URL("..", import.meta.url)));
const forever = () => ({ forever: true });
const group = keys => ({ mode: "group", fine_initialized: false, policy: forever(), fine: Object.fromEntries(keys.map(k => [k, forever()])) });
let saved = { version: 3, revision: 0, recordings: forever(), history: group(["failed_capture", "failed_build", "superseded", "failed_publish"]), current: forever(), logs: forever() };
let puts = 0;
const server = await createServer({ server: { host: "127.0.0.1", port: 0 } });
await server.listen();
const origin = server.resolvedUrls.local[0];
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage();
const errors = [];
page.on("pageerror", e => errors.push(e.message));
await page.route("**/retention-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><div id="app"></div><script type="module">
import { mount } from '/node_modules/.vite/deps/svelte.js';
import Panel from '/src/RetentionPanel.svelte';
import { OperatorClient } from '/src/operator/client.ts';
import '/src/app.css';
mount(Panel, {target: document.getElementById('app'), props: {operatorClient: new OperatorClient('/operator')}});
</script></body></html>` }));
await page.route(`${origin}operator/**`, async route => {
  assert.equal(new URL(route.request().url()).pathname, "/operator/storage/retention");
  if (route.request().method() === "PUT") {
    puts++;
    if (route.request().headers()["if-match"] !== `"${saved.revision}"`) return route.fulfill({ status: 412, contentType: "application/json", body: JSON.stringify({ error: "Settings changed; reload before saving" }) });
    const next = route.request().postDataJSON();
    saved = { ...next, revision: saved.revision + 1 };
  }
  return route.fulfill({ contentType: "application/json", body: JSON.stringify(saved) });
});
try {
  await page.goto(new URL("retention-fixture", origin).href);
  await page.getByRole("heading", { name: "Storage", exact: true }).waitFor();
  await page.getByText("Keep forever", { exact: true }).first().waitFor();
  assert.equal(await page.getByRole("checkbox").count(), 4);
  assert.equal(await page.getByRole("button", { name: "Save retention settings" }).isDisabled(), true);
  const recordings = page.locator("section").filter({ has: page.getByRole("heading", { name: "Recordings", exact: true }) }).last();
  await recordings.getByRole("checkbox").uncheck();
  for (const days of [7, 30, 60, 90]) {
    const button = recordings.getByRole("button", { name: `${days} days`, exact: true });
    await button.click();
    assert.equal(await button.getAttribute("aria-pressed"), "true");
  }
  await recordings.getByRole("button", { name: "Custom days", exact: true }).click();
  await recordings.getByLabel("Source recordings days", { exact: true }).fill("45");
  assert.equal(await recordings.getByRole("combobox", { name: "Policy control" }).count(), 0);
  assert.equal(await page.getByText("Captured audio", { exact: true }).count(), 0);
  assert.equal(await page.getByText("Captured video", { exact: true }).count(), 0);
  const history = page.locator("section").filter({ has: page.getByRole("heading", { name: "Attempt history", exact: true }) }).last();
  await history.getByRole("checkbox").uncheck();
  await history.getByRole("button", { name: "60 days", exact: true }).click();
  await history.getByRole("combobox", { name: "Policy control" }).selectOption("fine");
  const failedRecordings = history.getByRole("group", { name: "Failed recordings", exact: true });
  assert.equal(await failedRecordings.getByRole("button", { name: "60 days", exact: true }).getAttribute("aria-pressed"), "true");
  await failedRecordings.getByRole("button", { name: "7 days", exact: true }).click();
  await history.getByRole("combobox", { name: "Policy control" }).selectOption("group");
  await history.getByRole("combobox", { name: "Policy control" }).selectOption("fine");
  assert.equal(await failedRecordings.getByRole("button", { name: "7 days", exact: true }).getAttribute("aria-pressed"), "true");
  assert.equal(puts, 0, "editing must not write settings");
  await page.getByRole("button", { name: "Save retention settings" }).click();
  await page.getByRole("status").waitFor();
  await page.reload();
  assert.equal(saved.recordings.count, 45);
  assert.equal(saved.recordings.unit, "days");
  assert.equal(await recordings.getByLabel("Source recordings days").inputValue(), "45");
  saved.revision++; // another administrator saves first
  await recordings.getByRole("button", { name: "90 days", exact: true }).click();
  await page.getByRole("button", { name: "Save retention settings" }).click();
  await page.getByRole("alert").filter({ hasText: "Settings changed" }).waitFor();
  await page.getByRole("button", { name: "Reload saved settings" }).click();
  await page.getByRole("alertdialog").waitFor();
  await page.getByRole("button", { name: "Leave", exact: true }).click();
  await page.waitForFunction(() => document.querySelector('button[type="submit"]')?.disabled);
  await recordings.getByRole("button", { name: "Custom days", exact: true }).click();
  await recordings.getByLabel("Source recordings days").fill("0");
  const previousPuts = puts;
  await page.getByRole("button", { name: "Save retention settings" }).click();
  assert.equal(puts, previousPuts, "invalid count must not submit");
  await recordings.getByLabel("Source recordings days").fill("1");
  await page.getByRole("button", { name: "Save retention settings" }).click();
  await page.getByRole("status").waitFor();
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "horizontal overflow");
  }
  assert.deepEqual(errors, []);
  console.log("Retention browser checks passed: defaults, group/fine, saved values, explicit Save, reload, stale saves, unsaved guard, responsive layout.");
} finally { await browser.close(); await server.close(); }
