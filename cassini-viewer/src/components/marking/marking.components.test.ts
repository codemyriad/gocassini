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
  it("adds the rail, marking and the marks toggle to the transcript", async () => {
    const html = frame(await opened(async () => meeting(true)));
    expect(html).toContain("The whole meeting. Drag down it to grab a stretch");
    expect(html).toContain("Mark with a tag…");
    expect(html).toMatch(/Marks <span[^>]*>2<\/span>/);
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
    expect(frame(session)).toMatch(/Marks <span[^>]*>0<\/span>/);
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

  it("offers to tag a new stretch, and to clear it, with its times", () => {
    const html = toolbar({});
    expect(html).toContain("0:01.2");
    expect(html).toContain("0:05.4");
    expect(html).toContain("Tag this stretch");
    expect(html).toContain("Clear");
  });

  it("is prefilled with the tag in hand, which it still waits to confirm", () => {
    const html = toolbar({ armed: { tagId: "t-hiring", label: "hiring" } });
    expect(html).toContain("Tag as hiring");
    expect(html).not.toContain("Tag this stretch");
  });

  it("saves a moved mark only when asked, and can remove it", async () => {
    const session = await opened(async () => meeting(true));
    let state = { status: "off" } as Parameters<typeof viewMarks>[0];
    session.subscribe((value) => (state = value))();
    const mark = viewMarks(state, []).placed[0]!;
    expect(toolbar({ mark })).not.toContain("Save move");
    expect(toolbar({ mark })).toContain("Done");
    const moved = toolbar({ mark, moved: true });
    expect(moved).toContain("Save move");
    expect(moved).toContain("Remove");
    expect(moved).toContain("Cancel");
  });
});
