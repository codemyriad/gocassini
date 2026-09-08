import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import TranscriptWords from "./TranscriptWords.svelte";
import { createWordHighlighter } from "../core/wordHighlight";
import type { TranscriptWordPart } from "../core/wordInteraction";

function renderWords(parts: TranscriptWordPart[]) {
  return render(TranscriptWords, {
    props: { parts, speakerLabel: "Maya", highlighter: createWordHighlighter(), seek: () => {} },
  }).body;
}
const plainText = (html: string) => html.replace(/<[^>]*>/g, "");

describe("the actual shared word renderer", () => {
  it("renders separate native buttons with exact seek targets and keeps punctuation outside them", () => {
    const html = renderWords([
      { id: "first", text: "A", prefix: "", startMs: 100, endMs: 250 },
      { id: "second", text: "better", prefix: " ", startMs: 260, endMs: 500 },
      { id: "third", text: "plan", prefix: " ", startMs: 499, endMs: 750 },
      { id: "punctuation", text: ".", prefix: "" },
    ]);
    expect(html.match(/<button\b/g)).toHaveLength(3);
    expect(html).toContain('type="button"');
    expect(html).toContain('data-start-ms="499"');
    expect(html).toContain('data-end-ms="750"');
    expect(html).toContain('aria-label="plan — seek to 0:00, Maya"');
    expect(html).toMatch(/>plan<\/button>/);
    expect(plainText(html)).toBe("A better plan.");
  });

  it("keeps cleaned and untimed display words readable without inventing word controls", () => {
    const html = renderWords([
      { id: "rewritten", text: "Revised", prefix: "" },
      { id: "timed", text: "plan", prefix: " ", startMs: 1000, endMs: 1500, alignment: "interpolated" },
      { id: "untimed", text: "confirmed.", prefix: " " },
    ]);
    expect(plainText(html)).toBe("Revised plan confirmed.");
    expect(html.match(/<button\b/g)).toHaveLength(1);
    expect(html).toContain("cassini-word-interpolated");
    expect(html).toContain('title="Estimated word timing"');
    expect(html).toMatch(/<span>Revised<\/span>/);
    expect(html).toMatch(/<span>confirmed\.<\/span>/);
  });
});
