import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { page } from "vitest/browser";

import MeetingView from "./MeetingView.svelte";
import "../app.css";
import { meeting, original, speakersFixture } from "./speakers/speakers.fixture";
import type { LoadedArtifact } from "../viewer/loadArtifact";

// A meeting's summary, as its readers ask for it (D-814): nothing is sent
// before the cost is shown, replacing one is offered only to whoever may, and
// a speaker edit keeps the summary unless someone allowed to asks otherwise.

const SHOTS = "../../node_modules/.cache/vitest-screenshots/summary";

let fixture: ReturnType<typeof speakersFixture>;
let view: ReturnType<typeof mount> | undefined;
let root: HTMLDivElement;

beforeEach(() => {
  root = document.createElement("div");
  root.className = "cassini-root";
  root.dataset.theme = "saturn-light";
  root.style.height = "100vh";
  document.body.append(root);
});

afterEach(async () => {
  if (view) await unmount(view);
  view = undefined;
  fixture?.dispose();
  root.remove();
});

function mountView() {
  view = mount(MeetingView, {
    target: root,
    props: {
      dataProvider: fixture.provider, meeting, surface: "app", bundled: false, isDesktop: true,
      speakerEditsPollMs: 20, speakerEditsClock: fixture.time.clock, summaryRequestPollMs: 20,
    },
  });
}

const details = () => page.getByRole("dialog", { name: "Meeting details" });
const openPeople = () => page.getByRole("button", { name: "Meeting details" }).click();
const SUMMARY = "# Summary\n\nThe team agreed to ship on Friday.";

describe("asking for a meeting's summary", () => {
  it("writes a missing one only after the cost is shown, then shows it", async () => {
    fixture = speakersFixture();
    fixture.summaryRequest({ available: true, reason: undefined, canWrite: true });
    mountView();
    // The meeting has finished loading before anything is asked.
    await expect.element(page.getByText("Loading meeting…")).not.toBeInTheDocument();

    await page.getByRole("button", { name: "Write summary", exact: true }).click();
    const confirm = page.getByRole("alertdialog", { name: "Write a summary for this meeting?" });
    await expect.element(confirm.getByText("1 call to summary-model (Hosted) · about 900 input tokens, 120 words")).toBeVisible();
    await expect.element(confirm.getByText("The transcript is sent to an external service, which may bill for it.")).toBeVisible();
    expect(fixture.requestSummary).not.toHaveBeenCalled();
    await page.screenshot({ path: `${SHOTS}/01-confirm-write.png` });

    await confirm.getByRole("button", { name: "Write summary", exact: true }).click();
    expect(fixture.requestSummary).toHaveBeenLastCalledWith(meeting, false);
    await expect.element(page.getByText(/Writing the summary/)).toBeVisible();

    // Written and republished: the meeting is read again and shows it.
    fixture.summaryWritten(SUMMARY);
    await expect.element(page.getByText("The team agreed to ship on Friday.")).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Write summary", exact: true })).not.toBeInTheDocument();
  });

  it("offers to regenerate only to someone who may replace it", async () => {
    fixture = speakersFixture();
    fixture.summaryWritten(SUMMARY);
    fixture.summaryRequest({ available: true, reason: undefined, canRegenerate: false });
    mountView();
    await expect.element(page.getByText("The team agreed to ship on Friday.")).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Regenerate summary", exact: true })).not.toBeInTheDocument();
    await unmount(view!);

    fixture.summaryRequest({ canRegenerate: true });
    mountView();
    await page.getByRole("button", { name: "Regenerate summary", exact: true }).click();
    const confirm = page.getByRole("alertdialog", { name: "Replace this meeting's summary?" });
    await expect.element(confirm.getByText(/sees the new summary instead of this one/)).toBeVisible();
    await confirm.getByRole("button", { name: "Regenerate summary", exact: true }).click();
    expect(fixture.requestSummary).toHaveBeenLastCalledWith(meeting, true);
  });

  it("says why a meeting has no summary when its file says, with nothing to offer", async () => {
    fixture = speakersFixture();
    const { loadSummaryRequestState: _l, requestSummary: _r, ...readOnly } = fixture.provider;
    const failed = (audio: string): LoadedArtifact => ({ ...original(audio), summaryStatus: { status: "failed", reason: "llm_failed" } });
    readOnly.loadMeetingForEntry = async () => failed(fixture.audioSrc);
    view = mount(MeetingView, { target: root, props: { dataProvider: readOnly, meeting, surface: "app", bundled: false, isDesktop: true } });
    await expect.element(page.getByText("No summary: it could not be generated when the meeting was published.")).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Write summary", exact: true })).not.toBeInTheDocument();
  });
});

describe("speaker edits and the summary", () => {
  it("keeps the summary unless someone allowed to asks to rewrite it", async () => {
    fixture = speakersFixture();
    fixture.summaryWritten(SUMMARY);
    fixture.summaryRequest({ available: true, reason: undefined });
    fixture.canRewriteSummary(true);
    mountView();
    await openPeople();
    await details().getByRole("textbox", { name: "Name for Ben Ortiz" }).fill("Benjamin Ortiz");
    const rewrite = details().getByRole("checkbox", { name: /Also rewrite the summary for these names/ });
    await expect.element(rewrite).not.toBeChecked();
    await expect.element(details().getByText("1 call to summary-model (Hosted) · about 900 input tokens, 120 words")).toBeVisible();
    await page.screenshot({ element: details(), path: `${SHOTS}/02-rewrite-choice.png` });
    await rewrite.click();
    await details().getByRole("button", { name: "Save", exact: true }).click();
    expect(fixture.save).toHaveBeenLastCalledWith(meeting, 0, expect.objectContaining({ labels: [{ speakerId: "spk_ben", label: "Benjamin Ortiz" }] }), "rewrite");
  });

  it("does not offer the rewrite to a reader who may not ask for it", async () => {
    fixture = speakersFixture();
    fixture.summaryWritten(SUMMARY);
    mountView();
    await openPeople();
    await details().getByRole("textbox", { name: "Name for Ben Ortiz" }).fill("Benjamin Ortiz");
    await expect.element(details().getByRole("checkbox", { name: /Also rewrite the summary/ })).not.toBeInTheDocument();
    await details().getByRole("button", { name: "Save", exact: true }).click();
    expect(fixture.save.mock.lastCall).toHaveLength(3);
  });
});
