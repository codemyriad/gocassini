import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import TagManager from "../tags/TagManager.svelte";
import { AnnotationError, type VocabularyTag } from "../../viewer/annotations";
import type { Topic, TopicLibrary, TopicSave } from "../../viewer/topics";
import "../../app.css";

const tag = (tagId: string, label: string): VocabularyTag => ({ tagId, namespace: "ns", label, meetings: 3, marks: 3, color: "", icon: "" });
const topic = (over: Partial<Topic>): Topic => ({
  tagId: "tag_budget", label: "budget", definition: "Budgets and spending.", color: null, icon: null, status: "active",
  revision: 1, updatedAt: "2026-10-14T10:00:00Z", preparing: false, broad: false, usage: { meetings: 4, pending: 2 }, ...over,
});

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let library: TopicLibrary;
let saves: TopicSave[];
let saveError: AnnotationError | null;

function provider() {
  return {
    updateTag: vi.fn(), mergeTag: vi.fn(), loadTagJob: vi.fn(async () => null),
    deleteTag: vi.fn(async (tagId: string) => ({ id: "j", kind: "delete", tagId, state: "finished", total: 1, done: 1, failed: [], actor: "a", startedAtUtc: "" })),
    loadTopicLibrary: vi.fn(async () => library),
    saveTopic: vi.fn(async (save: TopicSave) => {
      saves.push(save);
      if (saveError) throw saveError;
      return { topic: topic({ tagId: save.tagId ?? "tag_new", label: save.label ?? "x" }) };
    }),
    loadTopicHistory: vi.fn(async () => [
      { revision: 1, kind: "create", label: "budget", definition: "Money words pasted from a meeting.", at: "2026-10-01T10:00:00Z", redacted: false, canRedact: true },
      { revision: 2, kind: "definition", label: "budget", definition: "Budgets and spending.", at: "2026-10-02T10:00:00Z", redacted: false, canRedact: false },
    ]),
    redactTopicRevision: vi.fn(async () => {}),
    loadTopicTagRooms: vi.fn(async () => ({ rooms: ["Leadership 1:1"], roomCount: 1 })),
  };
}

