import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

import { buildTranscriptRows, type OverlapBlock, type TranscriptRow } from "../core/overlap";
import { formatClockTime } from "../core/transcript";
import { shreddedDoubleTalkSegments } from "../core/fixtures/shreddedDoubleTalk";

/**
 * What the transcript pane actually puts on the page, for the transcript the
 * pipeline really emits (D-693).
 *
 * The pane is a Svelte component with no DOM harness in this project, so the
 * assertions run against the row builder it renders — buildTranscriptRows, the
 * only thing between the turn model and the markup — plus `renderRow` below,
 * which is a transcription of the template rather than a second opinion about
 * it. Anything the template can do that the row cannot express (a class, a
 * colour) is not asserted here; anything a reader can READ is.
 *
 * The fixture is the shredded double-talk one: public synthetic audio from a
 * scenario committed in this repo, 44 producer blocks whose ground truth is
 * known exactly. See src/core/fixtures/shreddedDoubleTalk.ts.
 */

/** The words of one block, as the page sets them. */
function wordsOf(block: OverlapBlock): string {
  return (block.words ?? []).map((word) => word.text).join(" ");
}

/**
 * One row as the reader meets it: a header line (speaker, clock, and who this
 * turn ran over) and then the WHOLE TURN as one paragraph, with every
 * interjection inline at its seam.
 *
 * The chip text is `(Name: words)` on purpose — the parens and the name are
 * real text nodes in the template, so this is also what lands in the clipboard
 * when a reader selects across the paragraph.
 */
function renderRow(row: TranscriptRow<OverlapBlock>): { header: string; paragraph: string } {
  const over = row.over.length > 0 ? `  over ${row.over.join(" and ")}` : "";
  const paragraph = row.members
    .map((member) =>
      member.kind === "speech"
        ? wordsOf(member.block)
        : `(${member.speakerLabel}: ${member.blocks.map(wordsOf).join(" ")})`,
    )
    .join(" ");
  return {
    header: `${row.speakerLabel}  ${formatClockTime(row.startMs)}${over}`,
    paragraph,
  };
}

function blockIdsIn(row: TranscriptRow<OverlapBlock>): string[] {
  return row.members.flatMap((member) =>
    member.kind === "speech" ? [member.block.id] : member.blocks.map((block) => block.id),
  );
}

const rows = buildTranscriptRows(shreddedDoubleTalkSegments);
const rowsByKey = new Map(rows.map((row) => [row.key, row]));

