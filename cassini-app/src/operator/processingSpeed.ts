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
  /** Time spent waiting on the summary model, left out of ms. */
  summaryMs?: number;
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
  // Far behind real time, one significant digit, so it never reads "0.0×".
  if (tenths === 0) return `${Number(factor.toPrecision(1))}×`;
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

// How long one step took, once it has finished. A failed step is stamped
// finished too, so its time is how long it ran before failing. Only the run
// gets a multiplier: a step's own is nothing an administrator acts on.
export function stepTime(row: StageTimes, key: string): number | null {
  const t = stageTimes(row, key);
  return spanMs(t.started, t.finished);
}

// How long the attempt spent waiting on the summary model inside its build
// (D-899), whether or not the summary came back.
export function summaryTime(row: StageTimes): number | null {
  if (!("summary_started_at" in row)) return null;
  return spanMs(row.summary_started_at, row.summary_finished_at);
}

// A span of an attempt that processed the capture, without the summary wait
// inside it: that waits on the summary model, not on this server. Attempts
// that only write a summary or replay speaker edits keep it, as it is all
// they do.
function withoutSummary(row: StageTimes, ms: number | null): { ms: number | null; summaryMs?: number } {
  if (ms === null || !("trigger_kind" in row) || !processesCapture(row)) return { ms };
  const summaryMs = summaryTime(row);
  return summaryMs !== null && summaryMs < ms ? { ms: ms - summaryMs, summaryMs } : { ms };
}

// The build's time, without the summary for an attempt that processed the capture.
export function buildTimeWithoutSummary(row: StageTimes): number | null {
  return withoutSummary(row, stepTime(row, "build")).ms;
}

// From the build starting to the meeting being published. Time spent queued
// or waiting for resources before the build is not processing, and neither is
// the summary wait: a slow summary model would read as hardware that cannot
// keep up.
export function processingSpeed(row: StageTimes, meetingMs: number | null): Speed | null {
  const { ms, summaryMs } = withoutSummary(row, spanMs(stageTimes(row, "build").started, stageTimes(row, "publish").finished));
  const result = speed(ms, meetingMs);
  return result && summaryMs !== undefined ? { ...result, summaryMs } : result;
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

// Said on hover when the summary was left out of the figure.
export function processingHint(result: Speed): string | undefined {
  if (result.summaryMs === undefined) return undefined;
  return `Not counting ${formatSpan(result.summaryMs)} waiting on the summary model`;
}
