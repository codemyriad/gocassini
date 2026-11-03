import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import SuggestedTopics from "./SuggestedTopics.svelte";
import { AnnotationError } from "../../viewer/annotations";
import { StaticCatalogProvider } from "../../viewer/dataProvider";
import { TOPICS_PREVIEW_NOTE, type MeetingSuggestions, type TopicDecision } from "../../viewer/topics";
import { reactiveProps } from "./reactiveProps.svelte";
import "../../app.css";

const entry = { id: "m1", title: "Weekly sync", dateLabel: "", audioPath: "m1.opus" };
let view: MeetingSuggestions;
let decisions: { tagId: string; decision: TopicDecision; requestId: string }[];
let failWith: AnnotationError | null;
let app: ReturnType<typeof mount> | undefined;
let props: Record<string, unknown>;
let host: HTMLDivElement;

function suggestionsView(over: Partial<MeetingSuggestions> = {}): MeetingSuggestions {
  return {
    enabled: true, state: "done", language: "en", validatedLanguage: false, confirmable: true,
    suggestions: [{
      tagId: "tag_release", label: "Release planning", definition: "Shipping a new version: checklists, signing, rollouts.",
      color: "teal", icon: null, rule: "supporting-words", supportWords: 89,
      evidence: [{ startMs: 32000, endMs: 48000, wordCount: 42, counted: true }, { startMs: 49000, endMs: 67000, wordCount: 47, counted: true }],
    }],
    decided: [], ...over,
  };
}

beforeEach(() => {
  view = suggestionsView();
  decisions = [];
  failWith = null;
  host = document.createElement("div");
  document.body.append(host);
});
afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
});

function show() {
  const provider = {
    loadMeetingSuggestions: vi.fn(async () => view),
    decideSuggestion: vi.fn(async (_e: unknown, tagId: string, decision: TopicDecision, requestId: string) => {
      decisions.push({ tagId, decision, requestId });
      if (failWith) throw failWith;
      if (decision === "confirm") {
        view = suggestionsView({ suggestions: [], decided: [{ tagId, label: "Release planning", verdict: "confirmed", source: "review", at: "" }] });
        return { annotations: { meetingId: "m1", revision: 2 } as never };
      }
      if (decision === "reject") view = suggestionsView({ suggestions: [], decided: [{ tagId, label: "Release planning", verdict: "rejected", source: "review", at: "" }] });
      else view = suggestionsView();
      return {};
    }),
  };
  const seek = vi.fn();
  const events = { evidence: [] as unknown[], annotations: [] as unknown[] };
  props = reactiveProps<Record<string, unknown>>({ entry, provider, seek, marksRevision: null, quote: (start: number) => (start === 32000 ? "The changelog is not finished, the Android build needs signing." : "") });
  app = mount(SuggestedTopics, {
    target: host,
    props: props as never,
    events: { evidence: (e: CustomEvent) => events.evidence.push(e.detail), annotations: (e: CustomEvent) => events.annotations.push(e.detail) },
  });
  return { provider, seek, events };
}

