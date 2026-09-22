#!/usr/bin/env node
// The first-run dialog, the audience chip and the settings section, driven in a
// real browser against synthetic API responses (D-756).
//
// It exists because the rest of this package's tests read .svelte files as
// TEXT: they can say that a sentence is in the source and that a handler is
// wired, and nothing at all about what an administrator ends up looking at. The
// bug this was written after was exactly that shape — a button that fired the
// acknowledgement and navigated away, which every source-level assertion was
// happy with and which left a fresh install unable to record with nothing left
// to say so.
//
// The mechanism is lifted from #288 (scripts/check-setup-browser.mjs, written
// against the wizard this change deletes): start Vite, answer every API call
// with a fixture, drive the real app in headless Chromium. Its guards are
// lifted with it, because they are what makes a pass mean anything:
//
//   * an API call nobody mocked fails the run — no live deployment is reached,
//     and a request the fixtures do not know about is a scenario that is not
//     testing what it says it is;
//   * an uncaught page error fails the run;
//   * OC.PasswordConfirmation is a fixture that THROWS where it must not be
//     invoked, so "this scenario writes nothing to Nextcloud" is enforced
//     rather than asserted.
//
// What it does not do: exercise a real Nextcloud, its password confirmation,
// its provisioning writes, AppAPI, a migration, or recording and playback.
//
// Run: npm run test:first-run-browser [-- --screenshots <directory>]

import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { createServer } from "vite";

const appRoot = fileURLToPath(new URL("..", import.meta.url));
const args = process.argv.slice(2);
assert(
  args.length === 0 || (args.length === 2 && args[0] === "--screenshots"),
  "Usage: node scripts/check-first-run-browser.mjs [--screenshots <directory>]",
);
const screenshotDir = args[1] ? path.resolve(args[1]) : null;
if (screenshotDir) await mkdir(screenshotDir, { recursive: true });
// Vite's config reads process.cwd() when resolving its environment. Always
// resolve this workspace, even when invoked through npm from the repo root.
process.chdir(appRoot);

// --- what the operator would say --------------------------------------------

const ACCOUNT_CAUSE =
  "Nextcloud is not letting the cassini account write to its files. This usually means the " +
  "account was removed or its group changed.";
const ACCOUNT_STEP = "owner_account";

const catalog = {
  version: "cassini.viewer.catalog.v1",
  meetings: [
    {
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
    },
    {
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
    },
  ],
};

// The two account steps a fresh install's plan carries, in the operator's own
// order: the group before the account that joins it.
const ACCOUNT_PLAN = [
  {
    id: "group",
    action: "create_group",
    title: "Create the cassini group",
    args: { group: "cassini" },
    browser: true,
    occ: "occ group:add cassini",
    app_url: "",
  },
  {
    id: "user",
    action: "create_user",
    title: "Create the cassini account",
    args: { user: "cassini", group: "cassini", display_name: "Cassini" },
    browser: true,
    occ: "occ user:add --group=cassini cassini",
    app_url: "",
  },
];

function storage({ accountExists, firstRun, healthy }) {
  return {
    mode: "default",
    mode_source: "resolved_on_enable",
    mode_confirmed: true,
    awaiting_choice: false,
    first_run: firstRun,
    service_account: {
      user: "cassini",
      known: true,
      exists: accountExists,
      reset_occ: "occ user:resetpassword cassini",
    },
    migration_clean: true,
    pending_cleanup: "",
    stranded_root: "",
    stranded_recordings: 0,
    ok: healthy,
    state: healthy ? "provisioned" : "unavailable",
    step: healthy ? "" : ACCOUNT_STEP,
    detail: "",
    checked_at: "2026-09-11T09:00:00Z",
    migration: null,
    transition: null,
    installs: [],
    preview: null,
    modes: [
      {
        mode: "default",
        label: "Default",
        active: true,
        available: accountExists,
        summary: "",
        consequence: "",
        blocker: "",
        step: "",
        instructions: [],
        // A plan for the account exists only while the account does not: the
        // operator recomputes it from what it can see.
        setup: accountExists ? [] : ACCOUNT_PLAN,
        root: "CassiniNoACL/Recordings",
        archive: { probed: true, present: true, meetings: 2, catalog: true },
      },
      {
        mode: "access_controlled",
        label: "Access controlled",
        active: false,
        available: false,
        summary: "",
        consequence: "",
        blocker: "",
        step: "",
        instructions: [],
        setup: [],
        root: "Cassini/Recordings",
        archive: { probed: true, present: false, meetings: 0, catalog: false },
      },
    ],
  };
}

