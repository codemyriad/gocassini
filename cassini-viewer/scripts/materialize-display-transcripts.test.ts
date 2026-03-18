import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import {
  findMeetingArtifactDirs,
  materializeDisplayTranscriptForMeeting,
} from "./materialize-display-transcripts.mjs";

describe("materialize-display-transcripts", () => {
  it("finds meeting artifact directories under a source root", () => {
    const root = mkdtempSync(join(tmpdir(), "cassini-materialize-find-"));
    try {
      mkdirSync(join(root, "meeting-a"), { recursive: true });
      mkdirSync(join(root, "meeting-b"), { recursive: true });
      writeFileSync(join(root, "meeting-a", "transcript.words.v1.json"), "{}\n", "utf8");
      writeFileSync(join(root, "meeting-b", "transcript.words.v1.json"), "{}\n", "utf8");

      const dirs = findMeetingArtifactDirs(root);

      expect(dirs).toEqual([join(root, "meeting-a"), join(root, "meeting-b")]);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("writes transcript.display.v1.json next to transcript artifacts", () => {
    const root = mkdtempSync(join(tmpdir(), "cassini-materialize-display-"));
    try {
      mkdirSync(root, { recursive: true });
      writeFileSync(
        join(root, "transcript.words.v1.json"),
        `${JSON.stringify({
          version: "transcript.words.v1",
          media: { src: "meeting.opus", durationMs: 3000 },
          speakers: [{ id: "spk_1", label: "Alice" }],
          segments: [
            {
              id: "seg_1",
              speaker: "spk_1",
              startMs: 1000,
              endMs: 1800,
              text: "um hello there",
              words: [
                { id: "w_1", text: "um", startMs: 1000, endMs: 1100 },
                { id: "w_2", text: "hello", startMs: 1200, endMs: 1450 },
                { id: "w_3", text: "there", startMs: 1450, endMs: 1800 },
              ],
            },
          ],
        }, null, 2)}\n`,
        "utf8",
      );
      writeFileSync(
        join(root, "transcript.readable.v1.json"),
        `${JSON.stringify({
          version: "transcript.readable.v1",
          media: { src: "meeting.opus", durationMs: 3000 },
          speakers: [{ id: "spk_1", label: "Alice" }],
          segments: [
            {
              id: "rseg_1",
              speaker: "spk_1",
              startMs: 1000,
              endMs: 1800,
              text: "Hello there.",
              sourceSegmentIds: ["seg_1"],
            },
          ],
        }, null, 2)}\n`,
        "utf8",
      );

      const display = materializeDisplayTranscriptForMeeting(root);
      const written = JSON.parse(readFileSync(join(root, "transcript.display.v1.json"), "utf8"));

      expect(display.version).toBe("transcript.display.v1");
      expect(written.blocks[0]?.tokens.map((token) => token.text)).toEqual(["Hello", "there", "."]);
      expect(written.blocks[0]?.timingCoverage).toBe(1);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
