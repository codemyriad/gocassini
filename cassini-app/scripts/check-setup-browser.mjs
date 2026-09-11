#!/usr/bin/env node
// Review demo and browser regressions against the actual app, with synthetic
// API responses. This does not test a production install, Nextcloud password
// confirmation, provisioning, migration, or recording/playback end to end.
// Run: npm run test:setup-browser -- --screenshots /tmp/cassini-setup-review

import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { createServer } from "vite";

const appRoot = fileURLToPath(new URL("..", import.meta.url));
const args = process.argv.slice(2);
assert(args.length === 0 || (args.length === 2 && args[0] === "--screenshots"),
  "Usage: node scripts/check-setup-browser.mjs [--screenshots <directory>]");
const screenshotDir = args[1] ? path.resolve(args[1]) : null;
if (screenshotDir) await mkdir(screenshotDir, { recursive: true });
// Vite's config reads process.cwd() when resolving its environment. Always
// resolve this workspace, even when invoked through npm from the repo root.
process.chdir(appRoot);

const diagnostic = "storage_mode_undecided: nobody has chosen who can see recordings";
const catalog = {
  version: "cassini.viewer.catalog.v1",
  meetings: [{
    id: "synthetic-project-kickoff",
    title: "Project kickoff (demo recording)",
    roomId: "synthetic-demo-room",
    roomName: "Demo team",
    dateLabel: "2026-09-10T09:00:00Z",
    audioPath: "./meetings/synthetic-project-kickoff.opus",
    speakerCount: 3,
    segmentCount: 72,
    digestDurationMs: 840000,
    hasSummary: false,
    wordCount: 1840,
  }, {
    id: "synthetic-weekly-planning",
    title: "Weekly planning (demo recording)",
    roomId: "synthetic-demo-room",
    roomName: "Demo team",
    dateLabel: "2026-09-09T09:00:00Z",
    audioPath: "./meetings/synthetic-weekly-planning.opus",
    speakerCount: 4,
    segmentCount: 96,
    digestDurationMs: 1260000,
    hasSummary: false,
    wordCount: 2700,
  }],
};

function storage(confirmed = false) {
  return {
    mode: confirmed ? "default" : "",
    mode_source: confirmed ? "recorded" : "",
    mode_confirmed: confirmed,
    awaiting_choice: !confirmed,
    ok: confirmed,
    state: confirmed ? "provisioned" : "unavailable",
    step: confirmed ? "" : "storage_mode_undecided",
    detail: confirmed ? "" : diagnostic,
    checked_at: "2026-09-11T09:00:00Z",
    migration_clean: true,
    service_account: { user: "cassini", known: true, exists: true, reset_occ: "occ user:resetpassword cassini" },
    modes: [
      {
        mode: "default", label: "Default", active: confirmed, available: true,
        summary: "Recordings are kept in the Cassini account's private recordings folder.",
        consequence: "Everyone who can open Cassini will be able to see all recordings and their room names.",
        root: "CassiniNoACL/Recordings",
        archive: { probed: true, present: true, meetings: 2, catalog: true },
        setup: [],
      },
      {
        mode: "access_controlled", label: "Access controlled", active: false, available: true,
        summary: "Recordings are kept in the Cassini Team folder.",
        consequence: "New recordings will be visible only to the people in each meeting.",
        root: "Cassini/Recordings",
        archive: { probed: true, present: true, meetings: 0, catalog: false },
        setup: [],
      },
    ],
  };
}

let browser;
const server = await createServer({
  root: appRoot,
  configFile: path.join(appRoot, "vite.config.ts"),
  logLevel: "error",
  server: { host: "127.0.0.1", port: 0, strictPort: false, open: false, proxy: {} },
});
const unexpectedRequests = [];
const pageErrors = [];
let checks = 0;

