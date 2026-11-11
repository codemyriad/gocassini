// People's edits to a meeting's speakers (cassini.speaker-edits.v1).
//
// One participant's device can carry several people: a meeting-room laptop, a
// couple on one call. Someone who was there says so, the operator separates the
// voices in that participant's own audio and republishes the recording, and
// people name the voices or say two of them are the same person.
//
// The document is the DESIRED state, applied to the original transcript every
// time (cassini-go-recorder/internal/transcribe/speaker_edits.go): removing a
// split undoes it exactly, and a label for a voice that is not in the current
// result is kept and has no effect, so names come back when a split is redone.
// The viewer only ever sends whole documents; it never re-cuts a transcript.

import { get } from "svelte/store";

import type { TranscriptSpeaker } from "../core/types";
import { voiceParent } from "../core/speakers";
import { durationFormat, LL, numberFormat } from "../i18n";

export const SPEAKER_EDITS_FORMAT = "cassini.speaker-edits.v1";
// The annotation label rule, which the operator applies to these labels too.
export const MAX_SPEAKER_LABEL_LENGTH = 64;

export interface SpeakerSplit {
  speakerId: string;
}

// From is the same person as Into: two voices of one device.
export interface SpeakerMerge {
  from: string;
  into: string;
}

export interface SpeakerLabel {
  speakerId: string;
  label: string;
}

export interface SpeakerEditsDoc {
  format?: string;
  revision?: number;
  splits: SpeakerSplit[];
  merges: SpeakerMerge[];
  labels: SpeakerLabel[];
}

export interface SpeakerSplitReport {
  speakerId: string;
  voices: string[];
  inconclusive: boolean;
}

// What the last successful apply did. `inconclusive` lists the devices the
// diarizer heard only one voice on.
export interface SpeakerEditsReport {
  splits: SpeakerSplitReport[];
  inconclusive: string[];
  // What happened to the meeting summary. "stale" means it could not be
  // rewritten (no summary model, or it failed), so it still credits the
  // speakers it was written for.
  summary?: "none" | "unchanged" | "regenerated" | "restored" | "stale";
  // Which summary the published recording has: the SHA-256 of one rewritten
  // for speaker edits, "" for the build's own. Absent from older operators.
  summarySha256?: string;
}

export type SpeakerEditsRunState = "idle" | "applying" | "failed" | "unavailable";

// Why separating voices cannot be offered on this meeting at all.
export type SpeakerEditsUnavailableReason =
  | "no-job"
  | "no-source-audio"
  | "no-transcript"
  | "diarization-unavailable"
  | "unpublished-rebuild";

// How far the operator is with the revision it is applying, while `state` is
// "applying". `estimatedMs` is its guess, made once per attempt, of the time
// from the attempt's start to republished: the work, not the wait in the
// queue. `elapsedMs`, by the operator's clock at the moment it answered, is
// how long the attempt has waited while "queued", and how long it has run
// once it started.
//
// "queued": waiting behind another build or a recording; "separating": the
// diarizer is finding the voices on a device; "updating": rewriting the
// recording, its summary, and publishing it again.
export type SpeakerEditsPhase = "queued" | "separating" | "updating";

export interface SpeakerEditsProgress {
  phase: SpeakerEditsPhase;
  elapsedMs: number;
  estimatedMs: number;
}

// `GET annotations/meetings/<id>/speakers`. `participants` is the ORIGINAL
// roster — the devices that can be split — never voices.
export interface SpeakerEditsState {
  available: boolean;
  reason: SpeakerEditsUnavailableReason | "";
  revision: number;
  appliedRevision: number;
  state: SpeakerEditsRunState;
  lastError: string;
  doc: SpeakerEditsDoc;
  participants: TranscriptSpeaker[];
  report: SpeakerEditsReport | null;
  // null unless `state` is "applying"; absent from an operator older than it.
  progress?: SpeakerEditsProgress | null;
  // Whether this reader may ask a save to rewrite the meeting's summary for
  // the new speakers (D-814); absent from an older operator, which means no.
  canRewriteSummary?: boolean;
}

