import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import RetentionPanel from "./RetentionPanel.svelte";
import { OperatorClient } from "./operator/client";
import "./app.css";

const forever = () => ({ forever: true });
const group = (keys: string[]) => ({
  mode: "group", fine_initialized: false, policy: forever(),
  fine: Object.fromEntries(keys.map((key) => [key, forever()])),
});
const initialSettings = () => ({
  version: 4, revision: 0, schedule: { time: "02:00", timezone: "UTC" },
  recordings: forever(), nextcloud: { meetings: forever() },
  history: group(["failed_capture", "failed_build", "superseded", "failed_publish"]),
  current: forever(), logs: forever(),
});

let saved = initialSettings();
let puts = 0;
let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;

beforeEach(() => {
  saved = initialSettings();
  puts = 0;
  host = document.createElement("div");
  document.body.append(host);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path.endsWith("/operations")) return Response.json({ operations: [{ name: "old.opus", status: "delete-intent", updatedAt: "2026-09-30", error: "Recovery will retry" }], nextOffset: 1 });
    expect(path).toBe("/operator/storage/retention");
    if (init?.method === "PUT") {
      puts += 1;
      if ((init.headers as Record<string, string>)["If-Match"] !== `"${saved.revision}"`) {
        return Response.json({ error: "Settings changed; reload before saving" }, { status: 412 });
      }
      saved = { ...JSON.parse(init.body as string), revision: saved.revision + 1 };
    }
    return Response.json(saved);
  }));
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  vi.unstubAllGlobals();
  await page.viewport(1500, 1000);
});

function mountPanel() {
  app = mount(RetentionPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
}

describe("retention settings in the browser", () => {
  it("keeps edits local until Save, retains fine controls, and handles stale revisions", async () => {
    saved.revision = 1;
    mountPanel();
    await expect.element(page.getByRole("heading", { name: "Retention policies", exact: true })).toBeVisible();
    await expect.element(page.getByText("Keep forever", { exact: true }).first()).toBeVisible();
    expect(page.getByRole("checkbox").all().length).toBe(5);
    await expect.element(page.getByRole("button", { name: "Save retention settings" })).toBeDisabled();
    await expect.element(page.getByLabelText("Sweep time", { exact: true })).toHaveValue("02:00");
    await expect.element(page.getByLabelText("Timezone", { exact: true })).toHaveValue("UTC");
    await page.getByLabelText("Sweep time", { exact: true }).fill("15:45");
    await page.getByLabelText("Timezone", { exact: true }).fill("Europe/Zagreb");

    await page.getByRole("checkbox").all()[0].click();
    for (const days of [7, 30, 60, 90]) {
      const button = page.getByRole("button", { name: `${days} days`, exact: true }).first();
      await button.click();
      expect(button.element().getAttribute("aria-pressed")).toBe("true");
    }
    await page.getByRole("button", { name: "Custom days", exact: true }).first().click();
    await page.getByLabelText("Source recordings days", { exact: true }).fill("45");
    expect(page.getByText("Captured audio", { exact: true }).all()).toHaveLength(0);
    expect(page.getByText("Captured video", { exact: true }).all()).toHaveLength(0);

    await page.getByRole("checkbox").all()[1].click();
    await page.getByRole("button", { name: "60 days", exact: true }).all()[1].click();
    await page.getByRole("combobox", { name: "Policy control" }).selectOptions("fine");
    const failed = page.getByRole("group", { name: "Failed recordings", exact: true });
    expect(failed.getByRole("button", { name: "60 days", exact: true }).element().getAttribute("aria-pressed")).toBe("true");
    await failed.getByRole("button", { name: "7 days", exact: true }).click();
    await page.getByRole("combobox", { name: "Policy control" }).selectOptions("group");
    await page.getByRole("combobox", { name: "Policy control" }).selectOptions("fine");
    expect(failed.getByRole("button", { name: "7 days", exact: true }).element().getAttribute("aria-pressed")).toBe("true");
    expect(puts).toBe(0);

    await page.getByRole("button", { name: "Save retention settings" }).click();
    await expect.element(page.getByRole("status")).toBeVisible();
    expect(saved.schedule).toEqual({ time: "15:45", timezone: "Europe/Zagreb" });
    expect(saved.recordings).toMatchObject({ count: 45, unit: "days" });
    await unmount(app!);
    app = undefined;
    mountPanel();
    await expect.element(page.getByLabelText("Source recordings days")).toHaveValue(45);

    saved.revision += 1; // Another administrator saves first.
    await page.getByRole("button", { name: "90 days", exact: true }).first().click();
    await page.getByRole("button", { name: "Save retention settings" }).click();
    await expect.element(page.getByRole("alert")).toHaveTextContent("Settings changed");
    await page.getByRole("button", { name: "Reload saved settings" }).click();
    await expect.element(page.getByRole("alertdialog")).toBeVisible();
    await page.getByRole("button", { name: "Leave", exact: true }).click();
    await expect.element(page.getByRole("button", { name: "Save retention settings" })).toBeDisabled();
    await page.getByRole("button", { name: "Custom days", exact: true }).first().click();
    await page.getByLabelText("Source recordings days").fill("0");
    const previousPuts = puts;
    await page.getByRole("button", { name: "Save retention settings" }).click();
    expect(puts).toBe(previousPuts);
    await page.getByLabelText("Source recordings days").fill("1");
    await page.getByRole("button", { name: "Save retention settings" }).click();
    await expect.element(page.getByRole("status")).toBeVisible();
  });

  it("confirms unchanged defaults without a scroll gate", async () => {
    host.style.height = "3000px";
    app = mount(RetentionPanel, { target: host, props: {
      operatorClient: new OperatorClient("/operator"), initialSettings: initialSettings(),
    } });
    await expect.element(page.getByRole("button", { name: "Save retention settings" })).toBeEnabled();
    await page.getByRole("button", { name: "Save retention settings" }).click();
    expect(puts).toBe(1);
    expect(saved.revision).toBe(1);
  });

  it("saves the whole-meeting policy without preview or sweep controls", async () => {
    mountPanel();
    const meetings = page.getByRole("group", { name: "Whole meetings", exact: true });
    await expect.element(meetings).toBeVisible();
    await meetings.getByRole("checkbox").click();
    await meetings.getByRole("button", { name: "7 days", exact: true }).click();
    expect(puts).toBe(0);
    expect(page.getByRole("button", { name: /preview|sweep/i }).all()).toHaveLength(0);
    await meetings.getByRole("button", { name: "30 days", exact: true }).click();
    await page.getByRole("button", { name: "Save retention settings" }).click();
    await expect.element(page.getByText("Retention settings saved.")).toBeVisible();
    expect(saved.nextcloud).toEqual({ meetings: { forever: false, count: 30, unit: "days" } });
    await page.getByRole("button", { name: "Refresh Nextcloud operation status" }).click();
    await expect.element(page.getByText("old.opus: delete-intent (2026-09-30); Recovery will retry")).toBeVisible();
  });

  it.each([1280, 390])("has no horizontal overflow at %ipx", async (width) => {
    await page.viewport(width, 900);
    mountPanel();
    await expect.element(page.getByText("Keep forever", { exact: true }).first()).toBeVisible();
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth);
  });
});
