import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import TopicInbox from "./TopicInbox.svelte";
import { AnnotationError } from "../../viewer/annotations";
import { TOPICS_PREVIEW_NOTE, type Topic, type TopicProposal } from "../../viewer/topics";
import "../../app.css";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let proposals: TopicProposal[];
let actions: { id: string; action: Record<string, unknown> }[];

const proposal = (over: Partial<TopicProposal>): TopicProposal => ({
  id: "tp1", roomId: "r1", room: "Facilities", state: "open", draftName: "parking / garage", hints: ["parking", "garage", "permits"],
  flags: {}, meetings: 4, days: 4,
  evidence: [{ meetingId: "m1", title: "Office sync", date: "1 Oct", startMs: 0, endMs: 1000, quote: "the garage permits are late again" }],
  ...over,
});
const topics = [{ tagId: "tag_fac", label: "Facilities", status: "active", definition: "Office buildings and rooms.", revision: 3 } as Topic];
let saved: Record<string, unknown>[];

beforeEach(() => {
  proposals = [proposal({}), proposal({ id: "tp2", draftName: "thanks / everyone", flags: { edges: true } }), proposal({ id: "tp3", draftName: "old idea", state: "dismissed" })];
  actions = [];
  saved = [];
  host = document.createElement("div");
  document.body.append(host);
});
afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
});

function show(events: Record<string, (e: CustomEvent) => void> = {}) {
  const provider = {
    loadTopicProposals: vi.fn(async () => ({ enabled: true, proposals })),
    decideTopicProposal: vi.fn(async (id: string, action: Record<string, unknown>) => {
      actions.push({ id, action });
      if (action.action === "approve" && !action.confirmTerms) throw new AnnotationError(409, "terms-from-meetings");
      return { proposal: { id, state: "approved" } };
    }),
    saveTopic: vi.fn(async (save: Record<string, unknown>) => {
      saved.push(save);
      return { topic: topics[0] };
    }),
  };
  app = mount(TopicInbox, { target: host, props: { provider, topics }, events });
  return provider;
}