// "unavailable" is the operator's answer to a save on a meeting whose
// participant audio or transcript has gone since it was split (409, with the
// `reason` GET reports as available=false). "rate-limited" is its answer
// (429) to someone who saved more changes than it allows one person in a
// while, across all meetings; `retryAfterMs` says when the next one is
// taken.
export type SpeakerEditsErrorCode =
  | "revision-conflict"
  | "busy"
  | "invalid"
  | "diarization-unavailable"
  | "unavailable"
  | "rate-limited"
  | "not-found";

// A refusal from the speakers route. `revision` is the stored revision a
// revision-conflict reports; `detail` is the operator's own sentence for an
// invalid document; `reason` says why an unavailable meeting is;
// `retryAfterMs` how long a rate-limited caller waits.
export class SpeakerEditsError extends Error {
  status: number;
  code: SpeakerEditsErrorCode | "";
  revision?: number;
  detail?: string;
  reason?: string;
  retryAfterMs?: number;

  constructor(
    status: number,
    code: SpeakerEditsErrorCode | "",
    options: { revision?: number; detail?: string; reason?: string; retryAfterMs?: number } = {},
  ) {
    super(code || `HTTP ${status}`);
    this.name = "SpeakerEditsError";
    this.status = status;
    this.code = code;
    this.revision = options.revision;
    this.detail = options.detail;
    this.reason = options.reason;
    this.retryAfterMs = options.retryAfterMs;
  }
}

// Read when asked, so the message is in the reader's language.
function errorMessage(code: SpeakerEditsErrorCode): string {
  const t = get(LL).viewer.meeting.speaker_edits;
  switch (code) {
    case "revision-conflict": return t.error_revision_conflict();
    case "busy": return t.error_busy();
    case "invalid": return t.error_invalid();
    case "diarization-unavailable": return t.reason_diarization_unavailable();
    case "unavailable": return t.error_unavailable();
    case "rate-limited": return t.error_rate_limited();
    case "not-found": return t.error_not_found();
  }
}

// "Too many changes in a short time. Try again in N min.": whole minutes,
// rounded up, and never "0 min".
export function rateLimitedMessage(retryAfterMs: number | undefined): string {
  if (retryAfterMs === undefined || !Number.isFinite(retryAfterMs) || retryAfterMs < 0) {
    return errorMessage("rate-limited");
  }
  const minutes = numberFormat().format(Math.max(1, Math.ceil(retryAfterMs / 60_000)));
  return get(LL).viewer.meeting.speaker_edits.error_rate_limited_in({ minutes });
}

export function describeSpeakerEditsError(error: unknown): string {
  if (error instanceof SpeakerEditsError) {
    if (error.code === "invalid" && error.detail) {
      return get(LL).viewer.meeting.speaker_edits.error_invalid_detail({ detail: error.detail });
    }
    if (error.code === "unavailable" && error.reason && UNAVAILABLE_REASONS.includes(error.reason as SpeakerEditsUnavailableReason)) {
      return unavailableReasonText(error.reason as SpeakerEditsUnavailableReason);
    }
    if (error.code === "rate-limited") {
      return rateLimitedMessage(error.retryAfterMs);
    }
    return (error.code && errorMessage(error.code)) || error.message;
  }
  return error instanceof Error ? error.message : String(error);
}

// The reasons the feature can be absent on one meeting.
export const UNAVAILABLE_REASONS: readonly SpeakerEditsUnavailableReason[] = [
  "no-job",
  "no-source-audio",
  "no-transcript",
  "diarization-unavailable",
  "unpublished-rebuild",
];

// The sentence for each reason the feature is absent on one meeting.
export function unavailableReasonText(reason: SpeakerEditsUnavailableReason): string {
  const t = get(LL).viewer.meeting.speaker_edits;
  switch (reason) {
    // "no-job" covers both origins honestly: an imported meeting (D-828) and a
    // recording from before per-participant audio existed look identical here —
    // this installation has no recording job, so it holds nobody's own audio.
    case "no-job": return t.reason_no_job();
    case "no-source-audio": return t.reason_no_source_audio();
    case "no-transcript": return t.reason_no_transcript();
    case "diarization-unavailable": return t.reason_diarization_unavailable();
    case "unpublished-rebuild": return t.reason_unpublished_rebuild();
  }
}