describe("suggested topics on the meeting page", () => {
  it("shows the suggestion, its evidence and the notes, and seeks", async () => {
    const { seek, events } = show();
    await expect.element(page.getByRole("group", { name: "Suggested topics" }).getByText("Preview", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Suggested topic Release planning" }).click();
    const dialog = page.getByRole("dialog", { name: "Suggested topic: Release planning" });
    await expect.element(dialog.getByText(TOPICS_PREVIEW_NOTE)).toBeVisible();
    await expect.element(dialog.getByText("Shipping a new version", { exact: false })).toBeVisible();
    await expect.element(dialog.getByText("Discussed for about 1 minute across 2 passages", { exact: false })).not.toBeInTheDocument();
    await expect.element(dialog.getByText(/Discussed for about/)).toBeVisible();
    await expect.element(dialog.getByText("The changelog is not finished", { exact: false })).toBeVisible();
    await expect.element(dialog.getByText("Not saved to the recording until someone confirms it.", { exact: false })).toBeVisible();
    await expect.element(dialog.getByText("Suggestions have not been checked for this meeting's language.")).toBeVisible();
    expect(events.evidence.at(-1)).toEqual([{ startMs: 32000, endMs: 48000 }, { startMs: 49000, endMs: 67000 }]);
    await dialog.getByRole("button", { name: "Play from 0:32" }).click();
    expect(seek).toHaveBeenCalledWith(32000);
  });

  it("stays marked Preview when nothing is pending", async () => {
    view = suggestionsView({ suggestions: [], decided: [{ tagId: "tag_release", label: "Release planning", verdict: "rejected", source: "review", at: "" }] });
    show();
    const group = page.getByRole("group", { name: "Suggested topics" });
    await expect.element(group.getByText("Decided for this meeting (1)")).toBeVisible();
    await expect.element(group.getByText("Preview", { exact: true })).toBeVisible();
  });

  it("shows no lone Preview badge on a checked meeting with nothing to show", async () => {
    view = suggestionsView({ suggestions: [] });
    show();
    await expect.element(page.getByRole("group", { name: "Suggested topics" })).toBeInTheDocument();
    await expect.element(page.getByText("Preview", { exact: true })).not.toBeInTheDocument();
  });

  it("confirms, hands the annotations on, and undoes from the toast", async () => {
    const { events } = show();
    await page.getByRole("button", { name: "Suggested topic Release planning" }).click();
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect.element(page.getByText("Tagged “Release planning”.")).toBeVisible();
    expect(events.annotations).toHaveLength(1);
    expect(page.getByRole("button", { name: "Suggested topic Release planning" }).query()).toBeNull();
    await page.getByRole("button", { name: "Undo" }).click();
    await expect.element(page.getByRole("button", { name: "Suggested topic Release planning" })).toBeVisible();
    expect(decisions.map((d) => d.decision)).toEqual(["confirm", "undo"]);
  });

  it("rejects, lists the decision, and reopens it", async () => {
    show();
    await page.getByRole("button", { name: "Suggested topic Release planning" }).click();
    await page.getByRole("button", { name: "Not this meeting" }).click();
    await expect.element(page.getByText("won't be suggested for this meeting", { exact: false })).toBeVisible();
    await page.getByText("Decided for this meeting (1)").click();
    await page.getByRole("button", { name: "Reopen" }).click();
    await expect.element(page.getByRole("button", { name: "Suggested topic Release planning" })).toBeVisible();
    expect(decisions.map((d) => d.decision)).toEqual(["reject", "reopen"]);
  });

  it("explains unresolved marks and keeps Confirm off", async () => {
    view = suggestionsView({ confirmable: false, blockedReason: "marks-unresolved" });
    show();
    await page.getByRole("button", { name: "Suggested topic Release planning" }).click();
    await expect.element(page.getByText("This meeting's audio changed", { exact: false })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Confirm" })).toBeDisabled();
    await expect.element(page.getByRole("button", { name: "Not this meeting" })).toBeEnabled();
  });

  it("keeps one request id across a retry", async () => {
    failWith = new AnnotationError(503, "");
    show();
    await page.getByRole("button", { name: "Suggested topic Release planning" }).click();
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect.element(page.getByText("That did not work. Try again.")).toBeVisible();
    failWith = null;
    await page.getByRole("button", { name: "Confirm" }).click();
    await vi.waitFor(() => expect(decisions).toHaveLength(2));
    expect(decisions[0].requestId).toBe(decisions[1].requestId);
  });

  it("says why a meeting has nothing to review", async () => {
    view = suggestionsView({ state: "unavailable", reason: "no-transcript", suggestions: [] });
    show();
    await expect.element(page.getByText("could not be checked (no transcript)", { exact: false })).toBeVisible();
  });

  it("is absent from the public viewer", () => {
    const provider = new StaticCatalogProvider() as unknown as Record<string, unknown>;
    expect(provider.loadMeetingSuggestions).toBeUndefined();
    expect(provider.decideSuggestion).toBeUndefined();
    expect(provider.loadTopicLibrary).toBeUndefined();
  });

  it("forgets the last meeting's toast when another opens", async () => {
    show();
    await page.getByRole("button", { name: "Suggested topic Release planning" }).click();
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect.element(page.getByRole("button", { name: "Undo" })).toBeVisible();
    view = suggestionsView();
    props.entry = { ...entry, id: "m2" };
    await vi.waitFor(() => expect(page.getByRole("button", { name: "Undo" }).query()).toBeNull());
  });

  it("sends an Undo clicked while the page reloads", async () => {
    show();
    await page.getByRole("button", { name: "Suggested topic Release planning" }).click();
    await page.getByRole("button", { name: "Confirm" }).click();
    await page.getByRole("button", { name: "Undo" }).click();
    await vi.waitFor(() => expect(decisions.map((d) => d.decision)).toEqual(["confirm", "undo"]));
  });

  it("lets Confirm through once the old marks are sorted out", async () => {
    view = suggestionsView({ confirmable: false, blockedReason: "marks-unresolved" });
    show();
    await page.getByRole("button", { name: "Suggested topic Release planning" }).click();
    await expect.element(page.getByRole("button", { name: "Confirm" })).toBeDisabled();
    view = suggestionsView();
    props.marksRevision = 7;
    await expect.element(page.getByRole("button", { name: "Suggested topic Release planning" })).toBeVisible();
    await page.getByRole("button", { name: "Suggested topic Release planning" }).click();
    await expect.element(page.getByRole("button", { name: "Confirm" })).toBeEnabled();
  });
});
