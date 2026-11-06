import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import SharingNotice from "./SharingNotice.svelte";
import { noticeRowLabel, refusedNoticeTitle, unpublishedTitle, type AcceptSharesResult, type ListedMeeting } from "./operator/sharingNotice";
import type { PendingRecording, RecordingNotice, SharingStatus } from "./operator/setupHealth";
import { reactiveProps } from "./testing/reactiveProps.svelte";
import "./app.css";

// What a reader sees in Browse about their own recording shares (D-885):
// recordings waiting for their acceptance, and recordings a sharing setting
// kept from them. The operator is faked behind the `accept` prop, as the
// shell would hand it in.

const SHOTS = "../node_modules/.cache/vitest-screenshots/sharing-notice";
const appUrl = "https://cloud.example/index.php/apps/app_api/proxy/gocassini/control-panel/";

const weekly: PendingRecording = {
  shareIds: ["34", "35"], jobId: "01K9QV7X3B2MA8ZC4D6N0RT5WE", title: "Weekly sync",
  startedAt: "2026-10-30T10:00:00Z", durationSeconds: 2520,
};
const retro: PendingRecording = {
  shareIds: ["36"], jobId: "01K9QV7X3B2MA8ZC4D6N0RT5WF", title: "Retro",
  startedAt: "2026-10-23T14:00:00Z", durationSeconds: 3900,
};
const checked = (over: Partial<SharingStatus> = {}): SharingStatus =>
  ({ checked: true, pending: [], refused: [], unpublished: [], dismissedRefused: [], dismissedUnpublished: [], ...over });
// Recordings the reader started that were not published.
const design: RecordingNotice = { jobId: "J7", title: "", roomName: "Design review", startedAt: "2026-10-31T15:00:00Z" };
const standup: RecordingNotice = { jobId: "J8", title: "", roomName: "Standup", startedAt: "2026-10-30T09:00:00Z" };
const planning: RecordingNotice = { jobId: "J9", title: "", roomName: "Planning", startedAt: "2026-10-29T11:00:00Z" };
// Published recordings a sharing setting kept from the reader: the
// customer's case, cassini only in "admin".
const allHands: RecordingNotice = { jobId: "J4", title: "All hands", roomName: "All hands", startedAt: "2026-10-30T16:00:00Z" };
const sync: RecordingNotice = { jobId: "J5", title: "Weekly sync", roomName: "Weekly sync", startedAt: "2026-10-30T10:00:00Z" };
const retroRoom: RecordingNotice = { jobId: "J6", title: "Retro", roomName: "Retro", startedAt: "2026-10-23T14:00:00Z" };

const answered = (result: Partial<AcceptSharesResult> = {}): AcceptSharesResult =>
  ({ accepted: [], failed: [], sharing: null, ...result });

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let accepted: AcceptSharesResult[];
let dismissed: string[][];

function show(
  sharing: SharingStatus | null,
  accept: (shareIds: string[]) => Promise<AcceptSharesResult> = async () => answered(),
  variant: "card" | "strip" = "card",
  extra: { dismiss?: (jobIds: string[]) => Promise<void>; meetings?: ListedMeeting[] } = {},
) {
  const props = reactiveProps({ sharing, variant, appUrl, accept, dismiss: extra.dismiss ?? (async () => {}), meetings: extra.meetings ?? [] });
  app = mount(SharingNotice, {
    target: host,
    props,
    events: {
      accepted: (event: CustomEvent<AcceptSharesResult>) => accepted.push(event.detail),
      dismissed: (event: CustomEvent<string[]>) => dismissed.push(event.detail),
    },
  });
  return props;
}

const lineOf = (element: Element | null | undefined) => element?.textContent?.replace(/\s+/g, " ").trim() ?? "";

const details = () => host.querySelector<HTMLDetailsElement>("details")!;

beforeEach(() => {
  accepted = [];
  dismissed = [];
  host = document.createElement("div");
  host.style.cssText = "width: 720px; margin: 0 auto;";
  document.body.append(host);
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
});

