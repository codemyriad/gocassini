import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { get } from "svelte/store";
import { LL, useLocale } from "cassini-viewer/i18n";
import MeetingAccessNotice from "./MeetingAccessNotice.svelte";
import type { AcceptSharesResult, MeetingAccess } from "./operator/sharingNotice";
import type { PendingRecording, RecordingNotice } from "./operator/setupHealth";
import "./app.css";

// What a meeting opened from a link (Talk's chat, a Nextcloud notification)
// says in place of "Meeting not found" when the reader cannot open it yet
// (D-885). The operator is faked behind the `accept` prop.

const SHOTS = "../node_modules/.cache/vitest-screenshots/meeting-access";
const appUrl = "https://cloud.example/index.php/apps/app_api/proxy/gocassini/control-panel/";

const weekly: PendingRecording = {
  shareIds: ["34", "35"], jobId: "J1", title: "Weekly sync", startedAt: "2026-10-30T10:00:00Z", durationSeconds: 2520,
};
const allHands: RecordingNotice = { jobId: "J4", title: "All hands", roomName: "All hands", startedAt: "2026-10-30T16:00:00Z" };
const answered = (result: Partial<AcceptSharesResult> = {}): AcceptSharesResult =>
  ({ accepted: [], failed: [], sharing: null, ...result });

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let accepted: AcceptSharesResult[];

function show(access: MeetingAccess, accept: (shareIds: string[]) => Promise<AcceptSharesResult> = async () => answered()) {
  app = mount(MeetingAccessNotice, {
    target: host,
    props: { access, appUrl, accept },
    events: { accepted: (event: CustomEvent<AcceptSharesResult>) => accepted.push(event.detail) },
  });
}

beforeEach(() => {
  accepted = [];
  host = document.createElement("div");
  host.style.cssText = "width: 520px; margin: 2rem auto;";
  document.body.append(host);
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
});

it("offers one button that accepts every share of the meeting", async () => {
  const accept = vi.fn(async (shareIds: string[]) => answered({ accepted: shareIds }));
  show({ kind: "pending", entry: weekly }, accept);
  await expect.element(page.getByRole("heading", { name: "This recording is waiting for you" })).toBeVisible();
  expect(host.textContent).toContain("Weekly sync");
  expect(host.textContent).toContain("42 min");
  expect(host.textContent).toContain("Nextcloud holds files shared with you until you accept them.");
  expect(host.textContent).not.toContain("Meeting not found");
  await page.screenshot({ element: host, path: `${SHOTS}/01-pending.png` });

  await userEvent.click(page.getByRole("button", { name: "Accept" }));
  await expect.poll(() => accept.mock.calls).toEqual([[["34", "35"]]]);
  await expect.poll(() => accepted.length).toBe(1);
});

it("says why when Nextcloud would not accept it, in its words", async () => {
  show({ kind: "pending", entry: weekly }, async () => answered({ failed: [{ shareId: "34", reason: "Not a recording waiting for you." }] }));
  await userEvent.click(page.getByRole("button", { name: "Accept" }));
  await expect.element(page.getByRole("alert")).toHaveTextContent("Not a recording waiting for you.");
  // Still told, so the shell can re-read what is waiting.
  expect(accepted.length).toBe(1);
});

it("says the operator's sentence when the request itself fails", async () => {
  show({ kind: "pending", entry: weekly }, async () => { throw new Error("Cassini is restarting."); });
  await userEvent.click(page.getByRole("button", { name: "Accept" }));
  await expect.element(page.getByRole("alert")).toHaveTextContent("Cassini is restarting.");
  expect(accepted.length).toBe(0);
});

it("tells a refused reader what the list tells them, with the link for an administrator", async () => {
  show({ kind: "refused", entry: allHands });
  await expect.element(page.getByRole("heading", { name: /^The recording in “All hands” on .* could not be shared with you\.$/ })).toBeVisible();
  expect(host.textContent).toContain("A Nextcloud sharing setting stops it.");
  await expect.element(page.getByRole("link", { name: "send them this link" })).toHaveAttribute("href", appUrl);
  expect(host.querySelector("button")).toBeNull();
  await page.screenshot({ element: host, path: `${SHOTS}/02-refused.png` });
});

it("still explains a refused meeting whose list notice was dismissed", async () => {
  show({ kind: "refused", entry: null });
  await expect.element(page.getByRole("heading", { name: "This recording could not be shared with you." })).toBeVisible();
  await expect.element(page.getByRole("link", { name: "send them this link" })).toBeVisible();
});

it("tells the starter their recording could not be published", async () => {
  show({ kind: "unpublished", entry: null });
  await expect.element(page.getByRole("heading", { name: "This recording you started could not be published." })).toBeVisible();
  expect(host.textContent).toContain("Nextcloud refused to share it with you.");
});

it("speaks the reader's language, not the one loaded first", async () => {
  const english = get(LL).app.shell.sharing_notice.send_link();
  useLocale(["fr"]);
  try {
    const t = get(LL).app.shell.sharing_notice;
    expect(t.send_link()).not.toBe(english);
    show({ kind: "refused", entry: null });
    await expect.element(page.getByRole("heading", { name: t.refused_this() })).toBeVisible();
    await expect.element(page.getByRole("link", { name: t.send_link() })).toHaveAttribute("href", appUrl);
  } finally {
    useLocale(["en"]);
  }
});
