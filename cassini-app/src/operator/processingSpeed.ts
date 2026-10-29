// How long processing took against the length of the meeting (D-817), so an
// admin can tell whether their hardware keeps up. The multiplier reads the way
// the transcription research does: 4.8× real time means a 58 minute meeting
// took 12 minutes, and below 1× the server falls behind the meetings it records.
import type { Job, JobAttempt, JobDetailResponse } from "./types";

type StageTimes = Job | JobAttempt;

export const PROCESSING_STEPS = ["build", "seal", "publish"] as const;

// Only these attempt kinds process the capture. A summarize writes a summary
// and a refine replays speaker edits; timing either against the meeting would
// read as hardware many times faster than it is.
const CAPTURE_KINDS = new Set(["initial", "rerun"]);

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
  return `${factor < 10 ? factor.toFixed(1) : Math.round(factor).toLocaleString()}×`;
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

function times(row: StageTimes, key: string) {
  const r = row as unknown as Record<string, string | null | undefined>;
  return { started: r[`${key}_started_at`], finished: r[`${key}_finished_at`] };
}

// One step's time, once it has finished.
export function stepSpeed(row: StageTimes, key: string, meetingMs: number | null): Speed | null {
  const t = times(row, key);
  return speed(spanMs(t.started, t.finished), meetingMs);
}

// From the build starting to the meeting being published. Time spent queued
// or waiting for resources before the build is not processing.
export function processingSpeed(row: StageTimes, meetingMs: number | null): Speed | null {
  return speed(spanMs(times(row, "build").started, times(row, "publish").finished), meetingMs);
}

// The run's figure: the newest attempt that processed the capture end to end.
export function runProcessing(detail: JobDetailResponse): Speed | null {
  const meetingMs = meetingLengthMs(detail.job);
  const attempts = [...detail.attempts].sort((a, b) => b.attempt_number - a.attempt_number);
  for (const attempt of attempts) {
    if (!CAPTURE_KINDS.has(attempt.trigger_kind)) continue;
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
