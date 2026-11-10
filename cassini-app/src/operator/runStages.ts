// The steps of a run as the meeting stands now (D-817): each one taken from
// the attempt that produced it, so a summary written later does not replace
// the build that transcribed the meeting. An attempt still running, or the
// one that failed last, shows over the steps it reached; the rest keep what
// is live.
import { buildTimeWithoutSummary, processesCapture, spanMs, stageTimes, stepTime, summaryTime } from "./processingSpeed";
import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";
import type { Job, JobAttempt, JobDetailResponse } from "./types";

const copy = () => get(LL).app.operator.run_stages;

// "skipped" is a step that was decided not to run, such as a summary with
// summaries turned off: settled, unlike a step not reached yet.
export type StageStatus = "pending" | "queued" | "active" | "done" | "failed" | "blocked" | "skipped";

export const ATTEMPT_STEPS = ["record", "build", "seal", "publish"] as const;
export type AttemptStep = (typeof ATTEMPT_STEPS)[number];

export interface StageView {
  key: string;
  label: string;
  status: StageStatus;
  /** How long the step took, once it finished. */
  ms: number | null;
  /** Said in place of a time, when the step has none of its own. */
  note?: string;
  /** Why the step reads as it does, when its note is not enough. */
  hint?: string;
  /** The attempt the step comes from. */
  attempt?: number;
}

// One attempt's steps: done once finished, active once started, queued once
// queued. A halted attempt's furthest step is where it failed or is blocked.
export function stepStatuses(row: Job | JobAttempt): Record<AttemptStep, StageStatus> {
  const blocked = row.stage === "build" && row.state === "blocked";
  const halted = blocked || row.state === "failed" || row.state === "stopped" || row.state === "interrupted";
  const statuses = {} as Record<AttemptStep, StageStatus>;
  let furthest: AttemptStep | null = null;
  for (const key of ATTEMPT_STEPS) {
    const t = stageTimes(row, key);
    statuses[key] = t.finished ? "done" : t.started ? "active" : t.queued ? "queued" : "pending";
    if (t.queued || t.started || t.finished) furthest = key;
  }
  if (halted && furthest) statuses[furthest] = blocked ? "blocked" : "failed";
  return statuses;
}

// Where an unfinished or failed attempt's steps land on the run's bar. A
// summary-only attempt's build writes the summary. A speaker-edit replay's
// build shows over Build while it runs or once it failed, so the run says so;
// once it succeeds it never stands for the build that transcribed the meeting.
const RUN_STEP_FOR: Record<string, Partial<Record<AttemptStep, string>>> = {
  initial: { record: "record", build: "build", seal: "seal", publish: "publish" },
  rerun: { build: "build", seal: "seal", publish: "publish" },
  summarize: { build: "summary", seal: "seal", publish: "publish" },
  refine: { build: "build", seal: "seal", publish: "publish" },
};

// Getters, so each read is in the reader's language.
const SUMMARY_NOTES: Record<string, string> = {
  get completed() { return copy().summary_note.completed(); },
  get skipped() { return copy().summary_note.skipped(); },
  get failed() { return copy().summary_note.failed(); },
};

const SUMMARY_STATUSES: Record<string, StageStatus> = {
  completed: "done",
  skipped: "skipped",
  failed: "failed",
};

const SUMMARY_REASONS: Record<string, string> = {
  get disabled() { return copy().summary_reason.disabled(); },
  get not_configured() { return copy().summary_reason.not_configured(); },
  get no_transcript() { return copy().summary_reason.no_transcript(); },
  get llm_failed() { return copy().summary_reason.llm_failed(); },
};

function newest<T>(items: T[]): T | undefined {
  return items[items.length - 1];
}

function fromAttempt(key: string, label: string, attempt: JobAttempt, step: AttemptStep): StageView {
  return { key, label, status: "done", ms: stepTime(attempt, step), attempt: attempt.attempt_number };
}

