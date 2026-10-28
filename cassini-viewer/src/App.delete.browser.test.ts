import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import App from "./App.svelte";
import { AnnotationError } from "./viewer/annotations";
import type { MeetingCatalogEntry } from "./viewer/catalog";
import { StaticCatalogProvider } from "./viewer/dataProvider";
import "./app.css";

let app: ReturnType<typeof mount>;
let host: HTMLDivElement;
let previousBase: string | undefined;
let previousUrl: string;
let renderErrors: string[];

function captureDuplicateKey(event: ErrorEvent) {
  if (!event.message.includes("each_key_duplicate")) return;
  renderErrors.push(event.message);
  event.preventDefault();
  event.stopImmediatePropagation();
}

beforeEach(() => {
  previousBase = window.__CASSINI_VIEWER_BASE__;
  previousUrl = location.href;
  delete window.__CASSINI_VIEWER_BASE__;
  history.replaceState(null, "", location.pathname + location.search);
  renderErrors = [];
  window.addEventListener("error", captureDuplicateKey, true);
  host = document.createElement("div");
  host.style.cssText = "position:fixed;inset:0";
  document.body.append(host);
});

afterEach(async () => {
  await unmount(app);
  host.remove();
  window.removeEventListener("error", captureDuplicateKey, true);
  if (previousBase === undefined) delete window.__CASSINI_VIEWER_BASE__;
  else window.__CASSINI_VIEWER_BASE__ = previousBase;
  history.replaceState(null, "", previousUrl);
  await page.viewport(1440, 1000);
});

function fixture() {
  const meetings: MeetingCatalogEntry[] = [
    { id: "a", title: "Alpha fixture", dateLabel: "2026-10-20", audioPath: "a.opus" },
    { id: "b", title: "Beta fixture", dateLabel: "2026-10-20", audioPath: "b.opus" },
    { id: "c", title: "Untouched fixture", dateLabel: "2026-10-20", audioPath: "c.opus" },
  ];
  const provider = Object.assign(new StaticCatalogProvider(), {
    loadCatalog: vi.fn(async () => ({ version: "cassini.viewer.catalog.v1" as const, meetings })),
    loadMeetingSummary: async () => null,
    loadMeetingForEntry: () => new Promise<never>(() => {}),
    loadContextBundle: async () => "",
    deleteMeeting: vi.fn(async (_entry: MeetingCatalogEntry) => {}),
  });
  return { meetings, provider };
}

// Inert content is excluded from hit testing even when it paints above a
// dialog. Temporarily remove inert synchronously to measure visual stacking.
function paintedElementAt(x: number, y: number) {
  const inertNodes = [...host.querySelectorAll<HTMLElement>("[inert]")];
  try {
    for (const node of inertNodes) node.inert = false;
    return document.elementFromPoint(x, y);
  } finally {
    for (const node of inertNodes) node.inert = true;
  }
}

for (const width of [1440, 390]) {
  for (const entryPoint of ["list", "sheet"]) {
    it(`keeps ${entryPoint} deletion above the page at ${width}px and supports cancel/confirm`, async () => {
      await page.viewport(width, 900);
      const { provider } = fixture();
      app = mount(App, { target: host, props: { dataProvider: provider } });
      await expect.poll(() => host.querySelectorAll(".meeting-row").length).toBe(3);
      if (entryPoint === "sheet") {
        await page.getByRole("button", { name: /^Alpha fixture/ }).click();
      }
      const openDelete = () => page.getByRole("region", {
        name: entryPoint === "sheet" ? "Meeting view" : "Meeting list",
        exact: true,
      }).getByRole("button", { name: "Delete Alpha fixture", exact: true }).click();
      await openDelete();
      const dialog = page.getByRole("alertdialog");
      await expect.element(dialog.getByRole("button", { name: "Cancel", exact: true })).toHaveFocus();

      const confirm = host.querySelector(".confirm-dialog .btn-error") as HTMLElement;
      const rect = confirm.getBoundingClientRect();
      expect(confirm.contains(paintedElementAt(rect.x + rect.width / 2, rect.y + rect.height / 2))).toBe(true);
      const scrim = host.querySelector(".confirm-scrim") as HTMLElement;
      const backdrop = scrim.getBoundingClientRect();
      expect(paintedElementAt(backdrop.x + 8, backdrop.bottom - 8)).toBe(scrim);

      await userEvent.keyboard("{Escape}");
      await expect.poll(() => host.querySelector(".confirm-dialog")).toBeNull();
      expect(provider.deleteMeeting).not.toHaveBeenCalled();
      await openDelete();
      await page.getByRole("alertdialog").getByRole("button", { name: "Delete meeting", exact: true }).click();
      await expect.poll(() => provider.deleteMeeting.mock.calls.length).toBe(1);
      await expect.poll(() => host.querySelectorAll(".meeting-row").length).toBe(2);
    });
  }
}

for (const refresh of [false, true]) {
  it(`restores a failed bulk target once${refresh ? " after catalog refresh, keeping fresh metadata" : " without catalog refresh"}`, async () => {
    window.__CASSINI_VIEWER_BASE__ = location.origin + "/";
    const { meetings, provider } = fixture();
    let finishSecond!: () => void;
    provider.deleteMeeting.mockImplementation((entry) => entry.id === "a"
      ? Promise.reject(new AnnotationError(409, "busy"))
      : new Promise<void>((resolve) => { finishSecond = resolve; }));
    app = mount(App, { target: host, props: { dataProvider: provider } });
    await page.getByRole("checkbox", { name: "Select Alpha fixture", exact: true }).click();
    await page.getByRole("checkbox", { name: "Select Beta fixture", exact: true }).click();
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    await page.getByRole("alertdialog").getByRole("button", { name: "Delete 2 meetings", exact: true }).click();
    await expect.poll(() => provider.deleteMeeting.mock.calls.length).toBe(2);
    await expect.poll(() => host.querySelectorAll(".meeting-row").length).toBe(1);

    if (refresh) {
      provider.loadCatalog.mockResolvedValue({
        version: "cassini.viewer.catalog.v1",
        meetings: [{ ...meetings[0], title: "Alpha renamed" }, meetings[1], meetings[2]],
      });
      const loads = provider.loadCatalog.mock.calls.length;
      window.dispatchEvent(new Event("focus"));
      await expect.poll(() => provider.loadCatalog.mock.calls.length).toBeGreaterThan(loads);
      await expect.poll(() => host.querySelectorAll(".meeting-row").length).toBe(2);
    }
    finishSecond();
    await expect.element(page.getByRole("button", { name: "Retry delete", exact: true })).toBeVisible();
    // Let any queued render error reach the window listener before asserting.
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    expect(renderErrors).toEqual([]);
    const titles = [...host.querySelectorAll(".row-title")].map((node) => node.textContent);
    expect(titles).toHaveLength(2);
    expect(titles).toContain(refresh ? "Alpha renamed" : "Alpha fixture");
    expect(titles).toContain("Untouched fixture");

    provider.deleteMeeting.mockResolvedValue(undefined);
    await page.getByRole("button", { name: "Retry delete", exact: true }).click();
    await expect.poll(() => provider.deleteMeeting.mock.calls.length).toBe(3);
    expect(provider.deleteMeeting.mock.calls[2][0].id).toBe("a");
    await expect.poll(() => host.querySelectorAll(".meeting-row").length).toBe(1);
  });
}
