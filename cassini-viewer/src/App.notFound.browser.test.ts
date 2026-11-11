import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import NotFoundHost from "./testing/NotFoundHost.svelte";
import type { MeetingCatalogEntry } from "./viewer/catalog";
import { StaticCatalogProvider } from "./viewer/dataProvider";
import "./app.css";

// D-885: a link to a meeting the reader's list does not have — Talk's chat,
// a Nextcloud notification — shows what the shell says about it (a share
// waiting for them, or one a sharing setting refused) instead of "Meeting not
// found". Any meeting the shell has nothing to say about keeps the card.

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let previousUrl: string;

beforeEach(() => {
  previousUrl = location.href;
  host = document.createElement("div");
  host.style.cssText = "position:fixed;inset:0";
  document.body.append(host);
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  history.replaceState(null, "", previousUrl);
});

function provider(meetings: MeetingCatalogEntry[] = [
    { id: "a", title: "Alpha fixture", dateLabel: "2026-10-20", audioPath: "a.opus" },
    { id: "b", title: "Beta fixture", dateLabel: "2026-10-20", audioPath: "b.opus" },
  ]) {
  return Object.assign(new StaticCatalogProvider(), {
    loadCatalog: vi.fn(async () => ({ version: "cassini.viewer.catalog.v1" as const, meetings })),
    loadMeetingSummary: async () => null,
    loadMeetingForEntry: () => new Promise<never>(() => {}),
    loadContextBundle: async () => "",
  });
}

function openLink(meetingId: string, explained: string[], dataProvider = provider()) {
  history.replaceState(null, "", `${location.pathname}${location.search}#meeting=${meetingId}`);
  app = mount(NotFoundHost, { target: host, props: { dataProvider, explained } });
}

it("shows the shell's explanation in place of the not-found card", async () => {
  openLink("WAITING", ["WAITING"]);
  await expect.element(page.getByTestId("shell-not-found")).toHaveTextContent("The shell explains WAITING");
  expect(host.textContent).not.toContain("Meeting not found");
});

it("opens the meeting once the list has it, through the refresh the slot hands down", async () => {
  const meetings: MeetingCatalogEntry[] = [{ id: "a", title: "Alpha fixture", dateLabel: "2026-10-20", audioPath: "a.opus" }];
  const source = provider(meetings);
  openLink("WAITING", ["WAITING"], source);
  await expect.element(page.getByTestId("shell-not-found")).toBeVisible();
  // The share is accepted: the next catalog read lists the meeting.
  meetings.push({ id: "WAITING", title: "Weekly sync", dateLabel: "2026-10-30", audioPath: "WAITING.opus" });
  await page.getByRole("button", { name: "Accept" }).click();
  await expect.poll(() => host.querySelector('[data-testid="shell-not-found"]')).toBeNull();
  // Still the meeting the link named, now listed: the view loads it.
  expect(location.hash).toBe("#meeting=WAITING");
  await expect.element(page.getByText("Weekly sync").first()).toBeVisible();
  expect(host.textContent).not.toContain("Meeting not found");
});

it("keeps the not-found card for a meeting the shell cannot explain", async () => {
  openLink("GONE", ["WAITING"]);
  await expect.element(page.getByRole("heading", { name: "Meeting not found" })).toBeVisible();
  expect(host.querySelector('[data-testid="shell-not-found"]')).toBeNull();
  // Said once, in the meeting view: the list's notice line stays out of it.
  expect(host.querySelector(".list-note")).toBeNull();
});
