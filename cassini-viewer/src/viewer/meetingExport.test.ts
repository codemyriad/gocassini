import { describe, expect, it } from "vitest";
import { transcriptText, safeMeetingStem } from "./meetingExport";
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
    const text = transcriptText(
      { title: "Planning", dateLabel: "2026-09-24" },
      [segment("a", "Ana", 1000, "Let's ship this."), segment("b", "Ben", 9000, "Agreed.")],
    );
    expect(text).toBe("Planning — 2026-09-24\n\nAna  0:01\nLet's ship this.\n\nBen  0:09\nAgreed.\n");
  });

  it("names an empty transcript and makes distinct safe file names", () => {
    expect(transcriptText(null, [])).toBe("Meeting transcript\n\nNo transcript available.\n");
    expect(safeMeetingStem({ title: "Review / launch", id: "id:1" })).toBe("Review-launch-id-1");
    expect(safeMeetingStem({ title: "Qualitätssicherung", id: "one" })).toBe("Qualitätssicherung-one");
    expect(safeMeetingStem({ title: "Daily Standup", id: "Daily-Standup--2026-03-13" })).toBe("Daily-Standup--2026-03-13");
    expect(safeMeetingStem({ title: "Review / launch", id: "id:2" })).not.toBe(
      safeMeetingStem({ title: "Review / launch", id: "id:1" }),
    );
  });
});