// Whether nothing can be saved for this meeting at all. Without the diarizer
// only NEW splits are impossible: names, "same person" and undoing a split
// need no model. Every other reason is the operator refusing any save.
export function speakerEditsLocked(server: SpeakerEditsState): boolean {
  return !server.available && server.reason !== "" && server.reason !== "diarization-unavailable";
}

export function emptySpeakerEdits(): SpeakerEditsDoc {
  return { format: SPEAKER_EDITS_FORMAT, splits: [], merges: [], labels: [] };
}

// The body the operator takes: format and revision are its own to stamp.
export function speakerEditsBody(doc: SpeakerEditsDoc): SpeakerEditsDoc {
  return {
    splits: doc.splits.map(({ speakerId }) => ({ speakerId })),
    merges: doc.merges.map(({ from, into }) => ({ from, into })),
    labels: doc.labels.map(({ speakerId, label }) => ({ speakerId, label })),
  };
}

export function isSplit(doc: SpeakerEditsDoc, speakerId: string): boolean {
  return doc.splits.some((split) => split.speakerId === speakerId);
}

export function withSplit(doc: SpeakerEditsDoc, speakerId: string): SpeakerEditsDoc {
  if (isSplit(doc, speakerId)) return doc;
  return { ...doc, splits: [...doc.splits, { speakerId }] };
}

// Undoing a split leaves its voices' names and merges in place: they have no
// effect while the device is one speaker, and they come back if the device is
// split again, because its voices keep their ids (the diarizer's turns are
// frozen per device).
export function withoutSplit(doc: SpeakerEditsDoc, speakerId: string): SpeakerEditsDoc {
  return { ...doc, splits: doc.splits.filter((split) => split.speakerId !== speakerId) };
}

// The pending edits a reader has made in the People panel and not saved:
// names typed per voice, and "same person as" picks.
export interface PendingSpeakerEdits {
  // voice id → the name as typed. An empty name restores the default label.
  labels: Record<string, string>;
  // voice id → the voice it is the same person as, or null to separate a
  // voice that was saved as merged.
  merges: Record<string, string | null>;
}

export function emptyPending(): PendingSpeakerEdits {
  return { labels: {}, merges: {} };
}

export function labelProblem(label: string): string {
  const trimmed = label.trim();
  if ([...trimmed].length > MAX_SPEAKER_LABEL_LENGTH) {
    return get(LL).viewer.meeting.speaker_edits.label_too_long({ max: MAX_SPEAKER_LABEL_LENGTH });
  }
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f]/.test(trimmed)) {
    return get(LL).viewer.meeting.speaker_edits.label_control_characters();
  }
  return "";
}

// Folds pending edits into the stored document. `currentLabels` is what each
// speaker is called now, so typing a voice's current name back is no change.
// Merges are only ever between voices of one device, and never chained: a
// voice another voice was merged into cannot itself be merged away.
export function applyPending(
  doc: SpeakerEditsDoc,
  pending: PendingSpeakerEdits,
  currentLabels: ReadonlyMap<string, string>,
): SpeakerEditsDoc {
  const labels = new Map(doc.labels.map((entry) => [entry.speakerId, entry.label]));
  for (const [speakerId, typed] of Object.entries(pending.labels)) {
    const label = typed.trim();
    if (label === "") labels.delete(speakerId);
    else if (label !== currentLabels.get(speakerId) || labels.has(speakerId)) labels.set(speakerId, label);
  }
  const merges = new Map(doc.merges.map((entry) => [entry.from, entry.into]));
  const picks = Object.entries(pending.merges);
  for (const [from, into] of picks) {
    if (into === null) merges.delete(from);
  }
  for (const [from, into] of picks) {
    if (into === null || into === from || voiceParent(from) === "" || voiceParent(from) !== voiceParent(into)) continue;
    // A pick that would chain is refused, not the merge already there.
    const chains = merges.has(into) || [...merges].some(([other, target]) => other !== from && target === from);
    if (!chains) merges.set(from, into);
  }
  return {
    ...doc,
    merges: [...merges].map(([from, into]) => ({ from, into })),
    labels: [...labels].map(([speakerId, label]) => ({ speakerId, label })),
  };
}

