import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import Settings from "./Settings.svelte";
import { formatStorageBytes } from "./operator/storageUsage";
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
  published_category: { id: "published", bytes: 983_000_000, files: 3,
    undated_bytes: 3_000_000, undated_files: 1,
    days: [{ date: "2026-09-01", bytes: 480_000_000, files: 1 }, { date: "2026-09-15", bytes: 500_000_000, files: 1 }] },
};
const forever = { forever: true };
const settings = {
  version: 3, revision: 0, schedule: { time: "03:42", timezone: "UTC" },
  recordings: forever, current: forever, logs: forever,
  history: { mode: "group", policy: forever, fine: Object.fromEntries(ids.slice(2, 6).map((id) => [id, forever])) },
};

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let fail = false;
let empty = false;
let posts = 0;
let puts = 0;
let publishedError = false;
let oldOperator = false;
let longHistory = false;

beforeEach(() => {
  fail = false;
  empty = false;
  posts = 0;
  puts = 0;
  publishedError = false;
  oldOperator = false;
  longHistory = false;
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
    return Response.json({ ...report,
      published_category: oldOperator ? undefined : longHistory ? {
        ...report.published_category,
        days: [{ ...report.published_category.days[0], date: "2020-01-01" }, report.published_category.days[1]],
      } : report.published_category,
      published_category_error: publishedError ? "Could not inspect dates" : "",
    });
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
  it("plots published dates without changing local totals or rescanning", async () => {
    app = mount(Settings, { target: host, props: { panel: "storage" } });
    await expect.element(page.getByRole("button", { name: "Recalculate", exact: true })).toBeEnabled();
    const localTotal = host.querySelector(".total-card strong")?.textContent;
    expect(localTotal).toBe(formatStorageBytes(categories.reduce((sum, row) => sum + row.bytes, 0)));
    await page.getByRole("button", { name: /Explore In Nextcloud Files:/ }).click();
    expect(barCount("published")).toBe(29);
    const bars = category("published").querySelector(".bars")!.getBoundingClientRect();
    const axis = category("published").querySelector(".x-axis")!.getBoundingClientRect();
    expect(axis.top).toBeGreaterThanOrEqual(bars.bottom);
    expect(bars.width).toBeGreaterThan(500);
    expect(category("published").querySelector(".undated")?.textContent).toContain("1 file");
    (category("published").querySelector(".values summary") as HTMLElement).click();
    expect(category("published").querySelectorAll("tbody tr")).toHaveLength(29);
    await page.getByLabelText("In Nextcloud Files time range", { exact: true }).selectOptions("7");
    expect(barCount("published")).toBe(7);
    expect(host.querySelector(".total-card strong")?.textContent).toBe(localTotal);
    expect(localTotal).not.toBe(host.querySelectorAll(".total-card strong")[1]?.textContent);
    expect(posts).toBe(1);
    expect(puts).toBe(0);
    await page.getByLabelText("In Nextcloud Files time range", { exact: true }).selectOptions("all");
    await page.elementLocator(category("published")).screenshot({ path: "../node_modules/.cache/vitest-screenshots/storage-published-desktop.png" });
    await page.viewport(390, 844);
    await page.elementLocator(category("published")).screenshot({ path: "../node_modules/.cache/vitest-screenshots/storage-published-mobile.png" });
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth);
  });

  it("scrolls automatically grouped dates on narrow screens", async () => {
    longHistory = true;
    await page.viewport(390, 844);
    app = mount(Settings, { target: host, props: { panel: "storage" } });
    await expect.element(page.getByRole("button", { name: "Recalculate", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: /Explore In Nextcloud Files:/ }).click();
    await page.getByLabelText("In Nextcloud Files time range", { exact: true }).selectOptions("custom");
    await page.getByLabelText("In Nextcloud Files from date").fill("2020-01-01");
    await page.getByLabelText("In Nextcloud Files through date").fill("2026-09-29");
    expect(barCount("published")).toBeGreaterThan(30);
    expect(barCount("published")).toBeLessThanOrEqual(60);
    const scroller = category("published").querySelector(".plot-scroll") as HTMLElement;
    expect(scroller.scrollWidth).toBeGreaterThan(scroller.clientWidth);
    scroller.focus();
    await userEvent.keyboard("{ArrowRight}");
    await expect.poll(() => scroller.scrollLeft).toBeGreaterThan(0);
    await page.getByRole("button", { name: "Scroll In Nextcloud Files later", exact: true }).click();
    await expect.poll(() => scroller.scrollLeft).toBeGreaterThan(0);
    scroller.scrollLeft = scroller.scrollWidth;
    await expect.element(page.getByRole("button", { name: "Scroll In Nextcloud Files later", exact: true })).toBeDisabled();
    const last = category("published").querySelectorAll(".bar-target");
    expect(last[last.length - 1].getAttribute("aria-label")).toContain("2026");
    await page.getByRole("button", { name: "Scroll In Nextcloud Files earlier", exact: true }).click();
    await expect.poll(() => scroller.scrollLeft + scroller.clientWidth).toBeLessThan(scroller.scrollWidth - 1);
    await page.getByLabelText("In Nextcloud Files time range", { exact: true }).selectOptions("all");
    expect(barCount("published")).toBeGreaterThan(30);
    expect(barCount("published")).toBeLessThanOrEqual(60);
    await page.viewport(390, 844);
    await page.elementLocator(category("published")).screenshot({ path: "../node_modules/.cache/vitest-screenshots/storage-scroll-mobile.png" });
    expect(posts).toBe(1);
    expect(puts).toBe(0);
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth);
  });

  it("labels partial published dates and explains older operator responses", async () => {
    publishedError = true;
    app = mount(Settings, { target: host, props: { panel: "storage" } });
    await expect.element(page.getByRole("button", { name: "Recalculate", exact: true })).toBeEnabled();
    await expect.element(page.getByText(/The Nextcloud breakdown is incomplete/)).toBeVisible();
    await page.getByRole("button", { name: /Explore In Nextcloud Files:/ }).click();
    expect(category("published").textContent).toContain("Partial result");
    oldOperator = true;
    publishedError = false;
    await page.getByRole("button", { name: "Recalculate", exact: true }).click();
    await expect.element(page.getByText(/Nextcloud date data is not available yet/)).toBeVisible();
    expect(document.getElementById("category-published")).toBeNull();
  });

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
    expect(document.querySelectorAll(".comparison-row")).toHaveLength(10);
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
    expect(document.querySelectorAll(".comparison-row")).toHaveLength(6);
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
