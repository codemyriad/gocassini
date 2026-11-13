import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import App from "./App.svelte";
import { claimEscape } from "./components/ui/escapeClaim";
import type { MeetingCatalogEntry } from "./viewer/catalog";
import { StaticCatalogProvider } from "./viewer/dataProvider";
import "./app.css";

let app: ReturnType<typeof mount>;
let host: HTMLDivElement;
let previousUrl: string;

// What Nextcloud's Notifications app does from its header menu: cancel every
// Escape, from the document, before Cassini sees it.
function hostCancelsEscape(event: KeyboardEvent) {
  if (event.key === "Escape") event.preventDefault();
}

beforeEach(() => {
  previousUrl = location.href;
  history.replaceState(null, "", location.pathname + location.search);
  document.addEventListener("keydown", hostCancelsEscape, true);
  host = document.createElement("div");
  host.style.cssText = "position:fixed;inset:0";
  document.body.append(host);
});

afterEach(async () => {
  await unmount(app);
  host.remove();
  document.removeEventListener("keydown", hostCancelsEscape, true);
  history.replaceState(null, "", previousUrl);
});

async function openMeeting() {
  const meetings: MeetingCatalogEntry[] = [
    { id: "a", title: "Alpha fixture", dateLabel: "2026-10-20", audioPath: "a.opus" },
    { id: "b", title: "Beta fixture", dateLabel: "2026-10-20", audioPath: "b.opus" },
  ];
  const provider = Object.assign(new StaticCatalogProvider(), {
    loadCatalog: vi.fn(async () => ({ version: "cassini.viewer.catalog.v1" as const, meetings })),
    loadMeetingSummary: async () => null,
    loadMeetingForEntry: () => new Promise<never>(() => {}),
    loadContextBundle: async () => "",
  });
  await page.viewport(1440, 900);
  app = mount(App, { target: host, props: { dataProvider: provider } });
  await expect.poll(() => host.querySelectorAll(".meeting-row").length).toBe(2);
  await page.getByRole("button", { name: /^Alpha fixture/ }).click();
  await expect.element(page.getByRole("region", { name: "Meeting view", exact: true })).toBeInTheDocument();
}

it("closes the meeting on Escape when the host page cancels every Escape", async () => {
  await openMeeting();
  await userEvent.keyboard("{Escape}");
  await expect.poll(() => host.querySelector('[aria-label="Meeting view"]')).toBeNull();
});

it("leaves the meeting open when a control inside it handled the Escape", async () => {
  await openMeeting();
  const sheet = host.querySelector<HTMLElement>('[aria-label="Meeting view"]')!;
  const control = document.createElement("button");
  sheet.append(control);
  control.addEventListener("keydown", (event) => {
    if (event.key === "Escape") claimEscape(event);
  });
  control.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, composed: true, cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 50));
  expect(host.querySelector('[aria-label="Meeting view"]')).not.toBeNull();
});
