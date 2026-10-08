import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import ExportMenu from "./ExportMenu.svelte";
import PreparePanel from "./PreparePanel.svelte";
import { summarizeSelection } from "../viewer/selectionModel";
import type { MeetingCatalogEntry } from "../viewer/catalog";
import "../app.css";

let app: ReturnType<typeof mount>;
let host: HTMLDivElement;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
});
afterEach(async () => {
  await unmount(app);
  host.remove();
});

const entries: MeetingCatalogEntry[] = [
  { id: "one", title: "First meeting", dateLabel: "", audioPath: "one.opus" },
  { id: "two", title: "Second meeting", dateLabel: "", audioPath: "two.opus" },
];

function prepare(selected = entries) {
  // Keep the export pending to check busy controls without starting a download.
  const loadBundle = vi.fn(() => new Promise<string>(() => {}));
  const loadMeeting = vi.fn(() => new Promise<never>(() => {}));
  app = mount(PreparePanel, {
    target: host,
    props: { entries: selected, totals: summarizeSelection(selected), loadBundle, loadMeeting },
  });
  return { loadBundle, loadMeeting };
}

it("keeps the meeting menu's original three actions and keyboard dismissal", async () => {
  app = mount(ExportMenu, { target: host });
  const trigger = page.getByRole("button", { name: "Export", exact: true });
  await trigger.click();
  expect(host.querySelectorAll('[role="menuitem"]')).toHaveLength(3);
  await expect.element(page.getByRole("menuitem", { name: "Copy transcript", exact: true })).toHaveFocus();
  await userEvent.keyboard("{ArrowDown}");
  await expect.element(page.getByRole("menuitem", { name: "Download transcript", exact: true })).toHaveFocus();
  await userEvent.keyboard("{Escape}");
  await expect.element(trigger).toHaveFocus();
  expect(host.querySelector('[role="menu"]')).toBeNull();
});

it("downloads full context through the bundle loader and disables exports while busy", async () => {
  const { loadBundle, loadMeeting } = prepare();
  await page.getByRole("button", { name: "Export", exact: true }).click();
  expect(host.querySelectorAll('[role="menuitem"]')).toHaveLength(5);
  await expect.element(page.getByRole("menuitem", { name: "Copy full context", exact: true })).toBeVisible();
  await expect.element(page.getByRole("menuitem", { name: "Copy transcripts", exact: true })).toBeVisible();
  await expect.element(page.getByRole("menuitem", { name: "Download audio", exact: true })).toBeVisible();
  await page.getByRole("menuitem", { name: "Download full context", exact: true }).click();
  expect(loadBundle).toHaveBeenCalledOnce();
  expect(loadMeeting).not.toHaveBeenCalled();
  expect(host.querySelector('[role="menu"]')).toBeNull();
  await page.getByRole("button", { name: "Export", exact: true }).click();
  for (const item of host.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')) {
    expect(item.disabled).toBe(true);
  }
});

it("downloads transcripts through the meeting loader", async () => {
  const { loadBundle, loadMeeting } = prepare();
  await page.getByRole("button", { name: "Export", exact: true }).click();
  await page.getByRole("menuitem", { name: "Download transcripts", exact: true }).click();
  expect(loadMeeting).toHaveBeenCalledWith(entries[0]);
  expect(loadBundle).not.toHaveBeenCalled();
});

it("disables all five export actions for an empty selection", async () => {
  prepare([]);
  await page.getByRole("button", { name: "Export", exact: true }).click();
  const items = host.querySelectorAll<HTMLButtonElement>('[role="menuitem"]');
  expect(items).toHaveLength(5);
  for (const item of items) expect(item.disabled).toBe(true);
});
