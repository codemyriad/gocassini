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
  expect(row.textContent).toContain("Restrict users to only share with users in their groups");
  // Re-checked by the storage probe, like the row above it.
  expect([...row.querySelectorAll("button")].some(button => button.textContent?.trim() === "Check")).toBe(true);
  await userEvent.click(page.getByText("What to do about it").first());
  await expect.element(page.getByText(/turn off “Restrict users to only share with users in their groups”/)).toBeVisible();
  await expect.element(page.getByText(/does not need to be an administrator/)).toBeVisible();
  // Nobody can open a test recording either, so the panel does not offer one.
  const test = document.querySelector<HTMLElement>('[data-check-id="test"]');
  expect(test?.textContent).toContain("depends on Recording sharing");
  await page.screenshot({ element: document.querySelector('[aria-labelledby="doctor-title"]')!, path: `${SHOTS}/01-blocked.png` });
});

it("warns, naming the groups it can reach, when sharing is limited to group members", async () => {
  app = mount(RecordingSetup, { target: host, props: { operatorClient: createPreviewClient("sharing-restricted"), provisioningBase } });
  await expect.element(page.getByText(/^Recording sharing/)).toBeVisible();
  const row = sharingRow();
  expect(row.textContent).toContain("Needs attention");
  expect(row.textContent).toContain("only with members of “admin”");
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
  expect(row.textContent).not.toContain("What to do about it");
});
