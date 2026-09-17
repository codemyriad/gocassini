import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import TranscriptWords from "./TranscriptWords.svelte";
import { createWordHighlighter } from "../core/wordHighlight";
import type { TranscriptWordPart } from "../core/wordInteraction";
import type { PlacedMark } from "./marking/session";

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
    // Untimed words still carry their id, so a marked stretch and find can reach them.
    expect(html).toMatch(/<span data-word-id="rewritten">Revised<\/span>/);
    expect(html).toMatch(/<span data-word-id="untimed">confirmed\.<\/span>/);
  });

  it("puts a tagged section's chip in front of its first word, and only there", () => {
    const mark = {
      item: { id: "a1", actor: { id: "maya" } },
      tag: { id: "t1", label: "budget" },
      color: "teal",
      icon: "",
      startMs: 260,
      endMs: 750,
      column: 0,
    } as unknown as PlacedMark;
    const html = render(TranscriptWords, {
      props: {
        parts: [
          { id: "first", text: "A", prefix: "", startMs: 100, endMs: 250 },
          { id: "second", text: "better", prefix: " ", startMs: 260, endMs: 500 },
          { id: "third", text: "plan", prefix: " ", startMs: 499, endMs: 750 },
        ],
        speakerLabel: "Maya",
        highlighter: createWordHighlighter(),
        seek: () => {},
        chips: new Map([["second", [mark]]]),
      },
    }).body;
    expect(html.match(/cassini-tag-start/g)).toHaveLength(1);
    expect(html.indexOf("cassini-tag-start")).toBeGreaterThan(html.indexOf('data-gap-for="second"'));
    expect(html.indexOf("cassini-tag-start")).toBeLessThan(html.indexOf('data-word-id="second"'));
    expect(html).toContain('aria-label="budget, 0:00 to 0:00, marked by maya. Open it."');
    // Without chips, the words are all there is.
    expect(renderWords([{ id: "second", text: "better", prefix: "", startMs: 260, endMs: 500 }])).not.toContain("cassini-tag-start");
  });
});
