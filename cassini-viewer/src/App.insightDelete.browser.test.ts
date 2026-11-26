import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import App from "./App.svelte";
import type { MeetingCatalogEntry } from "./viewer/catalog";
import { StaticCatalogProvider } from "./viewer/dataProvider";
import type { InsightRecord } from "./viewer/insights";
import "./app.css";

// Deleting an insight (#499) and the card's "sources unavailable" pill, driven
// through the shell: the confirmation, what stays inert under it, a delete that
// is in flight, a refusal, and a listing that still carries a deleted run.

let app: ReturnType<typeof mount>;
let host: HTMLDivElement;
let previousBase: string | undefined;
let previousUrl: string;

beforeEach(() => {
  previousBase = window.__CASSINI_VIEWER_BASE__;
  previousUrl = location.href;
  delete window.__CASSINI_VIEWER_BASE__;
  history.replaceState(null, "", location.pathname + location.search);
  host = document.createElement("div");
  host.style.cssText = "position:fixed;inset:0";
  document.body.append(host);
});

afterEach(async () => {
  await unmount(app);
  host.remove();
  if (previousBase === undefined) delete window.__CASSINI_VIEWER_BASE__;
  else window.__CASSINI_VIEWER_BASE__ = previousBase;
  history.replaceState(null, "", previousUrl);
});

const insight: InsightRecord = {
  id: "ins_0123456789abcdef",
  status: "succeeded",
  createdBy: "alice",
  attemptNumber: 1,
  workflowId: "summarise",
  workflowVersion: "v0",
  workflowSha256: "abc",
  // "gone" is not in the catalog: one source unavailable.
  meetingIds: ["a", "gone"],
  roomIds: [],
  question: "",
  name: "Hiring themes",
  provider: "",
  model: "",
  documentPath: "Cassini Insights/Hiring themes.md",
  reason: "",
  error: "",
  createdAt: "2026-11-02T10:00:00Z",
  updatedAt: "2026-11-02T10:00:00Z",
};

function fixture(options: { deleteInsight?: (id: string) => Promise<void> } = {}) {
  const meetings: MeetingCatalogEntry[] = [
    { id: "a", title: "Alpha fixture", dateLabel: "2026-10-20", audioPath: "a.opus" },
  ];
  const provider = Object.assign(new StaticCatalogProvider(), {
    loadCatalog: vi.fn(async () => ({ version: "cassini.viewer.catalog.v1" as const, meetings })),
    loadMeetingSummary: async () => null,
    loadMeetingForEntry: () => new Promise<never>(() => {}),
    loadContextBundle: async () => "",
    // Stale on purpose: the operator's listing keeps answering with the run, as
    // one fetched before the delete landed would.
    listInsights: vi.fn(async () => [insight]),
    loadInsightDocument: vi.fn(async () => "# Themes\n"),
    deleteInsight: vi.fn(options.deleteInsight ?? (async (_id: string) => {})),
  });
  return { provider };
}

const card = () => host.querySelector(".insight-card");

async function openDeleteDialog() {
  host.querySelector<HTMLButtonElement>(".insight-open")?.click();
  await page.getByRole("button", { name: "Delete the insight", exact: true }).click();
  await expect.element(page.getByRole("alertdialog")).toBeVisible();
}

it("marks the card whose meeting is gone", async () => {
  const { provider } = fixture();
  app = mount(App, { target: host, props: { dataProvider: provider } });
  await expect.poll(() => card()?.textContent ?? "").toContain("1 source unavailable");
});