beforeEach(() => {
  library = { enabled: true, ready: true, library: [topic({}), topic({ tagId: "tag_old", label: "travel", definition: "Trips and travel.", status: "retired" })] };
  saves = [];
  saveError = null;
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

function open(p = provider(), tags = [tag("tag_budget", "budget"), tag("tag_hiring", "hiring")]) {
  app = mount(TagManager, { target: host, props: { tags, provider: p, open: true } });
  return p;
}

describe("the Topics tab", () => {
  it("is absent while topic suggestions are off", async () => {
    library = { enabled: false };
    const p = open();
    await vi.waitFor(() => expect(p.loadTopicLibrary).toHaveBeenCalled());
    await expect.element(page.getByRole("heading", { name: /Manage tags/ })).toBeVisible();
    expect(page.getByRole("tab", { name: "Topics" }).query()).toBeNull();
  });

  it("lists topics with their usage and creates one", async () => {
    const p = open();
    await page.getByRole("tab", { name: "Topics" }).click();
    await expect.element(page.getByText("On 4 meetings · 2 to review")).toBeVisible();
    await expect.element(page.getByText("Budgets and spending.")).toBeVisible();
    await page.getByRole("button", { name: "New topic" }).click();
    await page.getByLabelText("Topic name").fill("Hiring");
    await expect.element(page.getByRole("button", { name: "Add topic" })).toBeDisabled();
    await page.getByLabelText("Definition").fill("Recruiting: job descriptions, interviews and offers.");
    await expect.element(page.getByText("Do not paste text from a meeting.", { exact: false })).toBeVisible();
    await page.getByRole("button", { name: "Add topic" }).click();
    await vi.waitFor(() => expect(saves).toHaveLength(1));
    expect(saves[0]).toMatchObject({ label: "Hiring", definition: "Recruiting: job descriptions, interviews and offers." });
    expect(p.loadTopicLibrary.mock.calls.length).toBeGreaterThan(1);
  });

  it("offers Restore or a separate topic when the name belongs to a retired one", async () => {
    saveError = new AnnotationError(409, "topic-retired", "tag_old");
    open();
    await page.getByRole("tab", { name: "Topics" }).click();
    await page.getByRole("button", { name: "New topic" }).click();
    await page.getByLabelText("Topic name").fill("travel");
    await page.getByLabelText("Definition").fill("Trips.");
    await page.getByRole("button", { name: "Add topic" }).click();
    await expect.element(page.getByText("“travel” was a topic that has been retired.", { exact: false })).toBeVisible();
    await page.getByRole("button", { name: "create a separate topic" }).click();
    await vi.waitFor(() => expect(saves.at(-1)).toMatchObject({ separate: true }));
    saveError = null;
    await page.getByRole("button", { name: "Restore it" }).click();
    await vi.waitFor(() => expect(saves.at(-1)).toEqual({ tagId: "tag_old", expectRevision: 1, status: "active" }));
  });

  it("describes an existing tag with the one-room warning", async () => {
    const p = open();
    await page.getByRole("button", { name: "Actions for hiring" }).click();
    await page.getByRole("menuitem", { name: "Describe as topic…" }).click();
    await expect.element(page.getByLabelText("Topic name")).toHaveValue("hiring");
    await expect.element(page.getByText("only on meetings from “Leadership 1:1”", { exact: false })).toBeVisible();
    await page.getByLabelText("Definition").fill("Recruiting.");
    await page.getByRole("button", { name: "Add topic" }).click();
    await vi.waitFor(() => expect(saves[0]).toMatchObject({ fromTagId: "tag_hiring", label: "hiring" }));
    expect(p.loadTopicTagRooms).toHaveBeenCalledWith("tag_hiring");
    // A tag that already is a topic is not offered again.
    await page.getByRole("tab", { name: "Tags" }).click();
    await page.getByRole("button", { name: "Actions for budget" }).click();
    expect(page.getByRole("menuitem", { name: "Describe as topic…" }).query()).toBeNull();
  });

  it("shows the history and removes a revision", async () => {
    const p = open();
    await page.getByRole("tab", { name: "Topics" }).click();
    await page.getByRole("button", { name: "History of budget" }).click();
    await expect.element(page.getByText("Money words pasted from a meeting.", { exact: false })).toBeVisible();
    await page.getByRole("button", { name: "Remove from history" }).click();
    await vi.waitFor(() => expect(p.redactTopicRevision).toHaveBeenCalledWith("tag_budget", 1));
  });

  it("retires the topic of a deleted tag when asked", async () => {
    const p = open();
    await page.getByRole("button", { name: "Actions for budget" }).click();
    await page.getByRole("menuitem", { name: "Delete…" }).click();
    await expect.element(page.getByRole("checkbox", { name: /Also retire this topic/ })).toBeChecked();
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    await vi.waitFor(() => expect(p.deleteTag).toHaveBeenCalledWith("tag_budget"));
    expect(saves).toEqual([{ tagId: "tag_budget", expectRevision: 1, status: "retired" }]);
  });

  it("restores an earlier revision from the history", async () => {
    library = { enabled: true, ready: true, library: [topic({ revision: 2 })] };
    open();
    await page.getByRole("tab", { name: "Topics" }).click();
    await page.getByRole("button", { name: "History of budget" }).click();
    await page.getByRole("button", { name: "Use this version" }).click();
    await vi.waitFor(() => expect(saves).toEqual([{ tagId: "tag_budget", expectRevision: 2, label: "budget", definition: "Money words pasted from a meeting." }]));
  });

  it("does not delete a tag whose topic could not be retired", async () => {
    saveError = new AnnotationError(409, "revision-conflict");
    const p = open();
    await page.getByRole("button", { name: "Actions for budget" }).click();
    await page.getByRole("menuitem", { name: "Delete…" }).click();
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    await expect.element(page.getByText("could not be retired, so the tag was not deleted", { exact: false })).toBeVisible();
    expect(p.deleteTag).not.toHaveBeenCalled();
  });

  it("warns when a typed name is an existing tag's", async () => {
    const p = open();
    await page.getByRole("tab", { name: "Topics" }).click();
    await page.getByRole("button", { name: "New topic" }).click();
    await page.getByLabelText("Topic name").fill("Hiring");
    await expect.element(page.getByText("only on meetings from “Leadership 1:1”", { exact: false })).toBeVisible();
    expect(p.loadTopicTagRooms).toHaveBeenCalledWith("tag_hiring");
  });
});
