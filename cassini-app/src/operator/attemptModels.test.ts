import { describe, expect, it } from "vitest";
import { attemptLogs, speechModelLabel, summaryModelLabel } from "./attemptModels";
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
  it("lists the logs an attempt wrote, in pipeline order, and which are gone", () => {
    const logs = attemptLogs(attempt({
      publish_log_path: "/runs/a.logs/publish.log",
      record_log_path: "/runs/a.logs/record.log",
      build_log_path: "/runs/a.logs/build.log",
      seal_log_path: null,
      files_present: { record_log: false, build_log: true },
    }));
    expect(logs).toEqual([
      { stage: "record", label: "Record", present: false },
      { stage: "build", label: "Build", present: true },
      { stage: "publish", label: "Publish", present: true },
    ]);
  });
});
