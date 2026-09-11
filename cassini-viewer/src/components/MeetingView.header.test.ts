import { describe, expect, it } from "vitest";

import meetingViewSource from "./MeetingView.svelte?raw";

// Source-level assertions, for the reason MeetingList.test.ts gives: the suite
// runs in node with no DOM harness. What is asserted here is what the header
// has to keep saying while the transcript scrolls under it.

describe("MeetingView header", () => {
  it("keeps the meeting's name on screen while the transcript scrolls", () => {
    // It used to be a strip of status badges with the title in a SECOND,
    // scrolling header below it, so the one thing saying which meeting you were
    // reading left the screen the moment you started reading it.
    const header = meetingViewSource.slice(
      meetingViewSource.indexOf("<header class=\"sticky"),
      meetingViewSource.indexOf("</header>"),
    );
    expect(header).toContain('{meeting ? meeting.title : "Meeting transcript viewer"}');
    expect(header).toContain("roomLabelOf(meeting)");
    expect(header).toContain("formatMeetingDate(meeting.dateLabel)");
    expect(header).toContain("formatClockTime(clampedDurationMs)");
    expect(header).toContain("{#each speakerNames as name}");
    // And there is no second header left to scroll away.
    expect(meetingViewSource).not.toContain('class="m-4 mb-8 min-[981px]:mx-8');
  });

  it("is opaque, because it sits over the browse list", () => {
    // A blurred, translucent header with a meeting list showing through it
    // reads as two pages at once.
    expect(meetingViewSource).not.toContain("backdrop-blur-lg");
  });

  it("renders each fact only where it is known", () => {
    // The room and the date come from the catalog and are there before anything
    // loads; the duration and the speakers come out of the artifact and arrive
    // with it. A duration of 0:00 under a title is a claim, not a placeholder.
    expect(meetingViewSource).toContain("{#if transcriptIndex && clampedDurationMs > 0}");
    expect(meetingViewSource).toContain("{#if speakerNames.length > 0}");
  });
});

describe("MeetingView tagging", () => {
  it("works unwired: every tagging prop has a default, and no loader means no tagging", () => {
    expect(meetingViewSource).toContain("export let tagVocabulary: VocabularyTag[] = [];");
    expect(meetingViewSource).toContain(
      "export let loadAnnotations: (() => Promise<MeetingAnnotations>) | null = null;",
    );
    expect(meetingViewSource).toContain(
      "export let applyAnnotations: ((request: AnnotationRequest) => Promise<AnnotationResult>) | null = null;",
    );
    // A null loader leaves the session off, and an off session draws nothing
    // (marking.components.test.ts renders both states).
    expect(meetingViewSource).toContain('$: marksFor = loadAnnotations ? (meeting?.id ?? "") : null;');
    expect(meetingViewSource).toContain("void marks.open(loadAnnotations, applyAnnotations);");
  });

  it("says the tags changed after every write that succeeded", () => {
    expect(meetingViewSource).toContain(
      'const marks = createMarksSession((result) => dispatch("tagsChanged", result));',
    );
  });

  it("keeps whole-meeting tags in the header and wraps the transcript in the marking frame", () => {
    const header = meetingViewSource.slice(
      meetingViewSource.indexOf('<header class="sticky'),
      meetingViewSource.indexOf("</header>"),
    );
    expect(header).toContain("<MeetingTags session={marks} vocabulary={tagVocabulary} />");
    const frameAt = meetingViewSource.indexOf("<TranscriptFrame");
    expect(frameAt).toBeGreaterThan(-1);
    expect(meetingViewSource.indexOf("{#each transcriptRows as row (row.key)}")).toBeGreaterThan(frameAt);
    expect(meetingViewSource.indexOf("</TranscriptFrame>")).toBeGreaterThan(
      meetingViewSource.indexOf("{#each transcriptRows as row (row.key)}"),
    );
  });
});

describe("MeetingView linked insights", () => {
  it("shows what a meeting was used for under its summary, not under its transcript", () => {
    // It was a strip pinned to the bottom of the sheet, below the whole
    // transcript, where nobody scrolled to it. It is a fact of the same kind as
    // the summary — what came OUT of this conversation — so it reads with it.
    const summaryAt = meetingViewSource.indexOf("{@html summaryHtml}");
    const insightsAt = meetingViewSource.indexOf("{#if linkedInsights.length > 0}");
    const transcriptAt = meetingViewSource.indexOf(
      '<p class="text-xl font-semibold text-base-content">Transcript</p>',
    );
    expect(summaryAt).toBeGreaterThan(-1);
    expect(insightsAt).toBeGreaterThan(summaryAt);
    expect(transcriptAt).toBeGreaterThan(insightsAt);
  });

  it("is handed the links rather than looking them up", () => {
    // What a meeting was used FOR is not part of the recording, so it is the
    // shell's fact: it resolves them against the WHOLE catalog, and a build with
    // no operator to ask passes none.
    expect(meetingViewSource).toContain("export let linkedInsights: InsightRecord[] = [];");
    expect(meetingViewSource).toContain(
      "export let insightSourceCounts: ReadonlyMap<string, number> = new Map();",
    );
    expect(meetingViewSource).toContain('dispatch("openInsight", record)');
  });

  it("counts only the sources this caller can read", () => {
    // Not meetingIds.length: a source they may not read is absent, and a count
    // is a disclosure that it existed.
    expect(meetingViewSource).toContain("insightSourceCounts.get(record.id)");
    expect(meetingViewSource).not.toContain("record.meetingIds.length");
  });
});
