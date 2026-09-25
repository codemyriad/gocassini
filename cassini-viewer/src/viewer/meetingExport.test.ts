import { describe, expect, it } from "vitest";
import { transcriptMarkdown, safeMeetingStem } from "./meetingExport";
import type { JudgedDisplaySegment } from "../core/transcript";

const segment = (id: string, speakerLabel: string, startMs: number, text: string): JudgedDisplaySegment => ({
  id,
  speaker: id,
  speakerLabel,
  startMs,
  endMs: startMs + 1000,
  text,
  tokens: [],
  words: [],
  sourceSegmentIds: [id],
});

describe("transcript export", () => {
  it("preserves the reader's visible prose and gives turns speaker and time context", () => {
    const text = transcriptMarkdown(
      { title: "Planning", dateLabel: "2026-09-24" },
      [segment("a", "Ana", 1000, "Let's ship this."), segment("b", "Ben", 9000, "Agreed.")],
    );
    expect(text).toBe("# Planning — 2026-09-24\n\n**Ana** [0:01]\n\nLet's ship this.\n\n**Ben** [0:09]\n\nAgreed.\n");
  });

  it("keeps Markdown syntax in meeting text literal", () => {
    const markdown = transcriptMarkdown(
      { title: "Review [draft]", dateLabel: "" },
      [segment("a", "A*na", 0, "Use *literal* and <tags>.")],
    );
    expect(markdown).toContain(String.raw`# Review \[draft\]`);
    expect(markdown).toContain(String.raw`**A\*na** [0:00]`);
    expect(markdown).toContain(String.raw`Use \*literal\* and \<tags\>.`);
  });

  it("names an empty transcript and makes distinct safe file names", () => {
    expect(transcriptMarkdown(null, [])).toBe("# Meeting transcript\n\nNo transcript available.\n");
    expect(safeMeetingStem({ title: "Review / launch", id: "id:1" })).toBe("Review-launch-id-1");
    expect(safeMeetingStem({ title: "Qualitätssicherung", id: "one" })).toBe("Qualitätssicherung-one");
    expect(safeMeetingStem({ title: "Daily Standup", id: "Daily-Standup--2026-03-13" })).toBe("Daily-Standup--2026-03-13");
    expect(safeMeetingStem({ title: "Review / launch", id: "id:2" })).not.toBe(
      safeMeetingStem({ title: "Review / launch", id: "id:1" }),
    );
  });
});
