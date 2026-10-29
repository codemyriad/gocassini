// How long processing took against the length of the meeting (D-817), so an
// admin can tell whether their hardware keeps up. The multiplier reads the way
// the transcription research does: 4.8× real time means a 58 minute meeting
// took 12 minutes, and below 1× the server falls behind the meetings it records.
import type { Job, JobAttempt, JobDetailResponse } from "./types";

type StageTimes = Job | JobAttempt;

// Only these attempt kinds process the capture. A summarize writes a summary
// and a refine replays speaker edits; timing either against the meeting would
// read as hardware many times faster than it is.
const CAPTURE_KINDS = new Set(["initial", "rerun"]);

export function processesCapture(attempt: JobAttempt): boolean {
  return CAPTURE_KINDS.has(attempt.trigger_kind);
}

export interface Speed {
  ms: number;
  /** Meeting length over processing time; null when either is zero. */
  factor: number | null;
}

export function spanMs(start: string | null | undefined, end: string | null | undefined): number | null {
  if (!start || !end) return null;
  const from = Date.parse(start);
  const to = Date.parse(end);
  if (Number.isNaN(from) || Number.isNaN(to) || to < from) return null;
  return to - from;
}

export function formatSpan(ms: number): string {
  const totalSeconds = Math.round(ms / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const totalMinutes = Math.floor(totalSeconds / 60);
  const remSeconds = totalSeconds % 60;
  if (totalMinutes < 60) {
    return remSeconds > 0 ? `${totalMinutes}m ${remSeconds}s` : `${totalMinutes}m`;
  }
  const hours = Math.floor(totalMinutes / 60);
  const remMinutes = totalMinutes % 60;
  return remMinutes > 0 ? `${hours}h ${remMinutes}m` : `${hours}h`;
}

export function formatMultiplier(factor: number): string {
  // Rounded before choosing the precision, or 9.97 would read "10.0×".
  const tenths = Math.round(factor * 10) / 10;
  return `${tenths < 10 ? tenths.toFixed(1) : Math.round(factor).toLocaleString()}×`;
}

export function formatFactor(factor: number): string {
  return `${formatMultiplier(factor)} real time`;
}

// The meeting is as long as its recording ran. Reruns do not record, so this
// reads the job, which keeps the first attempt's record times.
export function meetingLengthMs(job: Job): number | null {
  return spanMs(job.record_started_at, job.record_finished_at);
}

function speed(ms: number | null, meetingMs: number | null): Speed | null {
  if (ms === null) return null;
  return { ms, factor: meetingMs && ms > 0 ? meetingMs / ms : null };
}

// Reads `${key}_queued_at` / `_started_at` / `_finished_at`, so a stage has
// times only if the operator names its columns to match.
export function stageTimes(row: StageTimes, key: string) {
  const r = row as unknown as Record<string, string | null | undefined>;
  return {
    queued: r[`${key}_queued_at`] ?? null,
    started: r[`${key}_started_at`] ?? null,
    finished: r[`${key}_finished_at`] ?? null,
  };
}

// One step's time, once it has finished. A failed step is stamped finished
// too, so the caller passes a meeting length only for a step that succeeded.
export function stepSpeed(row: StageTimes, key: string, meetingMs: number | null): Speed | null {
  const t = stageTimes(row, key);
  return speed(spanMs(t.started, t.finished), meetingMs);
}

// From the build starting to the meeting being published. Time spent queued
// or waiting for resources before the build is not processing.
export function processingSpeed(row: StageTimes, meetingMs: number | null): Speed | null {
  return speed(spanMs(stageTimes(row, "build").started, stageTimes(row, "publish").finished), meetingMs);
}

// The run's figure: the newest attempt that processed the capture and
// published it. A failed publish is stamped finished too, so the state decides.
export function runProcessing(detail: JobDetailResponse): Speed | null {
  const meetingMs = meetingLengthMs(detail.job);
  const attempts = [...detail.attempts].sort((a, b) => b.attempt_number - a.attempt_number);
  for (const attempt of attempts) {
    if (!processesCapture(attempt) || attempt.state !== "succeeded") continue;
    const result = processingSpeed(attempt, meetingMs);
    if (result) return result;
  }
  return null;
}

// "12m · 4.8× real time": the meeting's length is shown beside it already.
export function describeProcessing(result: Speed): string {
  if (result.factor === null) return formatSpan(result.ms);
  return `${formatSpan(result.ms)} · ${formatFactor(result.factor)}`;
}
