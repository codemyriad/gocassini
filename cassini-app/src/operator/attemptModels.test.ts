import { describe, expect, it } from "vitest";
import { attemptLogs, attemptReason, attemptSpeech, attemptSummary, speechModelLabel, summaryModelLabel } from "./attemptModels";
import type { JobAttempt } from "./types";

const attempt = (fields: Partial<JobAttempt>) => fields as JobAttempt;

describe("attempt models", () => {
  it("names the speech model and where it ran", () => {
    expect(speechModelLabel(attempt({ transcription_model: "parakeet-tdt-0.6b-v3", transcription_device: "cuda" }))).toBe("parakeet-tdt-0.6b-v3 on GPU");
    expect(speechModelLabel(attempt({ transcription_model: "parakeet-tdt-0.6b-v3", transcription_device: "cpu" }))).toBe("parakeet-tdt-0.6b-v3 on CPU");
    expect(speechModelLabel(attempt({ transcription_model: "parakeet-tdt-0.6b-v3" }))).toBe("parakeet-tdt-0.6b-v3");
  });

  it("says nothing for a step that recorded no model", () => {
    expect(speechModelLabel(attempt({ transcription_device: "cpu" }))).toBeNull();
    expect(summaryModelLabel(attempt({ summary_model: "  " }))).toBeNull();
    expect(summaryModelLabel(attempt({ summary_model: "gpt-4.1-mini" }))).toBe("gpt-4.1-mini");
  });
});

describe("attempt logs", () => {
  it("lists every step's log, in pipeline order, and which cannot be opened", () => {
    const logs = attemptLogs(attempt({
      publish_log_path: "/runs/a.logs/publish.log",
      record_log_path: "/runs/a.logs/record.log",
      build_log_path: "/runs/a.logs/build.log",
      seal_log_path: null,
      files_present: { record_log: false, build_log: true },
    }));
    expect(logs).toEqual([
      { stage: "record", label: "Record", written: true, present: false },
      { stage: "build", label: "Build", written: true, present: true },
      { stage: "seal", label: "Seal", written: false, present: false },
      { stage: "publish", label: "Publish", written: true, present: true },
    ]);
  });
});

describe("what an attempt did, by its kind", () => {
  const stop = (reason: string) => (reason === "room_empty" ? "Room empty" : reason);

  it("says why it ran", () => {
    expect(attemptReason(attempt({ trigger_kind: "initial", record_finished_at: "t", stop_reason: "room_empty" }), stop)).toBe("Recording ended · Room empty");
    expect(attemptReason(attempt({ trigger_kind: "initial" }), stop)).toBe("Recording");
    expect(attemptReason(attempt({ trigger_kind: "rerun" }), stop)).toBe("Rerun requested");
    expect(attemptReason(attempt({ trigger_kind: "summarize" }), stop)).toBe("Summary requested");
    expect(attemptReason(attempt({ trigger_kind: "refine", summary_mode: "rewrite" }), stop)).toBe("Speakers renamed, summary rewritten");
    expect(attemptReason(attempt({ trigger_kind: "refine" }), stop)).toBe("Speakers renamed, summary kept");
  });

  it("says a transcript was reused, and from which attempt, rather than name a model that did not run", () => {
    const attempts = [
      attempt({ attempt_number: 1, trigger_kind: "initial", state: "succeeded" }),
      attempt({ attempt_number: 2, trigger_kind: "rerun", state: "failed" }),
    ];
    const summary = attempt({ attempt_number: 3, trigger_kind: "summarize", transcription_model: "parakeet-tdt-0.6b-v3" });
    expect(attemptSpeech(summary, attempts)).toEqual({ text: "Reused from attempt 1", muted: true });
    expect(attemptSpeech(attempt({ trigger_kind: "rerun", transcription_model: "parakeet-tdt-0.6b-v3", transcription_device: "cuda" }), attempts))
      .toEqual({ text: "parakeet-tdt-0.6b-v3 on GPU" });
    expect(attemptSpeech(attempt({ trigger_kind: "initial" }), attempts)).toBeNull();
  });

  it("says what the summary step did when it names no model", () => {
    expect(attemptSummary(attempt({ trigger_kind: "summarize", summary_status: "completed", summary_model: "gpt-4.1-mini" }))).toEqual({ text: "gpt-4.1-mini" });
    expect(attemptSummary(attempt({ trigger_kind: "initial", summary_status: "skipped", summary_reason: "disabled" })))
      .toEqual({ text: "Skipped: summaries are turned off", muted: true });
    expect(attemptSummary(attempt({ trigger_kind: "initial", summary_status: "failed", summary_reason: "llm_failed" }))?.text).toBe("Failed: the model returned an error");
    expect(attemptSummary(attempt({ trigger_kind: "refine" }))).toEqual({ text: "Kept as it was", muted: true });
    expect(attemptSummary(attempt({ trigger_kind: "initial" }))).toBeNull();
  });
});