describe("what the transcript pane renders", () => {
  it("reads sustained double talk as one paragraph per speaker", () => {
    // Ground truth: Cara speaks one sentence 41.0-49.0 s and Ben a different,
    // competing sentence 43.2-49.0 s. The producer shredded the two into 31
    // alternating fragments of one to three words, which the pane used to
    // render as 31 paragraphs with 31 headers and 31 badges.
    const collision = rows.filter((row) => row.startMs >= 40_000 && row.startMs < 50_000);

    expect(collision.map((row) => row.speakerLabel)).toEqual(["Cara Lindqvist", "Ben Okafor"]);
    expect(collision.map((row) => blockIdsIn(row).length)).toEqual([16, 15]);
    expect(renderRow(collision[0]!).paragraph).toBe(
      "So the only thing I still need from you is f the final sign off on the wording, because once it goes out, we cannot quietly edit it afterwards.",
    );
    expect(renderRow(collision[1]!).paragraph).toBe(
      "Right, but hold on. I thought we agreed the wording was already settled last week when we went through it.",
    );
    // One header apiece, and the fragments carry none of their own: a row is
    // rendered as a single <p>, so there is nothing else it could be.
    expect(collision.map((row) => renderRow(row).header)).toEqual([
      "Cara Lindqvist  0:40  over Ben Okafor",
      "Ben Okafor  0:43  over Cara Lindqvist",
    ]);
  });

  it("names the other speaker on both sides of a genuine collision", () => {
    // A real floor change, not a backchannel: Ben takes the floor off Cara
    // 0.23 s before she finishes.
    expect(rowsByKey.get("seg_000005")?.over).toEqual(["Ben Okafor"]);
    expect(rowsByKey.get("seg_000008")?.over).toEqual(["Cara Lindqvist"]);
    // And the marker is a name. Nothing else.
    expect(rows.flatMap((row) => [...row.over]).every((label) => /^[\p{L} .'-]+$/u.test(label))).toBe(
      true,
    );
  });

  it("puts a backchannel inline in the turn it landed in, not on a row of its own", () => {
    const host = rowsByKey.get("seg_000000")!;

    expect(renderRow(host).paragraph).toBe(
      "Let me walk through where the release actually stands today. The installer (Ben Okafor: Right.) is finished, and the documentation has already been merged.",
    );
    // Ben's "Right." is IN Ana's paragraph and is not a row anywhere.
    expect(rows.some((row) => row.key === "seg_000001")).toBe(false);
    expect(rows.flatMap(blockIdsIn)).toContain("seg_000001");
    // Ana's sentence is whole again around it: before the fix her turn's second
    // half opened mid-clause, as a fresh paragraph with a fresh header.
    expect(host.members.map((member) => member.kind)).toEqual([
      "speech",
      "interjection",
      "speech",
    ]);
    // The same shape for the other backchannel, inside Cara's turn.
    expect(renderRow(rowsByKey.get("seg_000005")!).paragraph).toContain(
      "post the (Ana Duarte: Perfect.) link in the channel",
    );
  });

  it("leaves a clean sequential exchange exactly as it was", () => {
    // 13.0-19.3 s and 52.5-58.4 s: ordinary turns, seconds of silence between
    // them, nobody overlapping anybody. One block, one row, one paragraph, no
    // chip and no marker — the same page as before this feature existed.
    const clean = rows.filter(
      (row) =>
        (row.startMs >= 13_000 && row.startMs < 20_000) ||
        (row.startMs >= 33_000 && row.startMs < 39_000) ||
        row.startMs >= 52_000,
    );

    expect(clean.map((row) => renderRow(row).header)).toEqual([
      "Ben Okafor  0:13",
      "Ana Duarte  0:18",
      "Cara Lindqvist  0:33",
      "Ana Duarte  0:37",
      "Ana Duarte  0:52",
      "Cara Lindqvist  0:55",
    ]);
    expect(clean.map(blockIdsIn)).toEqual([
      ["seg_000003"],
      ["seg_000004"],
      ["seg_000009"],
      ["seg_000010"],
      ["seg_000042"],
      ["seg_000043"],
    ]);
    expect(clean.flatMap((row) => row.members.map((member) => member.kind))).toEqual(
      Array.from({ length: 6 }, () => "speech"),
    );
  });

  it("loses no block, no id and no ordering on the way to the page", () => {
    // 44 producer blocks in, 44 rendered — each exactly once, under 11 headers
    // instead of 44. Every one keeps its id, so every one keeps its seek
    // anchors, its scroll target and its playback ring.
    expect(rows).toHaveLength(11);
    const rendered = rows.flatMap(blockIdsIn);
    expect([...rendered].sort()).toEqual(
      shreddedDoubleTalkSegments.map((block) => block.id).sort(),
    );
    // Within a row, always in the order they were spoken. ACROSS rows they are
    // not: while two people talk at once their fragments interleave on the
    // tape, and putting each speaker's own back in one piece is the point.
    for (const row of rows) {
      const starts = row.members.flatMap((member) =>
        member.kind === "speech" ? [member.block.startMs] : member.blocks.map((block) => block.startMs),
      );
      expect(starts).toEqual([...starts].sort((left, right) => left - right));
    }
  });

  it("prints no duration anywhere in the transcript", () => {
    // The measurement stays in the model; the page never quotes it. This is the
    // whole of what the reader is shown, headers and prose alike.
    const page = rows.map((row) => `${renderRow(row).header}\n${renderRow(row).paragraph}`).join("\n");

    expect(page).not.toMatch(/\d+(?:[.,]\d+)?\s*(?:s|ms|sec|secs|seconds?)\b/);
    expect(page).not.toContain("during");
    expect(page).not.toContain("continues past");
  });
});

describe("the transcript pane's markup", () => {
  // The component has no DOM harness here, so these read the template itself.
  // They exist because the thing being fixed was a rendering decision: the
  // badges have to be GONE, not merely unused.
  const template = readFileSync(new URL("./MeetingView.svelte", import.meta.url), "utf8");

  it("carries none of the badges, borders or indentation the durations lived on", () => {
    expect(template).not.toContain("formatOverlapDuration");
    expect(template).not.toContain("describeOverlap");
    expect(template).not.toContain("describeResumption");
    expect(template).not.toContain("groupInterruptedTurns");
    expect(template).not.toContain("continues past");
    // The badge glyphs: ⇄ (&#8644;) and ↳ (&#8627;).
    expect(template).not.toContain("&#8644;");
    expect(template).not.toContain("&#8627;");
    // The group rule and the alternating inset of the previous attempt.
    expect(template).not.toContain("border-l-2 border-primary/50");
    expect(template).not.toContain("ml-3 pl-3 border-l-2");
  });

  it("gives every chip a screen-reader prefix that never reaches the clipboard", () => {
    // Read aloud as "Interjection by Ben Okafor: Right.", copied as
    // "(Ben Okafor: Right.)" — the prefix is select-none, the parens are real
    // text nodes marked aria-hidden so they are punctuation, not words.
    expect(template).toContain('<span class="sr-only select-none">Interjection by </span>');
    expect(template).toContain('<span aria-hidden="true">(</span>');
    expect(template).toContain('<span aria-hidden="true">)</span>');
  });

  it("keeps the transcript pane a live region with static rows inside it", () => {
    expect(template).toContain('role="log"');
    expect(template).toContain('<span class="sr-only">Simultaneous speech: </span>over ');
  });
});