try {
  await server.listen();
  const address = server.httpServer.address();
  assert(address && typeof address === "object", "Vite must listen on a local TCP port");
  const origin = `http://127.0.0.1:${address.port}`;
  browser = await chromium.launch({ headless: true });

  async function scenario(name, options, run) {
    const context = await browser.newContext({
      viewport: { width: options.mobile ? 390 : 1280, height: options.mobile ? 844 : 960 },
      colorScheme: options.theme ?? "dark",
      locale: "en-GB",
      timezoneId: "UTC",
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const calls = [];
    let confirmed = false;
    let storageFailures = options.storageFailures ?? 0;
    page.on("pageerror", (error) => pageErrors.push(`${name}: ${error.stack ?? error.message}`));
    await page.addInitScript(({ theme }) => {
      window.__CASSINI_CONFIG__ = { operatorBasePath: "/operator" };
      localStorage.setItem("cassini-theme", `saturn-${theme}`);
      // Model the host capability the embedded app checks. Choosing an already
      // prepared mode must not call it. This is no claim to have exercised
      // Nextcloud's password dialog, session auth, or provisioning writes.
      window.OC = { PasswordConfirmation: {
        requirePasswordConfirmation() {
          throw new Error("The ready-mode browser fixture must not ask Nextcloud to provision anything");
        },
      } };
    }, { theme: options.theme ?? "dark" });
    await page.route("**/*", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const json = (body, status = 200) => route.fulfill({ status, json: body });
      // No live deployment is contacted. Unmocked API calls are failures; only
      // same-origin Vite source/assets may pass through to the local server.
      if (url.origin !== origin) {
        unexpectedRequests.push(`${name}: external ${request.url()}`);
        return route.abort();
      }
      if (url.pathname === "/operator/status") {
        if (options.nonAdmin) return json({ error: "Forbidden" }, 403);
        return json({
          ok: confirmed,
          recordings_access: {
            ok: confirmed,
            state: confirmed ? "provisioned" : "unavailable",
            step: confirmed ? "" : options.failure ? "unexpected_setup_step" : "storage_mode_undecided",
            detail: confirmed ? "" : options.failure ? "Synthetic setup service response: retry required" : diagnostic,
          },
        }, confirmed ? 200 : 503);
      }
      if (url.pathname === "/operator/setup") {
        if (options.legacy) return json({ error: "Not found" }, 404);
        return json({ ok: confirmed, state: confirmed ? "provisioned" : "unavailable", awaiting_choice: !confirmed && !options.failure });
      }
      if (url.pathname === "/operator/storage") {
        const method = request.method();
        const body = method === "GET" ? null : request.postDataJSON();
        calls.push({ method, body });
        if (method === "GET") {
          if (storageFailures-- > 0) return json({ error: "Synthetic missing /operator/storage route" }, 404);
          return json(storage(confirmed));
        }
        if (method === "POST" && body.action === "preview") {
          return json({ ...storage(confirmed), preview: {
            mode: body.access_control_enabled ? "access_controlled" : "default",
            ready: true,
            source_root: "CassiniNoACL/Recordings",
            destination_root: "CassiniNoACL/Recordings",
            source_readable: true,
            destination_readable: true,
            meetings: 2,
            destination_meetings: 2,
            catalog_present: true,
            adopting_destination: true,
            nothing_to_move: true,
            overwrite_required: false,
            overwrite_names: [],
            warnings: [],
          } });
        }
        if (method === "PUT") {
          assert.deepEqual(body, { access_control_enabled: false });
          confirmed = true;
          return json(storage(true));
        }
      }
      if (url.pathname.endsWith("/catalog.json")) return json(catalog);
      if (url.pathname === "/insights") return json({ insights: [] });
      if (["fetch", "xhr"].includes(request.resourceType()) || url.pathname.startsWith("/operator/")) {
        unexpectedRequests.push(`${name}: ${request.method()} ${url.pathname}`);
        return json({ error: "Browser review fixture has no route for this request" }, 500);
      }
      return route.continue();
    });
    try {
      await page.goto(origin, { waitUntil: "networkidle" });
      await run(page, calls);
      checks++;
      console.log(`PASS ${name}`);
    } finally {
      await context.close();
    }
  }

  async function visible(locator) {
    await locator.waitFor({ state: "visible" });
  }
  async function hidden(locator) {
    assert.equal(await locator.isVisible(), false, `Expected hidden: ${locator}`);
  }
  async function screenshot(page, filename) {
    if (!screenshotDir) return;
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: path.join(screenshotDir, filename), fullPage: true, animations: "disabled" });
  }
  async function noOverflow(page) {
    const widths = await page.evaluate(() => ({
      viewport: window.innerWidth,
      document: document.documentElement.scrollWidth,
      body: document.body.scrollWidth,
    }));
    assert(widths.document <= widths.viewport && widths.body <= widths.viewport,
      `Horizontal overflow: ${JSON.stringify(widths)}`);
  }
  async function invitation(page) {
    await visible(page.getByRole("heading", { name: "Finish setting up Cassini", exact: true }));
    await visible(page.getByRole("button", { name: "Choose recording access", exact: true }));
    const notice = page.getByRole("status").filter({ hasText: "Finish setting up Cassini" });
    assert.equal(await notice.locator(".text-warning").count(), 0, "Choice invitation must not use a warning icon");
    assert.equal(await notice.getAttribute("class").then((value) => value.includes("border-warning")), false);
    await hidden(page.getByText(diagnostic, { exact: true }));
    await visible(page.getByText("Project kickoff (demo recording)", { exact: true }));
  }
  async function openWizard(page) {
    await page.getByRole("button", { name: "Choose recording access", exact: true }).click();
    assert.equal(new URL(page.url()).hash, "#surface=setup");
    await visible(page.getByRole("heading", { name: "Who should be able to see recordings?", exact: true }));
    await visible(page.getByRole("heading", { name: "Everyone in Cassini", exact: true }));
    await visible(page.getByRole("heading", { name: "Meeting participants", exact: true }));
    assert.equal(await page.getByRole("button", { name: "Choose recording access", exact: true }).count(), 0,
      "Setup should show the wizard's next action without repeating its entry invitation");
  }

  await scenario("admin choice, preserved archive, preview then explicit confirmation", {}, async (page, calls) => {
    await invitation(page);
    await screenshot(page, "notice-dark.png");
    await page.getByText("Technical details", { exact: true }).click();
    await visible(page.getByText(diagnostic, { exact: true }));
    await page.getByText("Technical details", { exact: true }).click();
    await openWizard(page);
    await screenshot(page, "wizard.png");
    const card = page.getByRole("radiogroup").locator(":scope > div").filter({ has: page.getByRole("heading", { name: "Everyone in Cassini", exact: true }) });
    await card.getByRole("radio", { name: "Choose this option", exact: true }).click();
    await visible(page.getByText("The existing archive will be kept. Nothing will be overwritten, copied, or removed.", { exact: true }));
    assert.deepEqual(calls.filter((call) => call.method === "POST"), [{ method: "POST", body: { action: "preview", access_control_enabled: false } }]);
    assert.equal(calls.filter((call) => call.method === "PUT").length, 0, "Selecting a mode must not commit it");
    await page.getByRole("button", { name: "Confirm recording access", exact: true }).click();
    await visible(page.getByRole("heading", { name: "Recording storage", exact: true }));
    await visible(page.getByRole("heading", { name: "Everyone in Cassini", exact: true }));
    await visible(page.getByRole("heading", { name: "Meeting participants", exact: true }));
    await hidden(page.getByRole("heading", { name: "Who should be able to see recordings?", exact: true }));
    assert.equal(calls.filter((call) => call.method === "PUT").length, 1);
    await page.getByRole("button", { name: "Browse", exact: true }).click();
    await visible(page.getByText("Project kickoff (demo recording)", { exact: true }));
    await hidden(page.getByRole("heading", { name: "Finish setting up Cassini", exact: true }));
  });

  await scenario("light theme invitation", { theme: "light" }, async (page) => {
    await invitation(page);
    await screenshot(page, "notice-light.png");
  });

  await scenario("manual recovery commands stay inside disclosure", { failure: true }, async (page) => {
    await visible(page.getByRole("heading", { name: "Cassini is not set up yet", exact: true }));
    await visible(page.getByRole("button", { name: "Continue setup", exact: true }));
    const commands = page.locator("pre").filter({ hasText: "occ app_api:app:disable gocassini" });
    await hidden(commands);
    await page.getByText("Technical details", { exact: true }).click();
    await visible(commands);
    assert.match(await commands.innerText(), /occ app_api:app:enable gocassini/);
  });

  await scenario("missing setup service explains and recovers with retry", { storageFailures: 1 }, async (page) => {
    await invitation(page);
    await page.getByRole("button", { name: "Choose recording access", exact: true }).click();
    await visible(page.getByRole("heading", { name: "We couldn’t open setup", exact: true }));
    assert.equal(await page.getByRole("button", { name: "Choose recording access", exact: true }).count(), 0,
      "Recovery should offer Try again without repeating the setup entry invitation");
    await visible(page.getByText("The setup service couldn’t be found.", { exact: false }));
    await hidden(page.getByText("HTTP 404: Synthetic missing /operator/storage route", { exact: true }));
    await screenshot(page, "setup-retry.png");
    await page.getByRole("button", { name: "Try again", exact: true }).click();
    await visible(page.getByRole("heading", { name: "Who should be able to see recordings?", exact: true }));
    await visible(page.getByRole("heading", { name: "Everyone in Cassini", exact: true }));
    await hidden(page.getByRole("heading", { name: "We couldn’t open setup", exact: true }));
  });

  await scenario("legacy setup route falls back to status decision", { legacy: true }, async (page) => {
    await invitation(page);
    await openWizard(page);
  });

  await scenario("non-admin gets reassurance without admin controls or diagnostics", { nonAdmin: true }, async (page) => {
    await visible(page.getByRole("heading", { name: "Finish setting up Cassini", exact: true }));
    await visible(page.getByText("There is nothing for you to fix.", { exact: false }));
    await visible(page.getByText("Project kickoff (demo recording)", { exact: true }));
    assert.equal(await page.getByRole("navigation", { name: "Cassini surfaces" }).count(), 0);
    assert.equal(await page.getByRole("button", { name: "Choose recording access", exact: true }).count(), 0);
    assert.equal(await page.getByText("Technical details", { exact: true }).count(), 0);
    assert.equal(await page.getByText(diagnostic, { exact: true }).count(), 0);
  });

  await scenario("390px invitation and recovery fit viewport", { mobile: true, storageFailures: 1 }, async (page) => {
    await invitation(page);
    await noOverflow(page);
    await screenshot(page, "notice-mobile.png");
    await page.getByRole("button", { name: "Choose recording access", exact: true }).click();
    await visible(page.getByRole("heading", { name: "We couldn’t open setup", exact: true }));
    await noOverflow(page);
    const recovery = page.locator("section").filter({ has: page.getByRole("heading", { name: "We couldn’t open setup", exact: true }) });
    await recovery.getByText("Technical details", { exact: true }).click();
    await visible(recovery.locator("pre"));
    await noOverflow(page);
  });

  assert.deepEqual(unexpectedRequests, [], "All API responses must be explicitly synthetic");
  assert.deepEqual(pageErrors, [], "The browser must not report uncaught application errors");
  console.log(`${checks} setup browser scenarios passed${screenshotDir ? `; screenshots: ${screenshotDir}` : ""}.`);
} finally {
  await browser?.close();
  await server.close();
}