it("is one line with Accept all at its end, and the rows behind Show them", async () => {
  const accept = vi.fn(async (shareIds: string[]) => answered({ accepted: shareIds, sharing: checked() }));
  show(checked({ pending: [weekly, retro] }), accept);
  await expect.element(page.getByText("2 recordings are waiting for you", { exact: true })).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Accept all 2" })).toBeVisible();
  expect(host.textContent).toContain("Nextcloud holds files shared with you until you accept them.");
  // The rows wait behind the disclosure.
  expect(details().open).toBe(false);
  expect(host.querySelector<HTMLElement>('[aria-label="Accept Weekly sync"]')!.checkVisibility()).toBe(false);
  await page.screenshot({ element: host, path: `${SHOTS}/01-pending-card.png` });

  await userEvent.click(page.getByText("Show them", { exact: true }));
  await expect.element(page.getByRole("button", { name: "Accept Weekly sync" })).toBeVisible();
  expect(host.textContent).toContain("Weekly sync");
  expect(host.textContent).toContain("42 min");
  expect(host.textContent).toContain("1 h 05");
  await expect.element(page.getByText(/^Why\? Your account accepts shared files by hand/)).toBeVisible();
  expect(host.textContent).toContain("Hide them");
  await page.screenshot({ element: host, path: `${SHOTS}/02-pending-open.png` });

  // One row: both of its share ids, since a person can hold a user share and
  // a group share of the same file.
  await userEvent.click(page.getByRole("button", { name: "Accept Weekly sync" }));
  await expect.poll(() => accept.mock.calls).toEqual([[["34", "35"]]]);
  await expect.poll(() => accepted.length).toBe(1);
  expect(accepted[0].accepted).toEqual(["34", "35"]);
  expect(accepted[0].sharing).toEqual(checked());

  // Every pending id in one request.
  await userEvent.click(page.getByRole("button", { name: "Accept all 2" }));
  await expect.poll(() => accept.mock.calls.length).toBe(2);
  expect(accept.mock.calls[1]).toEqual([["34", "35", "36"]]);
});

it("spins on the row while the operator answers, and lets nothing else start meanwhile", async () => {
  let answer: (result: AcceptSharesResult) => void = () => {};
  const accept = vi.fn(() => new Promise<AcceptSharesResult>((resolve) => { answer = resolve; }));
  show(checked({ pending: [weekly, retro] }), accept);
  await userEvent.click(page.getByText("Show them", { exact: true }));
  await userEvent.click(page.getByRole("button", { name: "Accept Retro" }));
  const retroButton = page.getByRole("button", { name: "Accept Retro" });
  await expect.element(retroButton).toBeDisabled();
  expect(retroButton.element().querySelector(".loading")).not.toBeNull();
  expect(page.getByRole("button", { name: "Accept Weekly sync" }).element().querySelector(".loading")).toBeNull();
  await expect.element(page.getByRole("button", { name: "Accept all 2" })).toBeDisabled();
  answer(answered({ accepted: ["36"] }));
  await expect.element(retroButton).toBeEnabled();
  expect(retroButton.element().querySelector(".loading")).toBeNull();
});

it("keeps a refused row, with the operator's reason under it", async () => {
  const accept = vi.fn(async (shareIds: string[]) => {
    // A single Retro accept fails outright; Accept all is answered, with
    // one of Weekly sync's two shares refused by name and Retro's not
    // mentioned at all.
    if (shareIds.length === 1) throw new Error("Cassini could not reach Nextcloud.");
    return answered({ accepted: ["34"], failed: [{ shareId: "35", reason: "Wrong share ID, share does not exist" }] });
  });
  show(checked({ pending: [weekly, retro] }), accept);
  // Accept all from the collapsed line: a refusal opens the rows, because
  // that is where it is said.
  await userEvent.click(page.getByRole("button", { name: "Accept all 2" }));
  await expect.element(page.getByRole("alert").filter({ hasText: "Wrong share ID, share does not exist" })).toBeVisible();
  expect(details().open).toBe(true);
  // Retro's share is in neither list: not attempted, so nothing is said on
  // its row (the test below). Weekly sync's refusal is the one alert.
  expect(host.querySelectorAll("[role=alert]")).toHaveLength(1);
  // Still there, still offered: the shell decides what the refreshed list says.
  await expect.element(page.getByRole("button", { name: "Accept Weekly sync" })).toBeEnabled();
  // A request that failed outright says so on the row it was for, in place
  // of what was there.
  await userEvent.click(page.getByRole("button", { name: "Accept Retro" }));
  await expect.element(page.getByRole("alert").filter({ hasText: "Cassini could not reach Nextcloud." })).toBeVisible();
  expect(host.textContent).not.toContain("Nextcloud did not accept this share.");
  expect(host.querySelectorAll("[role=alert]")).toHaveLength(2);
  await page.screenshot({ element: host, path: `${SHOTS}/03-refused-rows.png` });
  // Trying again clears the old reason first.
  accept.mockImplementation(async (shareIds: string[]) => answered({ accepted: shareIds }));
  await userEvent.click(page.getByRole("button", { name: "Accept Retro" }));
  await expect.poll(() => host.querySelectorAll("[role=alert]").length).toBe(1);
});