// Real UI, synthetic migration and ACL responses. No permission writes may
// happen until the administrator selects a recording and confirms restriction.
function accessReviewFixture({ mode = "default", failReview = false, count = 1, known = true, initiallyRestricted = false } = {}) {
  const writes = [];
  let restricted = initiallyRestricted;
  let reviews = 0;
  const row = { id: "admin-only", room_name: "Admin only room", narrowable: true,
    audience: [{ type: "user", id: "admin" }], audience_digest: "captured-admin-roster" };
  const snapshot = () => {
    const state = storage({ accountExists: true, firstRun: false, healthy: true });
    state.mode = mode;
    for (const option of state.modes) {
      option.active = option.mode === mode;
      option.available = true;
      option.archive.meetings = option.active ? count : 0;
      option.archive.probed = known;
    }
    return state;
  };
  return { writes, get reviews() { return reviews; }, handle: async (route, request) => {
    const body = request.postDataJSON();
    const action = body?.action;
    if (request.method() === "PUT") {
      assert.equal(body.access_control_enabled, true);
      mode = "access_controlled";
      writes.push("switch");
    }
    if (action === "list_unrestricted") reviews++;
    if (action === "list_unrestricted" && failReview) {
      failReview = false;
      return route.fulfill({ status: 503, json: { error: "Permission check unavailable" } });
    }
    const state = snapshot();
    if (action === "restrict_meetings") {
      assert.deepEqual(body.meetings, [{ id: row.id, audience_digest: row.audience_digest }]);
      writes.push("restrict");
      restricted = true;
      state.restricted = [{ id: row.id, outcome: "restricted", grants: 1 }];
    }
    if (["list_unrestricted", "restrict_meetings"].includes(action)) {
      state.open_recordings = { recordings: restricted || count === 0 ? [] : [row], ignored: [], narrowable: restricted || count === 0 ? 0 : 1 };
    } else assert([undefined, "recheck"].includes(action), `Unexpected storage action: ${action}`);
    return route.fulfill({ json: state });
  }};
}