// Whether two documents ask for the same thing, ignoring order and stamps.
export function sameEdits(a: SpeakerEditsDoc, b: SpeakerEditsDoc): boolean {
  const key = (doc: SpeakerEditsDoc) =>
    JSON.stringify([
      doc.splits.map((split) => split.speakerId).sort(),
      doc.merges.map((merge) => `${merge.from}>${merge.into}`).sort(),
      doc.labels.map((label) => `${label.speakerId}=${label.label}`).sort(),
    ]);
  return key(a) === key(b);
}


// Where an apply is now, `sinceMs` after the operator said `progress`: the
// time left on its estimate (below zero once the estimate is overrun) and how
// much of the estimate has gone, as a whole percentage. The bar never fills
// before the recording is actually republished, so it stops at 95.
export const PROGRESS_CAP_PERCENT = 95;

// An attempt is queued the moment it is saved and normally starts a moment
// later, so the save's own answer, and often the next poll's, say "queued" on
// an operator with nothing else to do. Only an attempt still queued after this
// long is waiting behind other work; until then it is "starting". While
// queued none of the estimate is used up: the time waited is not work, and
// the operator counts from the start once the attempt runs, so counting the
// wait down here would make the time left jump back up at the start.
export const QUEUED_GRACE_MS = 5000;

export type SpeakerEditsPhaseNow = SpeakerEditsPhase | "starting";

export interface SpeakerEditsProgressNow {
  phase: SpeakerEditsPhaseNow;
  remainingMs: number;
  percent: number;
}

export function progressNow(progress: SpeakerEditsProgress, sinceMs: number): SpeakerEditsProgressNow {
  const elapsed = Math.max(0, progress.elapsedMs) + Math.max(0, sinceMs);
  const worked = progress.phase === "queued" ? 0 : elapsed;
  const estimated = Math.max(0, progress.estimatedMs);
  const share = estimated > 0 ? (worked / estimated) * 100 : PROGRESS_CAP_PERCENT;
  return {
    phase: progress.phase === "queued" && elapsed < QUEUED_GRACE_MS ? "starting" : progress.phase,
    remainingMs: estimated - worked,
    percent: Math.round(Math.min(PROGRESS_CAP_PERCENT, Math.max(0, share))),
  };
}

// "about 50 s left", "about 2 min left", "almost done". Seconds go in steps of
// five, rounded up: an estimate said to the second would promise too much.
export function remainingText(remainingMs: number): string {
  const t = get(LL).viewer.meeting.speaker_edits;
  if (remainingMs <= 0) return t.almost_done();
  if (remainingMs <= 55_000) return t.time_left({ duration: durationFormat({ seconds: Math.ceil(remainingMs / 5000) * 5 }) });
  return t.time_left({ duration: durationFormat({ minutes: Math.max(1, Math.round(remainingMs / 60_000)) }) });
}

export function phaseText(phase: SpeakerEditsPhaseNow): string {
  const t = get(LL).viewer.meeting.speaker_edits;
  switch (phase) {
    case "starting": return t.phase_starting();
    case "queued": return t.phase_queued();
    case "separating": return t.phase_separating();
    default: return t.phase_updating();
  }
}

// How many people a meeting's speakers add up to (countPeople), as the People
// panel heads its list and the details chip names it: "3 participants", or,
// once a shared device is separated, "4 voices on 2 devices". Every device
// listed has at least one voice, so there are never fewer voices than devices.
export function peopleCountText(people: { voices: number; devices: number; split: boolean }): string {
  const p = get(LL).viewer.meeting.people;
  if (!people.split) return p.count({ count: people.voices });
  return p.split({ voices: people.voices, devices: people.devices });
}
