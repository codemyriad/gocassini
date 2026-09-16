import { render } from "svelte/server";
import { describe, expect, it } from "vitest";

import { AnnotationError, type AnnotationItem, type MeetingAnnotations } from "../../viewer/annotations";
import MeetingTags from "./MeetingTags.svelte";
import StretchToolbar from "./StretchToolbar.svelte";
import TranscriptFrame from "./TranscriptFrame.svelte";
import { createMarksSession, viewMarks } from "./session";

// Rendered through Svelte's server renderer, as TranscriptWords.test.ts does:
// the suite has no DOM, so these assert what each state puts on the page.

const item = (id: string, tagId: string, target: AnnotationItem["target"]): AnnotationItem => ({
  id,
  tagId,
  target,
  createdAtUtc: "",
  actor: { kind: "user", id: "ana" },
  operationId: "op",
});

const meeting = (resolved: boolean): MeetingAnnotations => ({
  meetingId: "m1",
  revision: 1,
  resolved,
  annotations: {
    format: "cassini.annotations.v1",
    revision: 1,
    audioOpusSha256: "",
    tagNamespace: "",
    tags: [
      { id: "t-hiring", label: "hiring" },
      { id: "t-budget", label: "budget" },
    ],
    items: [
      item("i1", "t-hiring", { kind: "time-range", startMs: 5000, endMs: 9000 }),
      item("i2", "t-budget", { kind: "time-range", startMs: 1000, endMs: 6000 }),
      item("i3", "t-budget", { kind: "meeting" }),
    ],
  },
});

async function opened(load: () => Promise<MeetingAnnotations>) {
  const session = createMarksSession(() => {});
  await session.open(load, async () => {
    throw new Error("not written in these tests");
  });
  return session;
}

const frame = (session: ReturnType<typeof createMarksSession>) =>
  render(TranscriptFrame, { props: { session, durationMs: 60_000 } }).body;

describe("a meeting view with no annotation loader", () => {
  it("offers find and nothing else: no rail, no marking, no marks, no header tags", () => {
    const session = createMarksSession(() => {});
    const html = frame(session);
    expect(html).toContain('aria-label="Find in this meeting"');
    expect(html).not.toContain("The whole meeting");
    expect(html).not.toContain("Mark with a tag");
    expect(html).not.toContain("Marks");
    expect(render(MeetingTags, { props: { session } }).body).not.toContain("Add tag");
  });
});

describe("a meeting view with its marks loaded", () => {
  it("adds the rail and the tagged-sections count to the transcript, and no tagging mode to set first", async () => {
    const html = frame(await opened(async () => meeting(true)));
    // Rendered with no width, the frame is laid out for a narrow screen, where
    // the rail is a map of the tagged sections and the text does the grabbing.
    expect(html).toContain('aria-label="Tagged sections across the whole meeting"');
    expect(html.match(/class="mr-seg /g)).toHaveLength(2);
    // On the section's own heading line, in words: "marks" was the data model's
    // term for one application of a tag, and the only one a reader had to be
    // taught.
    expect(html).toMatch(/2\s+tagged sections/);
    // Arming is chosen from a selection's own toolbar, at the moment somebody
    // is tagging; in the search bar it was a mode with no readable purpose.
    expect(html).not.toContain("Mark with a tag…");
  });

  it("puts the whole-meeting tags in the header, removable, with a way to add one", async () => {
    const html = render(MeetingTags, { props: { session: await opened(async () => meeting(true)) } }).body;
    expect(html).toContain("budget");
    expect(html).toContain('aria-label="Remove budget"');
    expect(html).toContain("Add tag");
    expect(html).not.toContain("can't be placed");
  });

  it("counts marks made against other audio instead of drawing them, and offers to remove them", async () => {
    const session = await opened(async () => meeting(false));
    const html = render(MeetingTags, { props: { session } }).body;
    expect(html).toContain("2 marks can't be placed on this recording");
    expect(html).toContain("Remove them");
    expect(frame(session)).toMatch(/0\s+tagged sections/);
  });

  it("says quietly that tags are being prepared on a 503", async () => {
    const session = await opened(async () => {
      throw new AnnotationError(503, "");
    });
    const html = render(MeetingTags, { props: { session } }).body;
    expect(html).toContain("Tags are being prepared");
    expect(html).not.toContain("Add tag");
    expect(frame(session)).not.toContain("The whole meeting");
  });
});

describe("the stretch toolbar", () => {
  const toolbar = (props: Record<string, unknown>) =>
    render(StretchToolbar, { props: { startMs: 1234, endMs: 5470, ...props } }).body;

  it("offers to tag a new section, and to clear it, with its times", () => {
    const html = toolbar({});
    expect(html).toContain("0:01.2");
    expect(html).toContain("0:05.4");
    expect(html).toContain("Tag selection");
    expect(html).toContain("Clear");
    // No mode to set before tagging, and nothing to offer again yet.
    expect(html).not.toContain("Keep this tag ready");
    expect(html).not.toContain("Tag as ");
  });

  it("offers the last tag used again, in one click, beside the way to pick any", () => {
    const html = toolbar({ recent: { tagId: "t-hiring", label: "hiring" } });
    expect(html).toContain('aria-label="Tag as hiring"');
    expect(html).toContain("Tag selection");
  });

  it("saves a moved mark only when asked, and can remove it", async () => {
    const session = await opened(async () => meeting(true));
    let state = { status: "off" } as Parameters<typeof viewMarks>[0];
    session.subscribe((value) => (state = value))();
    const mark = viewMarks(state, []).placed[0]!;
    expect(toolbar({ mark })).not.toContain("Save changes");
    expect(toolbar({ mark })).not.toContain("Unsaved changes");
    expect(toolbar({ mark })).toContain("Done");
    const moved = toolbar({ mark, moved: true });
    // Said as a state, and saved with the strongest button on the card.
    expect(moved).toContain("Unsaved changes");
    expect(moved).toContain("Save changes");
    expect(moved).toContain("Remove");
    expect(moved).toContain("Cancel");
  });
});