// --- the harness -------------------------------------------------------------

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
      colorScheme: options.theme ?? "light",
      locale: "en-GB",
      timezoneId: "UTC",
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    // What the app asked the operator to DO, in order, and what it asked
    // Nextcloud to create. Both are assertions in their own right: the bug this
    // script was written after was an acknowledgement sent at the wrong moment.
    const actions = [];
    const ncWrites = [];
    const admin = options.nonAdmin !== true;
    const healthy = options.broken !== true;
    let accountExists = options.accountExists ?? false;
    let firstRun = options.firstRun ?? true;

    page.on("pageerror", (error) => pageErrors.push(`${name}: ${error.stack ?? error.message}`));
    await page.addInitScript(
      ({ theme, admin, provisioning }) => {
        window.__CASSINI_CONFIG__ = { operatorBasePath: "/operator" };
        try {
          localStorage.setItem("cassini-theme", `saturn-${theme}`);
        } catch {
          // A context with no storage still renders; the theme is not the test.
        }
        // The host capability the embedded app checks before it offers to write
        // anything to Nextcloud. A scenario that must write nothing gets a
        // fixture that throws AND leaves a mark, so a swallowed rejection
        // cannot quietly turn the guard off.
        window.__cassiniConfirmations = 0;
        window.__cassiniForbiddenConfirmation = false;
        window.OC = {
          isUserAdmin: () => admin,
          requestToken: "synthetic-request-token",
          getRootPath: () => "",
          PasswordConfirmation: {
            requiresPasswordConfirmation: () => true,
            requirePasswordConfirmation(callback) {
              if (!provisioning) {
                window.__cassiniForbiddenConfirmation = true;
                throw new Error(
                  "This scenario must not ask Nextcloud to provision anything",
                );
              }
              window.__cassiniConfirmations += 1;
              callback();
            },
          },
        };
      },
      { theme: options.theme ?? "light", admin, provisioning: options.provisioning === true },
    );

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
      const ocs = (extra = {}) =>
        json({ ocs: { meta: { status: "ok", statuscode: 100, message: "OK" }, data: {} }, ...extra });

      // Nextcloud's own provisioning API, as the administrator's session.
      if (url.pathname.startsWith("/ocs/v2.php/cloud/")) {
        ncWrites.push(`${request.method()} ${url.pathname}`);
        if (url.pathname.startsWith("/ocs/v2.php/cloud/users")) {
          accountExists = true;
        }
        return ocs();
      }

      if (url.pathname === "/operator/status") {
        // A non-administrator is denied at the proxy, which is the whole of how
        // this app decides there is no operator surface.
        if (!admin) return json({ error: "Forbidden" }, 403);
        return json(
          {
            ok: healthy,
            version: "synthetic",
            recordings_access: {
              ok: healthy,
              state: healthy ? "provisioned" : "unavailable",
              step: healthy ? "" : ACCOUNT_STEP,
              // Empty on purpose: the operator withholds a sentence that would
              // name an account, and the app's own fallback is what an
              // administrator then reads.
              cause: "",
              detail: "",
              mode: "default",
            },
          },
          healthy ? 200 : 503,
        );
      }

      if (url.pathname === "/operator/setup") {
        return json({
          ok: healthy,
          state: healthy ? "provisioned" : "unavailable",
          awaiting_choice: false,
          mode: "default",
          cause: "",
          features: { summaries: true, insights: true },
        });
      }

      if (url.pathname === "/operator/storage" && options.storage) return options.storage(route, request);
      if (url.pathname === "/operator/storage") {
        const method = request.method();
        if (method === "GET") {
          actions.push("read");
          return json(storage({ accountExists, firstRun, healthy }));
        }
        if (method === "POST") {
          const body = request.postDataJSON();
          actions.push(body.action);
          if (body.action === "acknowledge_first_run") {
            firstRun = false;
          }
          return json(storage({ accountExists, firstRun, healthy }));
        }
        unexpectedRequests.push(`${name}: ${method} ${url.pathname}`);
        return json({ error: "This check never switches the storage mode" }, 500);
      }

      // The operator surface, which scenario (b) navigates to. Empty in every
      // dimension: this script is about the first run and the account row.
      if (url.pathname === "/operator/jobs") return json([]);
      if (url.pathname === "/operator/events") {
        // A non-stream answer ends EventSource permanently instead of leaving
        // it reconnecting behind the assertions.
        return route.fulfill({ status: 204, body: "" });
      }
      if (url.pathname === "/operator/settings") {
        return json({ quality: "balanced", device: "", search_aliases: {} });
      }
      if (url.pathname === "/operator/settings/llm") return json({});
      if (url.pathname === "/operator/settings/workflows") return json([]);

      // /operator/health since D-798 V2: the route carries host, recording and
      // archive checks, so "readiness" named a third of what it returns.
      if (options.readiness && ["/operator/health", "/operator/health/check", "/operator/talk/setup"].includes(url.pathname)) {
        return options.readiness(route, request);
      }
      // D-763's readiness checks now render inside the Publish pipeline panel,
      // above recording access, so every scenario that opens that panel reaches
      // these. Answered as a healthy, fully verified install: this check is
      // about the first-run ACCESS flow, and a readiness card reporting work to
      // do would put a second call to action on the screen it is measuring.
      if (url.pathname === "/operator/health" || url.pathname === "/operator/health/check") {
        return json({
          state: "passed",
          checks: [],
          secret_configured: true,
          secret_source: "env",
          test_room_url: "",
          test: { state: "idle", published: false },
        });
      }
      if (url.pathname === "/operator/talk/setup") {
        return json({
          state: "passed",
          checks: [],
          secret_configured: true,
          secret_source: "env",
          test_room_url: "",
          test: { state: "idle", published: false },
        });
      }

      if (url.pathname.endsWith("/catalog.json")) return json(catalog);
      if (url.pathname === "/insights") return json({ insights: [] });
      if (url.pathname === "/annotations/tags") {
        return json({ tags: [], meetings: [], coverage: { visible: 2, indexed: 2 } });
      }

      if (["fetch", "xhr"].includes(request.resourceType()) || url.pathname.startsWith("/operator/")) {
        unexpectedRequests.push(`${name}: ${request.method()} ${url.pathname}`);
        return json({ error: "This check has no fixture for that request" }, 500);
      }
      return route.continue();
    });

    try {
      await page.goto(origin, { waitUntil: "networkidle" });
      await run(page, { actions, ncWrites });
      // The password-confirmation guard, checked for every scenario rather than
      // by the ones that remember to.
      assert.equal(
        await page.evaluate(() => window.__cassiniForbiddenConfirmation),
        false,
        `${name}: Nextcloud provisioning was attempted where it must not be`,
      );
      checks++;
      console.log(`PASS ${name}`);
    } finally {
      await context.close();
    }
  }

  async function visible(locator) {
    await locator.waitFor({ state: "visible" });
  }
  async function absent(locator, why) {
    assert.equal(await locator.count(), 0, why);
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
    assert(
      widths.document <= widths.viewport && widths.body <= widths.viewport,
      `Horizontal overflow: ${JSON.stringify(widths)}`,
    );
  }

  const dialog = (page) => page.getByRole("dialog");
  // The audience, stated beside the Rooms heading as a label whose detail opens
  // on hover, focus or tap.
  const audienceChip = (page) =>
    page.getByRole("button", { name: "Visible to all users", exact: true });
  const meeting = (page) => page.getByText("Project kickoff (demo recording)", { exact: true });

  // (a) A fresh install, as the administrator who enabled the app.
  await scenario("fresh install: the dialog creates the account, then acknowledges", {
    provisioning: true,
  }, async (page, { actions, ncWrites }) => {
    await visible(dialog(page));
    await visible(page.getByRole("heading", { name: "Cassini is ready to record", exact: true }));
    // Who can see recordings, and the rooms they came from, before anything
    // about how Cassini works (11 September product decision) — asked as a
    // choice, with the audience in force already chosen, so starting keeps it.
    await visible(page.getByRole("radiogroup", { name: "Who can see recordings", exact: true }));
    assert.equal(
      await page.getByRole("radio", { name: /^Everyone with a Nextcloud account/ }).getAttribute("aria-checked"),
      "true",
      "the audience in force is the one the dialog starts with",
    );
    // The archive is behind it, not replaced by it: this dialog is a
    // disclosure, not a gate on reading what is already published.
    await visible(meeting(page));
    await screenshot(page, "first-run.png");
    // Nothing has been written yet — no plan run, and above all no
    // acknowledgement.
    assert.deepEqual(actions, ["read"], "the dialog must ask for nothing until it is answered");

    await page.getByRole("button", { name: "Create the account and start", exact: true }).click();
    await dialog(page).waitFor({ state: "detached" });

    // The group before the account that joins it, then the operator: look
    // again, and only then record that the first run happened.
    assert.deepEqual(ncWrites, [
      "POST /ocs/v2.php/cloud/groups",
      "POST /ocs/v2.php/cloud/users",
    ]);
    assert.equal(await page.evaluate(() => window.__cassiniConfirmations) >= 1, true,
      "creating the account goes through Nextcloud's own password confirmation");
    const answered = actions.indexOf("acknowledge_first_run");
    assert(answered > 0, "the first run must be acknowledged after the account is created");
    assert(actions.indexOf("recheck") < answered, "the operator must look again before the flag is set");
    assert.equal(actions.filter((action) => action === "acknowledge_first_run").length, 1);

    // And the permanent disclosure is the chip, in the words the whole product
    // uses.
    await visible(audienceChip(page));
  });

  // (b) The other audience. Choosing it changes nothing here and answers
  // nothing: the dialog hands the choice to the settings section, whose switch
  // does the work, and the first run is acknowledged only once that completes.
  await scenario("choose the other audience: nothing is acknowledged on the way out", {}, async (page, { actions, ncWrites }) => {
    await visible(dialog(page));
    await page.getByRole("radio", { name: /^Room members/ }).click();
    await page.getByRole("button", { name: "Continue in Publish pipeline", exact: true }).click();
    await dialog(page).waitFor({ state: "detached" });

    assert.deepEqual(ncWrites, [], "leaving for the settings writes nothing to Nextcloud");
    assert.equal(
      actions.includes("acknowledge_first_run"),
      false,
      "the first run must not be acknowledged by a button that created no account",
    );

    // It lands on the section it named, not on the operator's front page.
    const hash = new URL(page.url()).hash;
    assert.match(hash, /surface=operator/);
    assert.match(hash, /panel=pipeline/);
    await visible(page.getByRole("heading", { name: "Who can see recordings", exact: true }));
    // …where the account can still be made.
    await visible(page.getByText("Cassini needs a Nextcloud account to keep recordings in.", { exact: true }));
    await visible(page.getByRole("button", { name: "Create the account", exact: true }));
    await screenshot(page, "settings-account-row.png");
  });

  // (c) Everybody else, on an install with nothing wrong with it.
  await scenario("non-admin: the chip and nothing else", { nonAdmin: true, accountExists: true, firstRun: false }, async (page, { actions }) => {
    await visible(meeting(page));
    await visible(audienceChip(page));
    await absent(dialog(page), "the first-run dialog is an administrator's");
    await absent(page.getByRole("navigation", { name: "Cassini surfaces" }), "there is no operator surface for a non-administrator");
    await absent(page.getByText("Cassini can't save recordings right now", { exact: true }), "nothing is wrong with this install");
    await absent(page.getByText("Show details", { exact: true }), "a diagnosis is not a non-administrator's to read");
    assert.deepEqual(actions, [], "the ADMIN-gated storage record is never read for a non-administrator");
  });

  // (d) An install that cannot save recordings.
  await scenario("broken install: the consequence first, the diagnosis behind a disclosure", {
    broken: true,
    accountExists: false,
    firstRun: false,
  }, async (page) => {
    await visible(page.getByRole("heading", { name: "Cassini can't save recordings right now", exact: true }));
    await visible(page.getByText(ACCOUNT_CAUSE, { exact: true }));
    await screenshot(page, "broken-install.png");
    // Nothing technical before it is asked for: not the operator's step name,
    // not a command line.
    const step = page.getByText(ACCOUNT_STEP, { exact: false });
    const command = page.getByText("occ app_api:app:enable", { exact: false });
    assert.equal(await step.isVisible(), false, "the step name is not the first thing to read");
    assert.equal(await command.isVisible(), false, "the commands are not the first thing to read");

    await page.getByRole("button", { name: "Show details", exact: true }).click();
    await visible(step);
    await visible(command);
  });

  // (e) The same two surfaces at the narrowest width the product supports.
  await scenario("390px: the dialog fits", { mobile: true }, async (page) => {
    await visible(dialog(page));
    await noOverflow(page);
    await screenshot(page, "first-run-390.png");
  });

  await scenario("390px: the broken-install notice fits, open and closed", {
    mobile: true,
    broken: true,
    firstRun: false,
  }, async (page) => {
    await visible(page.getByRole("heading", { name: "Cassini can't save recordings right now", exact: true }));
    await noOverflow(page);
    await page.getByRole("button", { name: "Show details", exact: true }).click();
    await visible(page.getByText("occ app_api:app:enable", { exact: false }));
    await noOverflow(page);
    await screenshot(page, "broken-install-390.png");
  });

  // A slow background GET must not discard a save or restore the old report.
  let releasePoll;
  let sawPoll;
  const pollStarted = new Promise(resolve => { sawPoll = resolve; });
  let savedSecret = false;
  const readinessReport = configured => ({
    state: configured ? "not_verified" : "needs_action",
    checks: [{ id: "talk.discovery", state: "not_verified", code: "test_room_required", message: "Choose a test room.", action: "test_room" }],
    secret_configured: configured, secret_source: configured ? "setup" : "unset",
    test_room_url: "", test: { state: "not_started", published: false },
  });
  await scenario("recording setup: save survives an in-flight status poll", {
    firstRun: false, accountExists: true,
    readiness: async (route, request) => {
      if (request.method() === "GET") {
        await new Promise(resolve => { releasePoll = resolve; sawPoll(); });
        return route.fulfill({ json: readinessReport(false) });
      }
      if (request.method() === "PUT") {
        assert.equal(request.postDataJSON().internal_secret, "new-internal-secret");
        savedSecret = true;
        return route.fulfill({ json: readinessReport(true) });
      }
      return route.fulfill({ json: readinessReport(savedSecret) });
    },
  }, async page => {
    await page.evaluate(() => {
      window.location.hash = "surface=operator&panel=pipeline";
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await page.getByRole("button", { name: "Talk authentication", exact: true }).click();
    await page.getByLabel("Internal secret", { exact: true }).fill("new-internal-secret");
    await pollStarted;
    const saved = page.waitForResponse(response => response.url().endsWith("/talk/setup") && response.request().method() === "PUT");
    await page.getByRole("button", { name: "Save secret", exact: true }).click();
    await saved;
    await visible(page.getByText("An internal secret is saved. Enter a new value to replace it.", { exact: true }));
    const polled = page.waitForResponse(response => response.url().endsWith("/health") && response.request().method() === "GET");
    releasePoll();
    await polled;
    await page.waitForTimeout(100);
    await visible(page.getByText("An internal secret is saved. Enter a new value to replace it.", { exact: true }));
    assert.equal(savedSecret, true);
  });

  assert.deepEqual(unexpectedRequests, [], "All API responses must be explicitly synthetic");
  assert.deepEqual(pageErrors, [], "The browser must not report uncaught application errors");
  async function openPipeline(page) {
    await page.evaluate(() => {
      window.location.hash = "surface=operator&panel=pipeline";
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await visible(page.getByRole("heading", { name: "Who can see recordings", exact: true }));
  }

  const review = accessReviewFixture();
  await scenario("Room members switch reveals existing access; reload and restriction require review", {
    firstRun: false, accountExists: true, storage: review.handle,
  }, async page => {
    await openPipeline(page);
    await page.getByRole("radio", { name: /^Room members/ }).click();
    await page.getByRole("button", { name: "Switch", exact: true }).click();
    await visible(page.getByLabel("Restrict Admin only room", { exact: true }));
    assert.deepEqual(review.writes, ["switch"], "review must not apply ACLs automatically");
    await screenshot(page, "review-after-switch.png");

    await page.reload({ waitUntil: "networkidle" });
    await visible(page.getByRole("button", { name: "Review existing recordings", exact: true }));
    await visible(page.getByText("You have 1 recording. Existing recordings may still be visible to everyone. Review their access below.", { exact: true }));
    await page.getByRole("button", { name: "Review existing recordings", exact: true }).click();
    await page.getByLabel("Restrict Admin only room", { exact: true }).check();
    await page.getByRole("button", { name: "Restrict 1 recording to room members", exact: true }).click();
    assert.deepEqual(review.writes, ["switch"], "selection must wait for confirmation");
    const confirmation = page.getByRole("alertdialog", { name: "Restrict this recording?", exact: true });
    await confirmation.getByRole("button", { name: "Restrict 1 recording to room members", exact: true }).click();
    await page.getByLabel("Restrict Admin only room", { exact: true }).waitFor({ state: "detached" });
    assert.deepEqual(review.writes, ["switch", "restrict"]);
    await visible(page.getByText("No recordings to review. Ignored recordings keep their current permissions.", { exact: true }));
    await absent(page.getByRole("button", { name: "Review existing recordings", exact: true }), "completed review should dismiss the amber prompt");
    await absent(page.locator(".access-existing"), "completed review should dismiss stale review guidance");
    await page.getByRole("button", { name: "Check recording access again", exact: true }).click();
    await visible(page.getByText("No recordings to review. Ignored recordings keep their current permissions.", { exact: true }));
    assert.deepEqual(review.writes, ["switch", "restrict"]);
  });

  const failedReview = accessReviewFixture({ failReview: true });
  await scenario("failed review preserves successful mode switch and offers retry", {
    firstRun: false, accountExists: true, storage: failedReview.handle,
  }, async page => {
    await openPipeline(page);
    await page.getByRole("radio", { name: /^Room members/ }).click();
    await page.getByRole("button", { name: "Switch", exact: true }).click();
    await visible(page.getByRole("button", { name: "Try again", exact: true }));
    assert.equal(await page.getByRole("radio", { name: /^Room members/ }).getAttribute("aria-checked"), "true");
    await absent(page.getByText("No recordings to review", { exact: true }), "a failed check is not an empty review");
    assert.deepEqual(failedReview.writes, ["switch"]);
    await page.getByRole("button", { name: "Try again", exact: true }).click();
    await visible(page.getByLabel("Restrict Admin only room", { exact: true }));
    assert.deepEqual(failedReview.writes, ["switch"]);
  });

  const emptyArchive = accessReviewFixture({ count: 0 });
  await scenario("empty archive switches without prompting for an unnecessary review", {
    firstRun: false, accountExists: true, storage: emptyArchive.handle,
  }, async page => {
    await openPipeline(page);
    await page.getByRole("radio", { name: /^Room members/ }).click();
    await page.getByRole("button", { name: "Switch", exact: true }).click();
    await visible(page.getByText("Done. New recordings are visible to room members only.", { exact: true }));
    await absent(page.getByRole("button", { name: "Review existing recordings", exact: true }), "zero recordings need no review prompt");
    await absent(page.locator("summary").filter({ hasText: "Recordings visible to everyone" }), "zero recordings need no review disclosure");
    assert.equal(emptyArchive.reviews, 0);
    await page.reload({ waitUntil: "networkidle" });
    await visible(page.getByText("No recordings yet. Only room members will be able to see them.", { exact: true }));
    await absent(page.getByRole("button", { name: "Review existing recordings", exact: true }), "empty archive stays quiet on reload");
    assert.equal(emptyArchive.reviews, 0);
  });

  for (const [name, options] of [
    ["already restricted archive", { initiallyRestricted: true }],
    ["unknown archive count", { count: 0, known: false }],
  ]) {
    const fixture = accessReviewFixture({ mode: "access_controlled", ...options });
    await scenario(`${name}: offer review until a successful empty result`, {
      firstRun: false, accountExists: true, storage: fixture.handle,
    }, async page => {
      await openPipeline(page);
      await page.getByRole("button", { name: "Review existing recordings", exact: true }).click();
      await visible(page.getByText("No recordings to review. Ignored recordings keep their current permissions.", { exact: true }));
      await absent(page.getByRole("button", { name: "Review existing recordings", exact: true }), "successful empty review dismisses the warning");
      await visible(page.getByRole("button", { name: "Check recording access again", exact: true }));
      assert.equal(fixture.reviews, 1);
      assert.deepEqual(fixture.writes, []);
    });
  }

  console.log(`${checks} first-run browser scenarios passed${screenshotDir ? `; screenshots: ${screenshotDir}` : ""}.`);
} finally {
  await browser?.close();
  await server.close();
}