it("leaves a share the operator did not get to as still waiting", async () => {
  // Accept all, answered for Weekly sync alone: the operator works through
  // the pending list in bounded rounds and can stop short, and a share it
  // never attempted is in neither list. That is not a refusal.
  const accept = vi.fn(async () => answered({ accepted: ["34", "35"] }));
  show(checked({ pending: [weekly, retro] }), accept);
  await userEvent.click(page.getByRole("button", { name: "Accept all 2" }));
  await expect.poll(() => accepted.length).toBe(1);
  expect(host.querySelectorAll("[role=alert]")).toHaveLength(0);
  expect(host.textContent).not.toContain("Nextcloud did not accept this share.");
  // Nothing to read, so the rows stay behind the disclosure; the shell's
  // refreshed shares decide whether Retro's row stays.
  expect(details().open).toBe(false);
  await userEvent.click(page.getByText("Show them", { exact: true }));
  await expect.element(page.getByRole("button", { name: "Accept Retro" })).toBeEnabled();
  // A share the operator names as failed, even without a reason, is refused.
  accept.mockImplementation(async () => answered({ failed: [{ shareId: "36", reason: "" }] }));
  await userEvent.click(page.getByRole("button", { name: "Accept Retro" }));
  await expect.element(page.getByRole("alert").filter({ hasText: "Nextcloud did not accept this share." })).toBeVisible();
  expect(host.querySelectorAll("[role=alert]")).toHaveLength(1);
});

it("draws the same two lines above a list that has meetings", async () => {
  show(checked({ pending: [weekly, retro] }), undefined, "strip");
  await expect.element(page.getByText("2 recordings are waiting for you", { exact: true })).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Accept all 2" })).toBeVisible();
  expect(host.querySelector(".sharing-strip")).not.toBeNull();
  expect(details().open).toBe(false);
  await userEvent.click(page.getByText("Show them", { exact: true }));
  await expect.element(page.getByRole("button", { name: "Accept Weekly sync" })).toBeVisible();
  await page.screenshot({ element: host, path: `${SHOTS}/04-strip-open.png` });
  await userEvent.click(page.getByText("Hide them", { exact: true }));
  await expect.poll(() => details().open).toBe(false);
});

it("tells a reader a sharing setting kept a recording from them, one notice each, with the link to hand on", async () => {
  const dismiss = vi.fn(async () => {});
  show(checked({ refused: [allHands, sync] }), undefined, "card", { dismiss });
  const blocks = [...host.querySelectorAll(".sharing-block")].map(lineOf);
  expect(blocks).toEqual([
    `${refusedNoticeTitle(allHands)} A Nextcloud sharing setting stops it. An administrator can fix that: send them this link.`,
    `${refusedNoticeTitle(sync)} A Nextcloud sharing setting stops it. An administrator can fix that: send them this link.`,
  ]);
  expect(blocks[0]).toContain("“All hands”");
  expect(host.querySelector("strong")?.textContent).toBe(refusedNoticeTitle(allHands));
  const link = host.querySelector<HTMLAnchorElement>("a.link")!;
  expect(link.textContent).toBe("send them this link");
  expect(link.href).toBe(appUrl);
  expect(link.title).toBe("Opening it as an administrator shows exactly what is stopping it");
  expect(host.querySelector("details")).toBeNull();
  await page.screenshot({ element: host, path: `${SHOTS}/05-refused.png` });

  await userEvent.click(page.getByRole("button", { name: `Dismiss: ${refusedNoticeTitle(sync)}` }));
  await expect.poll(() => dismiss.mock.calls).toEqual([[["J5"]]]);
  await expect.poll(() => dismissed).toEqual([["J5"]]);

  // Above a list that has meetings, each line keeps to the fact and the fix.
  await unmount(app!);
  app = undefined;
  show(checked({ refused: [allHands], unpublished: [design] }), undefined, "strip");
  expect([...host.querySelectorAll("p")].map(lineOf)).toEqual([
    `${refusedNoticeTitle(allHands)} An administrator can fix that: send them this link.`,
    `${unpublishedTitle(design)} An administrator can fix that: send them this link.`,
  ]);
});

