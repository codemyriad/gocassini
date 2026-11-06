import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it } from "vitest";
import { page, userEvent } from "vitest/browser";
import RecordingSetup from "./RecordingSetup.svelte";
import { createPreviewClient } from "./preview/doctorScenarios";
import "./app.css";

// Doctor's Recording sharing row (D-885): what an administrator sees when
// Nextcloud's sharing settings stop Cassini sharing recordings.

const SHOTS = "../node_modules/.cache/vitest-screenshots/doctor-sharing";
const provisioningBase = "https://preview.invalid/index.php/apps/app_api/proxy/gocassini/operator/";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
});

function sharingRow(): HTMLElement {
  const row = document.querySelector<HTMLElement>('[data-check-id="storage.sharing"]');
  if (!row) throw new Error("no Recording sharing row");
  return row;
}

it("names the setting and the way out when Cassini can share with nobody", async () => {
  app = mount(RecordingSetup, { target: host, props: { operatorClient: createPreviewClient("sharing-blocked"), provisioningBase } });
  await expect.element(page.getByText(/^Recording sharing/)).toBeVisible();
  const row = sharingRow();
  expect(row.textContent).toContain("Needs action");
  // Verdict, the setting by its Nextcloud name, the account, the consequence.
  expect(row.textContent).toContain("Nobody can receive recordings. Nextcloud's “Restrict users to only share with users in their groups” is on, and Cassini shares from its cassini account, which is in no group. Every share is refused, so no new meetings can be published.");
  // Re-checked by the storage probe, like the row above it.
  expect([...row.querySelectorAll("button")].some(button => button.textContent?.trim() === "Check")).toBe(true);
  await userEvent.click(page.getByText("What to do about it").first());
  await expect.element(page.getByText(/Turn off “Restrict users to only share with users in their groups” under Administration settings › Sharing\./)).toBeVisible();
  await expect.element(page.getByText(/It does not need to be an administrator\./)).toBeVisible();
  // Nothing is unshared yet: no count, no way to the runs.
  expect(row.textContent).not.toContain("Open the runs");
  // Nobody can open a test recording either, so the panel does not offer one.
  const test = document.querySelector<HTMLElement>('[data-check-id="test"]');
  expect(test?.textContent).toContain("depends on Recording sharing");
  await page.screenshot({ element: document.querySelector('[aria-labelledby="doctor-title"]')!, path: `${SHOTS}/01-blocked.png` });
});

it("warns, naming the groups it can reach, when sharing is limited to group members", async () => {
  let openedRuns = 0;
  app = mount(RecordingSetup, {
    target: host,
    props: { operatorClient: createPreviewClient("sharing-restricted"), provisioningBase },
    events: { openRuns: () => { openedRuns += 1; } },
  });
  await expect.element(page.getByText(/^Recording sharing/)).toBeVisible();
  const row = sharingRow();
  expect(row.textContent).toContain("Needs attention");
  expect(row.textContent).toContain("Only administrators can receive recordings.");
  expect(row.textContent).toContain("which is only in “admin”, the administrators' group. Anyone who is not an administrator cannot open the recordings of their meetings, and a recording they start is not published at all.");
  // What the setting has already cost, under the message, and the way to
  // the runs where each meeting is re-shared.
  expect(row.textContent).toContain("7 recordings since 12 Oct were not shared with 3 people and 1 group.");
  await userEvent.click(page.getByRole("button", { name: "Open the runs" }));
  expect(openedRuns).toBe(1);
  // A warning leaves the test's own starter reachable.
  const test = document.querySelector<HTMLElement>('[data-check-id="test"]');
  expect(test?.textContent).not.toContain("depends on Recording sharing");
  await page.screenshot({ element: document.querySelector('[aria-labelledby="doctor-title"]')!, path: `${SHOTS}/02-restricted.png` });
});

it("passes quietly with default sharing settings", async () => {
  app = mount(RecordingSetup, { target: host, props: { operatorClient: createPreviewClient("healthy"), provisioningBase } });
  await expect.element(page.getByText(/^Recording sharing/)).toBeVisible();
  const row = sharingRow();
  expect(row.textContent).toContain("Passed");
  expect(row.textContent).toContain("anyone with recordings waiting sees them in Browse.");
  expect(row.textContent).not.toContain("What to do about it");
  expect(row.textContent).not.toContain("Open the runs");
});

it("says what Nextcloud's share limit is holding back, where to re-share, and how to raise it", async () => {
  let openedRuns = 0;
  app = mount(RecordingSetup, {
    target: host,
    props: { operatorClient: createPreviewClient("sharing-share-limit"), provisioningBase },
    events: { openRuns: () => { openedRuns += 1; } },
  });
  await expect.element(page.getByText(/^Recording sharing/)).toBeVisible();
  const row = sharingRow();
  expect(row.textContent).toContain("Nextcloud's share limit held back recipients of 2 recordings since 6 Nov. Re-share each from its run, or raise the limit.");
  // Each is re-shared from its run.
  await userEvent.click(page.getByRole("button", { name: "Open the runs" }));
  expect(openedRuns).toBe(1);
  await userEvent.click(page.getByText("Raise the limit", { exact: true }));
  await expect.element(page.getByText(/files_sharing\.shareapi\.createshare user limit/)).toBeVisible();
  expect(row.textContent).toContain("raise the limit for every account's share creation");
});