describe("New topics", () => {
  it("shows drafts with their words and quotes, flagged ones collapsed", async () => {
    show();
    await expect.element(page.getByText("Preview", { exact: true })).toBeVisible();
    await expect.element(page.getByText(TOPICS_PREVIEW_NOTE)).toBeVisible();
    await expect.element(page.getByText("parking / garage").first()).toBeVisible();
    await expect.element(page.getByText("Words from meetings in Facilities: parking, garage, permits").first()).toBeVisible();
    await expect.element(page.getByText("the garage permits are late again").first()).toBeVisible();
    await expect.element(page.getByText("mostly greetings and wrap-ups", { exact: false })).toBeVisible();
    expect((host.querySelector('[aria-label="New topic: thanks / everyone"]') as HTMLDetailsElement).open).toBe(false);
  });

  it("needs a person's name and definition, and a confirmation for the meetings' words", async () => {
    show();
    await page.getByRole("button", { name: "Create topic" }).first().click();
    await expect.element(page.getByRole("textbox", { name: "Topic name" })).toHaveValue("");
    await page.getByRole("textbox", { name: "Topic name" }).fill("Parking permits");
    await page.getByRole("textbox", { name: "Definition" }).fill("Staff car parking and the permits for it.");
    await page.getByRole("button", { name: "Create topic" }).first().click();
    await expect.element(page.getByRole("alertdialog", { name: "Words from these meetings" })).toHaveTextContent("parking, permits");
    await page.getByRole("button", { name: "Save anyway" }).click();
    await vi.waitFor(() => expect(actions.at(-1)?.action).toMatchObject({ action: "approve", label: "Parking permits", confirmTerms: true }));
  });

  it("links, dismisses and restores", async () => {
    show();
    await page.getByRole("button", { name: "Link to topic" }).first().click();
    await page.getByRole("combobox", { name: "Topic to link to" }).selectOptions("tag_fac");
    await page.getByRole("button", { name: "Link", exact: true }).click();
    await vi.waitFor(() => expect(actions.at(-1)?.action).toMatchObject({ action: "link", tagId: "tag_fac" }));
    await page.getByRole("button", { name: "Dismiss" }).first().click();
    await vi.waitFor(() => expect(actions.at(-1)?.action).toMatchObject({ action: "dismiss" }));
    await page.getByText("Dismissed (1)").click();
    await page.getByRole("button", { name: "Restore" }).click();
    await vi.waitFor(() => expect(actions.at(-1)).toMatchObject({ id: "tp3", action: { action: "restore" } }));
  });

  it("offers to sharpen a linked topic's definition, confirming the meetings' words", async () => {
    show();
    await page.getByRole("button", { name: "Link to topic" }).first().click();
    await page.getByRole("combobox", { name: "Topic to link to" }).selectOptions("tag_fac");
    await page.getByRole("button", { name: "Link", exact: true }).click();
    await page.getByRole("textbox", { name: "New definition" }).fill("Office buildings, rooms and the parking garage.");
    await page.getByRole("button", { name: "Save definition" }).click();
    await expect.element(page.getByRole("alertdialog", { name: "Words from these meetings" })).toHaveTextContent("parking, garage");
    await page.getByRole("button", { name: "Save anyway" }).click();
    await vi.waitFor(() => expect(saved).toEqual([{ tagId: "tag_fac", expectRevision: 3, definition: "Office buildings, rooms and the parking garage." }]));
  });

  it("shows the draft beside the fields", async () => {
    show();
    await page.getByRole("button", { name: "Create topic" }).first().click();
    await expect.element(page.getByText("Draft:", { exact: false }).first()).toHaveTextContent("parking / garage");
  });

  it("shows an action's error on the card acted on, even while creating another", async () => {
    proposals = [proposal({}), proposal({ id: "tp5", draftName: "budget / invoices" })];
    const provider = show();
    await page.getByRole("button", { name: "Create topic" }).nth(1).click();
    provider.decideTopicProposal.mockRejectedValueOnce(new AnnotationError(409, "proposal-dismissed"));
    await page.getByRole("button", { name: "Dismiss" }).first().click();
    const card = page.getByRole("group", { name: "New topic: parking / garage" });
    await expect.element(card.getByRole("alert")).toHaveTextContent("Someone else decided");
    await expect.element(page.getByRole("textbox", { name: "Topic name" })).toBeVisible();
  });

  it("keeps every room in the selector while one is chosen", async () => {
    proposals = [proposal({}), proposal({ id: "tp4", roomId: "r2", room: "Finance", draftName: "budget" })];
    const provider = show();
    provider.loadTopicProposals.mockImplementation(async (room?: string) => ({ enabled: true, proposals: proposals.filter((p) => !room || room === `id:${p.roomId}`) }));
    await page.getByRole("combobox", { name: "Room" }).selectOptions("r2");
    await expect.element(page.getByText("budget").first()).toBeVisible();
    const options = [...host.querySelectorAll<HTMLOptionElement>('select[aria-label="Room"] option')].map((o) => o.textContent?.trim());
    expect(options).toEqual(expect.arrayContaining(["Facilities", "Finance"]));
  });

  it("says when the list could not be loaded", async () => {
    const provider = show();
    provider.loadTopicProposals.mockRejectedValue(new Error("down"));
    await page.getByRole("button", { name: "Dismiss" }).first().click();
    await expect.element(page.getByText("New topics could not be loaded.")).toBeVisible();
  });

  it("opens a meeting from its title, quote or not", async () => {
    proposals = [proposal({ evidence: [{ meetingId: "m9", title: "Quiet meeting", date: "2 Oct", startMs: 5000, endMs: 6000, quote: "" }] })];
    const opened: unknown[] = [];
    show({ openMeeting: (e) => opened.push(e.detail) });
    await page.getByRole("button", { name: "Quiet meeting" }).click();
    expect(opened).toEqual([{ meetingId: "m9", startMs: 5000 }]);
  });
});
