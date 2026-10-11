import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import Settings from "./Settings.svelte";
import "./app.css";

const ids = ["recordings", "current", "failed_capture", "failed_build", "superseded", "failed_publish", "logs", "other"];
const categories = ids.map((id, index) => {
  const days = id === "other" ? [] : Array.from({ length: 45 }, (_, day) => ({
    date: new Date(Date.UTC(2026, 7, 16 + day)).toISOString().slice(0, 10),
    bytes: Math.round((Math.sin(day * 0.8) ** 2 + 0.1) * (8 - index) * 4_000_000),
    files: 3,
  }));
  const undated_bytes = id === "other" ? 8_000_000 : 1_000;
  return { id, bytes: days.reduce((sum, day) => sum + day.bytes, 0) + undated_bytes,
    files: days.length * 3 + 1, undated_bytes, undated_files: 1, days };
});
const fixture = {
  measured_at: "2026-09-29T10:00:00Z",
  published: [{ id: "published", label: "Published recordings", bytes: 983_000_000 }],
  directories: [{ id: "current", bytes: 1 }], categories,
};
const forever = { forever: true };
const settings = {
  version: 3, revision: 0, schedule: { time: "02:00", timezone: "UTC" },
  recordings: forever, current: forever, logs: forever,
  history: { mode: "group", policy: forever, fine: Object.fromEntries(ids.slice(2, 6).map((id) => [id, forever])) },
};

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let fail = false;
let empty = false;
let posts = 0;
let puts = 0;

beforeEach(() => {
  fail = false;
  empty = false;
  posts = 0;
  puts = 0;
  Object.assign(window, { __CASSINI_CONFIG__: { operatorBasePath: "/operator" } });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (init?.method === "PUT") puts += 1;
    if (path === "/operator/storage/retention") return Response.json(settings);
    expect(path).toBe("/operator/storage/usage/details");
    if (init?.method === "POST") posts += 1;
    if (fail) return Response.json({ error: "Synthetic scan failure" }, { status: 503 });
    const report = empty ? {
      ...fixture,
      categories: categories.map((category) => ({ ...category, bytes: 0, files: 0,
        undated_bytes: 0, undated_files: 0, days: [] })),
    } : fixture;
    return Response.json(report);
  }));
  host = document.createElement("div");
  host.style.maxWidth = "980px";
  host.style.margin = "auto";
  host.style.padding = "20px";
  document.body.append(host);
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  delete (window as Window & { __CASSINI_CONFIG__?: unknown }).__CASSINI_CONFIG__;
  vi.unstubAllGlobals();
  await page.viewport(1500, 1000);
});

function category(id: string) {
  return document.getElementById(`category-${id}`)!;
}

function barCount(id: string) {
  return category(id).querySelectorAll(".bar-target").length;
}

describe("storage charts in the browser", () => {
  it("keeps chart controls independent of retention writes and storage rescans", async () => {
    await page.viewport(1280, 1000);
    app = mount(Settings, { target: host, props: { panel: "storage" } });
    await expect.element(page.getByRole("button", { name: "Recalculate", exact: true })).toBeEnabled();
    await expect.poll(() => posts).toBe(1);
    await expect.element(page.getByRole("heading", { name: "Retention", exact: true })).toBeVisible();

    await expect.element(page.getByText("Last calculated", { exact: false })).toBeVisible();
    expect(page.getByRole("button", { name: "Retention policies" }).all()).toHaveLength(0);
    expect(page.getByLabelText(/precision/i).all()).toHaveLength(0);

    await page.getByRole("button", { name: /Explore Recordings:/ }).click();
    await expect.element(page.getByLabelText("Recordings time range", { exact: true })).toHaveValue("all");
    expect(barCount("recordings")).toBe(45);
    await page.getByLabelText("Recordings time range", { exact: true }).selectOptions("7");
    expect(barCount("recordings")).toBe(7);
    (category("recordings").querySelector(".bar-target") as HTMLElement).focus();
    await expect.poll(() => category("recordings").querySelector(".bar-detail")?.textContent).toMatch(/2026/);
    await page.getByText("View chart data", { exact: true }).first().click();
    expect(category("recordings").querySelectorAll("tbody tr")).toHaveLength(7);

    await page.getByLabelText("Recordings time range", { exact: true }).selectOptions("custom");
    await page.getByLabelText("Recordings from date").fill("2026-09-01");
    await page.getByLabelText("Recordings through date").fill("2026-09-07");
    expect(barCount("recordings")).toBe(7);
    await page.getByLabelText("Recordings through date").fill("2026-08-31");
    await expect.element(page.getByRole("alert")).toHaveTextContent("end date");
    // A long custom range picks a wider bar instead of exceeding the cap.
    await page.getByLabelText("Recordings from date").fill("2000-01-01");
    await page.getByLabelText("Recordings through date").fill("2026-09-07");
    expect(barCount("recordings")).toBeGreaterThan(0);
    expect(barCount("recordings")).toBeLessThanOrEqual(180);

    await page.getByRole("button", { name: /Explore Processing logs:/ }).click();
    await expect.element(page.getByLabelText("Processing logs time range", { exact: true })).toHaveValue("all");
    await expect.element(page.getByLabelText("Recordings time range", { exact: true })).toHaveValue("custom");
    const attempts = page.getByRole("button", { name: /^Previous attempts:/ });
    await expect.element(attempts).toHaveAttribute("aria-expanded", "false");
    await attempts.click();
    await expect.element(page.getByRole("button", { name: /Explore Failed processing:/ })).toBeVisible();
    expect(document.querySelectorAll(".comparison-row")).toHaveLength(9);
    await page.getByRole("button", { name: /Explore Other local files:/ }).click();
    await expect.element(page.getByText("No dated files to plot in this category.")).toBeVisible();
    expect(puts).toBe(0);
    expect(posts).toBe(1);

    await attempts.click();
    await page.getByLabelText("Recordings time range", { exact: true }).selectOptions("all");
    await page.getByText("View chart data", { exact: true }).first().click();
    window.scrollTo(0, 0);
    await page.screenshot({ path: "../node_modules/.cache/vitest-screenshots/storage-charts-desktop.png" });
    await page.viewport(390, 844);
    await page.screenshot({ path: "../node_modules/.cache/vitest-screenshots/storage-charts-mobile.png" });
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth);

    fail = true;
    await page.getByRole("button", { name: "Recalculate", exact: true }).click();
    await expect.element(page.getByText(/Couldn’t recalculate:/)).toBeVisible();
    expect(document.querySelectorAll(".comparison-row")).toHaveLength(5);
    fail = false;
    empty = true;
    await page.getByRole("button", { name: "Recalculate", exact: true }).click();
    await expect.element(page.getByText("No retained files in this category.").first()).toBeVisible();
  });

  it("opens Previous attempts on its four kinds when the saved policy sets each kind separately", async () => {
    settings.history.mode = "fine";
    try {
      app = mount(Settings, { target: host, props: { panel: "storage" } });
      await expect.element(page.getByRole("button", { name: /^Previous attempts:/ })).toHaveAttribute("aria-expanded", "true");
      await expect.element(page.getByRole("button", { name: /Explore Replaced output:/ })).toBeVisible();
    } finally { settings.history.mode = "group"; }
  });
});
