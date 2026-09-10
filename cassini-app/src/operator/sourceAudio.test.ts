import { describe, expect, it } from "vitest";
import { audioUseLabel, sourceAudioProgress, transcriptUseLabel } from "./sourceAudio";
import type { Job, SourceAudioUse } from "./types";

const use: SourceAudioUse = { owner: "alice", segments: 2, placed: 1, skipped: 1,
  spliced_ms: 12000, mix_spliced: true, transcript_source: "merged-mix" };

describe("participant audio evidence", () => {
  it("does not turn successful storage or a finished job into proof of ingestion", () => {
    const job = {stage: "done", state: "succeeded", source_audio_rebuild: {pending: true}} as Job;
    expect(sourceAudioProgress(job, true)).toContain("before rebuilding");
    expect(sourceAudioProgress(job, false)).toContain("disabled");
  });
  it("distinguishes a mixed transcript from individual speaker attribution", () => {
    expect(transcriptUseLabel(use)).toBe("Used in the transcribed mix");
    expect(transcriptUseLabel({...use, transcript_source: "per-participant"})).toContain("participant’s transcript");
    expect(transcriptUseLabel({...use, transcript_source: undefined})).toBe("Use not confirmed");
    expect(transcriptUseLabel({...use, spliced_ms: 0})).toBe("Not used");
  });
  it("can show transcription use when the published mix opted out", () => {
    const report = {...use, mix_spliced: false, transcript_source: "per-participant"};
    expect(audioUseLabel(report)).toBe("Recorded call audio retained");
    expect(transcriptUseLabel(report)).toContain("participant’s transcript");
  });
});