it("groups three or more refused recordings behind the count, each dismissable", async () => {
  const dismiss = vi.fn(async () => {});
  show(checked({ refused: [allHands, sync, retroRoom] }), undefined, "card", { dismiss });
  const group = host.querySelector<HTMLDetailsElement>("details")!;
  expect(group.open).toBe(false);
  const summary = group.querySelector("summary")!;
  expect(lineOf(summary.querySelector("p"))).toBe("3 recordings could not be shared with you. A Nextcloud sharing setting stops it.");
  expect(summary.querySelector("a")).toBeNull();
  await userEvent.click(page.getByText("Show them", { exact: true }));
  expect([...group.querySelectorAll(".sharing-row p")].map(lineOf)).toEqual([allHands, sync, retroRoom].map((entry) => noticeRowLabel(entry)));
  expect(group.querySelectorAll("a.link")).toHaveLength(1);
  await page.screenshot({ element: host, path: `${SHOTS}/10-refused-grouped-open.png` });
  await userEvent.click(page.getByRole("button", { name: "Dismiss all", exact: true }));
  await expect.poll(() => dismiss.mock.calls).toEqual([[["J4", "J5", "J6"]]]);
});

it("tells a starter their recording was not published, and every block in order", async () => {
  show(checked({ pending: [weekly], refused: [allHands], unpublished: [design] }));
  await expect.element(page.getByText("1 recording is waiting for you", { exact: true })).toBeVisible();
  // The one row carries its own Accept, so the line's button is not "all".
  await expect.element(page.getByRole("button", { name: "Accept", exact: true })).toBeVisible();
  expect(host.textContent).not.toContain("Accept all");
  const blocks = [...host.querySelectorAll(".sharing-block")].map(lineOf);
  expect(blocks).toHaveLength(3);
  expect(blocks[0]).toContain("1 recording is waiting for you");
  expect(blocks[1]).toBe(`${refusedNoticeTitle(allHands)} A Nextcloud sharing setting stops it. An administrator can fix that: send them this link.`);
  expect(blocks[2]).toBe(`${unpublishedTitle(design)} Nextcloud refused to share it with you. An administrator can fix that: send them this link.`);
  await page.screenshot({ element: host, path: `${SHOTS}/06-both.png` });
});

it("drops a refused or waiting recording once the reader's list shows it, without a reload", async () => {
  // A re-share reached All hands; Weekly sync was accepted from Nextcloud's
  // own notification. Both now list, so neither is news.
  const props = show(checked({ pending: [weekly, retro], refused: [allHands, sync] }), undefined, "strip");
  props.meetings = [
    { id: "m1", audioPath: "https://cloud.example/proxy/cassini/meetings/J4.opus" },
    { id: weekly.jobId },
  ];
  await expect.poll(() => [...host.querySelectorAll(".sharing-block")].map(lineOf)).toEqual([
    expect.stringContaining("1 recording is waiting for you"),
    `${refusedNoticeTitle(sync)} An administrator can fix that: send them this link.`,
  ]);
});

it("draws nothing when the operator could not check, or there is nothing to say", async () => {
  show({ checked: false, pending: [weekly], refused: [allHands], unpublished: [design], dismissedRefused: [], dismissedUnpublished: [] });
  expect(host.textContent?.trim()).toBe("");
  await unmount(app!);
  app = undefined;
  show(checked());
  expect(host.textContent?.trim()).toBe("");
  await unmount(app!);
  app = undefined;
  show(null);
  expect(host.textContent?.trim()).toBe("");
});

