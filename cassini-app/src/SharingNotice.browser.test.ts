import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import SharingNotice from "./SharingNotice.svelte";
import type { AcceptSharesResult } from "./operator/sharingNotice";
import type { PendingRecording, SharingStatus } from "./operator/setupHealth";
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
  ({ checked: true, pending: [], refused: 0, refusedStarted: 0, ...over });

const answered = (result: Partial<AcceptSharesResult> = {}): AcceptSharesResult =>
  ({ accepted: [], failed: [], sharing: null, ...result });

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let accepted: AcceptSharesResult[];

function show(
  sharing: SharingStatus | null,
  accept: (shareIds: string[]) => Promise<AcceptSharesResult> = async () => answered(),
  variant: "card" | "strip" = "card",
) {
  app = mount(SharingNotice, {
    target: host,
    props: { sharing, variant, appUrl, accept },
    events: { accepted: (event: CustomEvent<AcceptSharesResult>) => accepted.push(event.detail) },
  });
}

const details = () => host.querySelector<HTMLDetailsElement>("details")!;

beforeEach(() => {
  accepted = [];
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
  // Neither accepted nor refused by name: the row says so rather than nothing.
  await expect.element(page.getByRole("alert").filter({ hasText: "Nextcloud did not accept this share." })).toBeVisible();
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

it("tells a reader a sharing setting kept recordings from them, with the link to hand on", async () => {
  show(checked({ refused: 2 }));
  await expect.element(page.getByText("2 recordings could not be shared with you.", { exact: true })).toBeVisible();
  expect(host.querySelector("p")?.textContent?.replace(/\s+/g, " ").trim())
    .toBe("2 recordings could not be shared with you. A Nextcloud sharing setting stops it. An administrator can fix that: send them this link.");
  expect(host.querySelector("strong")?.textContent).toBe("2 recordings could not be shared with you.");
  const link = host.querySelector<HTMLAnchorElement>("a.link")!;
  expect(link.textContent).toBe("send them this link");
  expect(link.href).toBe(appUrl);
  expect(link.title).toBe("Opening it as an administrator shows exactly what is stopping it");
  // No titles: nothing was shared, so nothing is theirs to be told about.
  expect(host.querySelector(".sharing-rows")).toBeNull();
  await page.screenshot({ element: host, path: `${SHOTS}/05-refused.png` });

  // Above a list that has meetings, the line keeps to the fact and the fix.
  await unmount(app!);
  app = undefined;
  show(checked({ refused: 2, refusedStarted: 1 }), undefined, "strip");
  const lines = [...host.querySelectorAll("p")].map((line) => line.textContent?.replace(/\s+/g, " ").trim());
  expect(lines).toEqual([
    "2 recordings could not be shared with you. An administrator can fix that: send them this link.",
    "1 recording you started could not be published. An administrator can fix that: send them this link.",
  ]);
});

it("tells a starter their recording was not published, and both blocks in order", async () => {
  show(checked({ pending: [weekly], refused: 1, refusedStarted: 1 }));
  await expect.element(page.getByText("1 recording is waiting for you", { exact: true })).toBeVisible();
  // The one row carries its own Accept, so the line's button is not "all".
  await expect.element(page.getByRole("button", { name: "Accept", exact: true })).toBeVisible();
  expect(host.textContent).not.toContain("Accept all");
  const blocks = [...host.querySelectorAll(".sharing-block")].map((block) => block.textContent?.replace(/\s+/g, " ").trim() ?? "");
  expect(blocks).toHaveLength(3);
  expect(blocks[0]).toContain("1 recording is waiting for you");
  expect(blocks[1]).toBe("1 recording could not be shared with you. A Nextcloud sharing setting stops it. An administrator can fix that: send them this link.");
  expect(blocks[2]).toBe("1 recording you started could not be published. Nextcloud refused to share it with you. An administrator can fix that: send them this link.");
  await page.screenshot({ element: host, path: `${SHOTS}/06-both.png` });
});

it("draws nothing when the operator could not check, or there is nothing to say", async () => {
  show({ checked: false, pending: [weekly], refused: 2, refusedStarted: 1 });
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
