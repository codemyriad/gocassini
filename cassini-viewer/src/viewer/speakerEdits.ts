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

import type { TranscriptSpeaker } from "../core/types";
import { voiceParent } from "../core/speakers";

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
}

export type SpeakerEditsRunState = "idle" | "applying" | "failed" | "unavailable";

// Why separating voices cannot be offered on this meeting at all.
export type SpeakerEditsUnavailableReason =
  | "no-job"
  | "no-source-audio"
  | "no-transcript"
  | "diarization-unavailable";

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
}

// "unavailable" is the operator's answer to a save on a meeting whose
// participant audio or transcript has gone since it was split (409, with the
// `reason` GET reports as available=false).
export type SpeakerEditsErrorCode =
  | "revision-conflict"
  | "busy"
  | "invalid"
  | "diarization-unavailable"
  | "unavailable"
  | "not-found";

// A refusal from the speakers route. `revision` is the stored revision a
// revision-conflict reports; `detail` is the operator's own sentence for an
// invalid document; `reason` says why an unavailable meeting is.
export class SpeakerEditsError extends Error {
  status: number;
  code: SpeakerEditsErrorCode | "";
  revision?: number;
  detail?: string;
  reason?: string;

  constructor(
    status: number,
    code: SpeakerEditsErrorCode | "",
    options: { revision?: number; detail?: string; reason?: string } = {},
  ) {
    super(code || `HTTP ${status}`);
    this.name = "SpeakerEditsError";
    this.status = status;
    this.code = code;
    this.revision = options.revision;
    this.detail = options.detail;
    this.reason = options.reason;
  }
}

const ERROR_MESSAGES: Record<SpeakerEditsErrorCode, string> = {
  "revision-conflict": "Someone else changed the voices in this meeting. Their changes are shown now; try again.",
  busy: "This recording is being updated. Try again when it is done.",
  invalid: "Cassini could not accept these changes.",
  "diarization-unavailable": "Voice separation is not installed on this server.",
  unavailable: "The voices in this recording cannot be changed any more.",
  "not-found": "This meeting is not available to you any more.",
};

export function describeSpeakerEditsError(error: unknown): string {
  if (error instanceof SpeakerEditsError) {
    if (error.code === "invalid" && error.detail) {
      return `${ERROR_MESSAGES.invalid} ${error.detail}`;
    }
    if (error.code === "unavailable" && error.reason && error.reason in UNAVAILABLE_REASONS) {
      return UNAVAILABLE_REASONS[error.reason as SpeakerEditsUnavailableReason];
    }
    return (error.code && ERROR_MESSAGES[error.code]) || error.message;
  }
  return error instanceof Error ? error.message : String(error);
}

// The sentence for each reason the feature is absent on one meeting.
export const UNAVAILABLE_REASONS: Record<SpeakerEditsUnavailableReason, string> = {
  "no-job": "This recording was made before Cassini kept each participant's audio.",
  "no-source-audio": "Each participant's own audio was not kept for this recording.",
  "no-transcript": "This recording has no transcript to separate.",
  "diarization-unavailable": "Voice separation is not installed on this server.",
};

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
    return `Names can be at most ${MAX_SPEAKER_LABEL_LENGTH} characters.`;
  }
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f]/.test(trimmed)) {
    return "Names cannot contain control characters.";
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
  if (remainingMs <= 0) return "almost done";
  if (remainingMs <= 55_000) return `about ${Math.ceil(remainingMs / 5000) * 5} s left`;
  return `about ${Math.max(1, Math.round(remainingMs / 60_000))} min left`;
}

const PHASE_TEXT: Record<SpeakerEditsPhaseNow, string> = {
  starting: "Starting…",
  queued: "Waiting for other recordings…",
  separating: "Separating voices…",
  updating: "Updating the recording…",
};

export function phaseText(phase: SpeakerEditsPhaseNow): string {
  return PHASE_TEXT[phase] ?? PHASE_TEXT.updating;
}