export function runStages(detail: JobDetailResponse, recordingDeleted = false): StageView[] {
  const { job } = detail;
  const attempts = detail.attempts.length > 0
    ? [...detail.attempts].sort((a, b) => a.attempt_number - b.attempt_number)
    : [{ ...job, attempt_number: job.current_attempt_number, trigger_kind: job.current_trigger_kind ?? "initial" } as unknown as JobAttempt];
  const succeeded = attempts.filter((a) => a.state === "succeeded");
  // The build that transcribed the meeting, and the attempt that published it as it is now.
  const capture = newest(succeeded.filter(processesCapture));
  const published = newest(succeeded);
  // A summary written after that build, alone or while replaying speaker edits.
  const laterSummary = newest(
    succeeded.filter(
      (a) => a.attempt_number > (capture?.attempt_number ?? 0)
        // A speaker-edit save that kept the summary still carries it, and one
        // asked to rewrite it may have failed to, keeping it too: only the
        // times of a rewrite that happened (D-899) say it wrote one.
        && (a.trigger_kind === "summarize" || (a.trigger_kind === "refine" && a.summary_mode === "rewrite" && summaryTime(a) !== null)),
    ),
  );

  const recordMs = spanMs(job.record_started_at, job.record_finished_at);
  const t = copy();
  const stages: Record<string, StageView> = {
    record: recordingDeleted
      ? { key: "record", label: t.record_deleted(), status: "pending", ms: null }
      : { key: "record", label: t.stage.record(), status: stepStatuses(job).record, ms: recordMs },
    // Without the summary it wrote, which has a step of its own.
    build: capture
      ? { ...fromAttempt("build", t.stage.build(), capture, "build"), ms: buildTimeWithoutSummary(capture) }
      : { key: "build", label: t.stage.build(), status: "pending", ms: null },
    summary: { key: "summary", label: t.stage.summary(), status: "pending", ms: null },
    seal: published ? fromAttempt("seal", t.stage.seal(), published, "seal") : { key: "seal", label: t.stage.seal(), status: "pending", ms: null },
    publish: published ? fromAttempt("publish", t.stage.publish(), published, "publish") : { key: "publish", label: t.stage.publish(), status: "pending", ms: null },
  };
  if (laterSummary) {
    // Timed on its own since D-899. Before, the build that wrote it stands
    // for it: a speaker rename replays in seconds, so the rest of that build
    // is the summary being written again.
    stages.summary = fromAttempt("summary", t.stage.summary(), laterSummary, "build");
    stages.summary.ms = summaryTime(laterSummary) ?? stages.summary.ms;
    if (laterSummary.trigger_kind === "refine") stages.summary.hint = t.summary_rewritten_after_rename();
  } else if (capture?.summary_status) {
    // Written in the same build as the transcript: timed on its own since
    // D-899, before which the build was timed as one step.
    const ms = summaryTime(capture);
    stages.summary = {
      key: "summary",
      label: t.stage.summary(),
      status: SUMMARY_STATUSES[capture.summary_status] ?? "pending",
      ms,
      note: ms !== null && capture.summary_status === "completed" ? undefined : SUMMARY_NOTES[capture.summary_status],
      hint: capture.summary_reason ? SUMMARY_REASONS[capture.summary_reason] : ms !== null ? t.summary_written_in_build() : undefined,
      attempt: capture.attempt_number,
    };
  }

  // The newest attempt, if it has not succeeded, shows over the steps it reached.
  const current = newest(attempts);
  if (current && current.state !== "succeeded") {
    const statuses = stepStatuses(current);
    const mapping = RUN_STEP_FOR[current.trigger_kind] ?? {};
    for (const step of ATTEMPT_STEPS) {
      const key = mapping[step];
      if (!key || statuses[step] === "pending" || (key === "record" && recordingDeleted)) continue;
      stages[key] = {
        key,
        label: stages[key].label,
        status: statuses[step],
        // The run's Build leaves the summary out, here as when published.
        ms: key === "build" ? buildTimeWithoutSummary(current) : stepTime(current, step),
        attempt: current.attempt_number,
      };
    }
  }
  return [stages.record, stages.build, stages.summary, stages.seal, stages.publish];
}
