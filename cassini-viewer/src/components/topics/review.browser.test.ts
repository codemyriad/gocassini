import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import NeedsReview from "./NeedsReview.svelte";
import type { TopicQueue } from "../../viewer/topics";
import { reactiveProps } from "./reactiveProps.svelte";
import "../../app.css";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let queue: TopicQueue;
let batches: { tagId: string; meetingIds: string[]; decision: string }[];
let props: Record<string, unknown>;

const item = (meetingId: string, title: string, quote?: string) => ({ meetingId, title, date: "14 Oct", room: "Team", rule: "supporting-words" as const, supportWords: 90, quote });

beforeEach(() => {
  queue = {
    total: 3, byMeeting: { m1: 2, m2: 1 }, next: "",
    coverage: { visible: 5, checked: 3, waiting: 1, notChecked: 0, unavailable: { "no-transcript": 1 }, blocked: 0, unvalidatedLanguage: 3, unknownLanguage: 0 },
    groups: [
      { tagId: "tag_audio", label: "Audio bugs", color: "amber", icon: null, total: 1, items: [item("m1", "Weekly sync", "the playback stutters every few seconds")] },
      { tagId: "tag_release", label: "Release planning", color: "teal", icon: null, total: 2, items: [item("m1", "Weekly sync"), item("m2", "Planning")] },
    ],
  };
  batches = [];
  host = document.createElement("div");
  host.style.position = "relative";
  host.style.height = "900px";
  document.body.append(host);
});
afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
});

function show() {
  const provider = {
    loadReviewQueue: vi.fn(async () => queue),
    decideSuggestionBatch: vi.fn(async (request: { tagId: string; meetingIds: string[]; decision: "confirm" | "reject" }) => {
      batches.push(request);
      return { results: request.meetingIds.map((meetingId) => ({ meetingId, status: request.decision === "confirm" ? "confirmed" as const : "rejected" as const })) };
    }),
  };
  const events = { opened: [] as string[], changed: 0 };
  props = reactiveProps<Record<string, unknown>>({ open: true, provider, room: "" });
  app = mount(NeedsReview, {
    target: host, props: props as never,
    events: { openMeeting: (e: CustomEvent<string>) => events.opened.push(e.detail), changed: () => events.changed++ },
  });
  return { provider, events };
}

describe("Needs review", () => {
  it("groups suggestions by topic with the coverage line and quotes", async () => {
    show();
    await expect.element(page.getByTestId("coverage-line")).toHaveTextContent(
      "Checked 3 of the 5 meetings you can see · 1 waiting · 1 could not be checked (no transcript) · 3 in languages not yet validated.");
    await expect.element(page.getByRole("region", { name: "Suggested: Release planning" })).toBeVisible();
    await expect.element(page.getByText("the playback stutters every few seconds")).toBeVisible();
  });

  it("confirms several meetings at once", async () => {
    const { events } = show();
    const group = page.getByRole("region", { name: "Suggested: Release planning" });
    await group.getByRole("checkbox", { name: "Select Weekly sync" }).click();
    await group.getByRole("checkbox", { name: "Select Planning" }).click();
    await group.getByRole("button", { name: "Confirm selected (2)" }).click();
    await expect.element(group.getByText("Confirmed on 2 meetings.")).toBeVisible();
    expect(batches).toEqual([expect.objectContaining({ tagId: "tag_release", meetingIds: ["m1", "m2"], decision: "confirm" })]);
    expect(events.changed).toBe(1);
  });

  it("decides from the keyboard", async () => {
    show();
    await expect.element(page.getByTestId("coverage-line")).toBeVisible();
    host.querySelector<HTMLElement>('[aria-label="Audio bugs on Weekly sync"]')!.focus();
    await userEvent.keyboard("r");
    await vi.waitFor(() => expect(batches).toEqual([expect.objectContaining({ tagId: "tag_audio", meetingIds: ["m1"], decision: "reject" })]));
  });

  it("opens a meeting", async () => {
    const { events } = show();
    await page.getByRole("region", { name: "Suggested: Release planning" }).getByRole("button", { name: "Planning", exact: true }).click();
    expect(events.opened).toEqual(["m2"]);
  });

  it("ignores shortcuts and keys meant for a row's buttons", async () => {
    show();
    await expect.element(page.getByTestId("coverage-line")).toBeVisible();
    host.querySelector<HTMLElement>('[aria-label="Audio bugs on Weekly sync"]')!.focus();
    await userEvent.keyboard("{Control>}c{/Control}");
    page.getByRole("button", { name: "Not Audio bugs on Weekly sync" }).element().focus();
    await userEvent.keyboard(" ");
    await vi.waitFor(() => expect(batches).toEqual([expect.objectContaining({ tagId: "tag_audio", decision: "reject" })]));
    expect(page.getByRole("checkbox", { name: "Select Weekly sync" }).first().element()).not.toBeChecked();
  });

  it("forgets selections when it opens again or the room changes", async () => {
    show();
    const group = page.getByRole("region", { name: "Suggested: Release planning" });
    await group.getByRole("checkbox", { name: "Select Planning" }).click();
    await expect.element(group.getByRole("button", { name: "Confirm selected (1)" })).toBeEnabled();
    props.room = "id:other";
    await expect.element(page.getByRole("region", { name: "Suggested: Release planning" }).getByRole("button", { name: "Confirm selected" })).toBeDisabled();
  });

  it("does not repeat a meeting a later page brings again", async () => {
    queue.next = "c1";
    const { provider } = show();
    await expect.element(page.getByTestId("coverage-line")).toBeVisible();
    provider.loadReviewQueue.mockResolvedValueOnce({ ...queue, next: "", groups: [{ ...queue.groups[1], items: [item("m2", "Planning"), item("m3", "Retro")] }] });
    await page.getByRole("button", { name: "Show more" }).click();
    await expect.element(page.getByRole("button", { name: "Retro", exact: true })).toBeVisible();
    expect(page.getByRole("button", { name: "Planning", exact: true }).all()).toHaveLength(1);
  });

  it("says so when suggestions were switched off", async () => {
    queue = { enabled: false } as TopicQueue;
    show();
    await expect.element(page.getByText("Topic suggestions are switched off.")).toBeVisible();
  });
});