it("names each recording a starter could not publish, room and time, and dismisses it", async () => {
  const dismiss = vi.fn(async () => {});
  show(checked({ unpublished: [design, standup] }), undefined, "card", { dismiss });
  const blocks = [...host.querySelectorAll(".sharing-block")].map(lineOf);
  expect(blocks).toEqual([
    `${unpublishedTitle(design)} Nextcloud refused to share it with you. An administrator can fix that: send them this link.`,
    `${unpublishedTitle(standup)} Nextcloud refused to share it with you. An administrator can fix that: send them this link.`,
  ]);
  expect(blocks[0]).toContain("“Design review”");
  // Two are two notices, not a disclosure.
  expect(host.querySelector("details")).toBeNull();
  await page.screenshot({ element: host, path: `${SHOTS}/07-unpublished-two.png` });

  await userEvent.click(page.getByRole("button", { name: `Dismiss: ${unpublishedTitle(design)}` }));
  await expect.poll(() => dismiss.mock.calls).toEqual([[["J7"]]]);
  await expect.poll(() => dismissed).toEqual([["J7"]]);
});

it("keeps a notice the operator could not dismiss, and says why", async () => {
  const dismiss = vi.fn(async (): Promise<void> => { throw new Error("Cassini could not dismiss the notice. Try again."); });
  show(checked({ unpublished: [design] }), undefined, "strip", { dismiss });
  await userEvent.click(page.getByRole("button", { name: `Dismiss: ${unpublishedTitle(design)}` }));
  await expect.element(page.getByRole("alert").filter({ hasText: "Cassini could not dismiss the notice. Try again." })).toBeVisible();
  expect(dismissed).toEqual([]);
  // Trying again clears the reason first.
  dismiss.mockImplementation(async () => {});
  await userEvent.click(page.getByRole("button", { name: `Dismiss: ${unpublishedTitle(design)}` }));
  await expect.poll(() => host.querySelectorAll("[role=alert]").length).toBe(0);
  await expect.poll(() => dismissed).toEqual([["J7"]]);
});

it("groups three or more behind the count, each dismissable, and all at once", async () => {
  const dismiss = vi.fn(async () => {});
  show(checked({ unpublished: [design, standup, planning] }), undefined, "card", { dismiss });
  const group = host.querySelector<HTMLDetailsElement>("details")!;
  expect(group.open).toBe(false);
  // The label is the count's sentence, without the link.
  const summary = group.querySelector("summary")!;
  expect(lineOf(summary.querySelector("p"))).toBe("3 recordings you started could not be published. Nextcloud refused to share them with you.");
  expect(summary.querySelector("a")).toBeNull();
  await page.screenshot({ element: host, path: `${SHOTS}/08-unpublished-grouped.png` });

  await userEvent.click(page.getByText("Show them", { exact: true }));
  expect([...group.querySelectorAll(".sharing-row p")].map(lineOf)).toEqual([design, standup, planning].map((entry) => noticeRowLabel(entry)));
  // The link to hand on is said once, under the rows.
  expect(group.querySelectorAll("a.link")).toHaveLength(1);
  await page.screenshot({ element: host, path: `${SHOTS}/09-unpublished-grouped-open.png` });

  await userEvent.click(page.getByRole("button", { name: `Dismiss ${noticeRowLabel(standup)}` }));
  await expect.poll(() => dismiss.mock.calls).toEqual([[["J8"]]]);
  await userEvent.click(page.getByRole("button", { name: "Dismiss all", exact: true }));
  await expect.poll(() => dismiss.mock.calls.length).toBe(2);
  expect(dismiss.mock.calls[1]).toEqual([["J7", "J8", "J9"]]);
  await expect.poll(() => dismissed).toEqual([["J8"], ["J7", "J8", "J9"]]);
});

it("drops a recording once the reader's list shows it, without a reload", async () => {
  const props = show(checked({ unpublished: [design, standup, planning] }), undefined, "strip");
  expect(host.querySelector("details")).not.toBeNull();
  // A rerun published Design review: the viewer's next listing carries its file.
  props.meetings = [{ id: "m1", audioPath: "https://cloud.example/proxy/cassini/meetings/J7.opus" }];
  // Two left: back to a notice each.
  await expect.poll(() => host.querySelector("details")).toBeNull();
  expect([...host.querySelectorAll(".sharing-block")].map(lineOf)).toEqual([
    `${unpublishedTitle(standup)} An administrator can fix that: send them this link.`,
    `${unpublishedTitle(planning)} An administrator can fix that: send them this link.`,
  ]);
  props.meetings = [...props.meetings, { id: "J8" }, { id: "m9", meetingPath: "./meetings/J9.json" }];
  await expect.poll(() => host.textContent?.trim()).toBe("");
});