it("deletes behind a confirmation that holds while the request is in flight, and stays deleted", async () => {
  let finish: () => void = () => {};
  const { provider } = fixture({ deleteInsight: () => new Promise<void>((resolve) => (finish = resolve)) });
  app = mount(App, { target: host, props: { dataProvider: provider } });
  await expect.poll(() => card()).not.toBeNull();

  await openDeleteDialog();
  // The open insight is under the dialog, and nothing in it can be reached.
  expect(host.querySelector(".meeting-sheet")?.hasAttribute("inert")).toBe(true);

  await page.getByRole("alertdialog").getByRole("button", { name: "Delete insight", exact: true }).click();
  await expect.poll(() => provider.deleteInsight.mock.calls.length).toBe(1);

  // In flight: Escape and the scrim cannot pretend to back out.
  await userEvent.keyboard("{Escape}");
  host.querySelector<HTMLElement>(".confirm-scrim")?.click();
  // Give a dismissal the chance to render before asserting there was none.
  await new Promise((resolve) => setTimeout(resolve, 100));
  expect(host.querySelector(".confirm-dialog")).not.toBeNull();

  finish();
  await expect.poll(() => host.querySelector(".confirm-dialog")).toBeNull();
  await expect.poll(() => card()).toBeNull();
  // The stale listing that follows does not bring it back.
  await expect.poll(() => provider.listInsights.mock.calls.length).toBeGreaterThan(1);
  expect(card()).toBeNull();
});

it("keeps the insight and the refusal when Escape is pressed while the delete is in flight", async () => {
  let refuse: (error: Error) => void = () => {};
  const { provider } = fixture({ deleteInsight: () => new Promise<void>((_resolve, reject) => (refuse = reject)) });
  app = mount(App, { target: host, props: { dataProvider: provider } });
  await expect.poll(() => card()).not.toBeNull();

  await openDeleteDialog();
  await page.getByRole("alertdialog").getByRole("button", { name: "Delete insight", exact: true }).click();
  await expect.poll(() => provider.deleteInsight.mock.calls.length).toBe(1);

  // The dialog's buttons are disabled while it is busy, so focus is no longer
  // inside it: this Escape reaches the shell, which must not close the insight
  // under the dialog.
  await userEvent.keyboard("{Escape}");
  await new Promise((resolve) => setTimeout(resolve, 100));
  expect(host.querySelector(".confirm-dialog")).not.toBeNull();

  refuse(new Error("Cassini could not delete the insight. Its document may still be in your Files."));
  await expect.poll(() => host.querySelector(".confirm-dialog")).toBeNull();
  await expect
    .element(page.getByText("Cassini could not delete the insight. Its document may still be in your Files."))
    .toBeVisible();
});

it("treats a refused delete of a run that is no longer listed as deleted", async () => {
  // Deleted from another tab: the operator answers 404, and its listing no
  // longer carries the run.
  let gone = false;
  const { provider } = fixture({
    deleteInsight: async () => {
      gone = true;
      throw new Error("That insight is no longer there, or this deployment cannot delete insights.");
    },
  });
  provider.listInsights.mockImplementation(async () => (gone ? [] : [insight]));
  app = mount(App, { target: host, props: { dataProvider: provider } });
  await expect.poll(() => card()).not.toBeNull();

  await openDeleteDialog();
  await page.getByRole("alertdialog").getByRole("button", { name: "Delete insight", exact: true }).click();
  await expect.poll(() => host.querySelector(".confirm-dialog")).toBeNull();
  await expect.poll(() => card()).toBeNull();
  expect(host.textContent).not.toContain("That insight is no longer there");
});

it("keeps the insight open with the reason when the delete is refused", async () => {
  const { provider } = fixture({
    deleteInsight: async () => {
      throw new Error("Cassini could not delete the insight. Its document may still be in your Files.");
    },
  });
  app = mount(App, { target: host, props: { dataProvider: provider } });
  await expect.poll(() => card()).not.toBeNull();

  await openDeleteDialog();
  await page.getByRole("alertdialog").getByRole("button", { name: "Delete insight", exact: true }).click();
  await expect.poll(() => host.querySelector(".confirm-dialog")).toBeNull();
  await expect
    .element(page.getByText("Cassini could not delete the insight. Its document may still be in your Files."))
    .toBeVisible();
  expect(card()).not.toBeNull();
});
