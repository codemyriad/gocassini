// Meeting import (D-828): the types the Import tab exchanges with a provider,
// and the browser-side preview parse of a dropped portable meeting file.
//
// The preview runs locally, before a byte is uploaded: a dropped file is read
// with the same parsers the player uses, so the list can show what the file is
// — title, date, duration, speakers, tags — and a non-portable file is refused
// without costing any bandwidth. Detection is by CONTENT, never by the name the
// file arrived under, mirroring how the published routes tolerate a recipient's
// rename.

import { get } from "svelte/store";

import { LL } from "../i18n";
import { dateFormat, durationFormat } from "../i18n/format";
import {
  PortableFormatError,
  extractPortableManifestFromArrayBuffer,
  extractTranscriptionDocument,
  readPortableAnnotations,
  readPortableSummaryMarkdown,
  type PortableMeetingManifest,
} from "./portable";

// One recipient of the shares an import creates. `label` is display-only: the
// operator keeps it on the job row so the list can say who "Shared with 3"
// means, and shares by type and id alone.
export interface ImportSharee {
  type: "user" | "group" | "circle";
  id: string;
  label: string;
}

// The sharing chosen for one file. It is sent when the upload is REGISTERED
// (before a byte moves) and is final from then on: submit and retry use what
// was stored. To import with different sharing, remove the attempt and drop
// the file again.
export interface ImportSharingRequest {
  share: boolean;
  audience: Array<{ type: string; id: string; label?: string }>;
}

// What POST imports sends per file once it is staged: the path, nothing else.
export interface ImportFileRequest {
  stagedPath: string;
}

// One import job as the operator serves it (GET imports).
export interface ImportPrincipal {
  type: string;
  id: string;
  label?: string;
}

export interface ImportJobRecord {
  origin?: "upload" | "cli";
  id: string;
  batchId: string;
  createdBy: string;
  sourceName: string;
  stagedPath: string;
  format?: string;
  state:
    | "uploading"
    | "upload_failed"
    | "staged"
    | "queued"
    | "fetching"
    | "validating"
    | "delivering"
    | "sharing"
    | "indexing"
    | "done"
    | "failed"
    | "canceled";
  stagingState?: string;
  phase?: string;
  error?: string;
  // What kind of failure error describes, when the server knows (#510).
  // One of disk_short, stalled, timed_out, invalid_file; others may come.
  errorCode?: string;
  sharing: {
    share: boolean;
    audience?: ImportPrincipal[];
    // Who Nextcloud's limit on new shares held back, the importer possibly
    // included; Cassini shares with them on its own once it can (#484).
    waiting?: ImportPrincipal[];
    // Who Nextcloud refused, with its own reason.
    notShared?: Array<{ type: string; id: string; reason?: string }>;
  };
  contentId?: string;
  deliveredName?: string;
  importedAt?: string;
  createdAt: string;
  updatedAt: string;
  // Set on a queued import: how many of the caller's own imports run before it.
  queuePosition?: number;
}

// Which step a running import is at, as its row says it.
export type ImportRunningStage = "uploading" | "waiting" | "queued" | "fetching" | "checking" | "adding";

export function importRunningStage(job: Pick<ImportJobRecord, "state" | "queuePosition">): ImportRunningStage {
  switch (job.state) {
    case "uploading":
      return "uploading";
    case "queued":
      return (job.queuePosition ?? 0) > 0 ? "queued" : "waiting";
    case "fetching":
      return "fetching";
    case "validating":
      return "checking";
    default:
      return "adding";
  }
}

export function importJobTerminal(state: ImportJobRecord["state"]): boolean {
  return state === "upload_failed" || state === "staged" || state === "done" || state === "failed" || state === "canceled";
}

// What the local parse learned about a dropped file.
export interface ImportPreview {
  format: "opus" | "json";
  title: string;
  dateLabel: string;
  durationMs: number;
  speakerCount: number;
  tagCount: number;
  markCount: number;
  hasSummary: boolean;
  hasAudio: boolean;
}

// Why a dropped file was refused: empty, not a Cassini meeting file, written
// by a newer Cassini, or a Cassini file that is damaged or incomplete.
export type ImportFileProblem = "empty" | "too_large" | "unreadable" | "not_cassini" | "newer" | "damaged";

// The largest file the server takes (maxImportUploadBytes in the operator).
export const maxImportFileBytes = 4 * 2 ** 30;

// ImportFileError's message is the sentence the review list shows, in the
// reader's language; detail is the parser's own, for whoever needs it.
export class ImportFileError extends Error {
  readonly problem: ImportFileProblem;
  readonly detail: string;
  constructor(problem: ImportFileProblem, detail = "") {
    super(importFileProblemText(problem));
    this.name = "ImportFileError";
    this.problem = problem;
    this.detail = detail;
  }
}

export function importFileProblemText(problem: ImportFileProblem): string {
  const text = get(LL).viewer.browse.import;
  switch (problem) {
    case "empty":
      return text.file_empty();
    case "too_large":
      return text.file_too_large();
    case "unreadable":
      return text.file_unreadable();
    case "not_cassini":
      return text.not_a_meeting();
    case "newer":
      return text.file_newer();
    default:
      return text.file_damaged();
  }
}

// parseImportPreview reads a dropped file far enough to describe it, or throws
// an ImportFileError saying why it cannot be imported. The parsers are
// fail-closed — the same contract the operator re-checks server-side — so a
// file that previews here can still be refused there, but not the other way
// around.
export async function parseImportPreview(file: File): Promise<ImportPreview> {
  // Checked before reading: the server would refuse it, and a browser may
  // not read a file this large into memory at all.
  if (file.size > maxImportFileBytes) {
    throw new ImportFileError("too_large");
  }
  // A file that cannot be read (moved since it was picked, or too large for
  // this browser to hold) says so, rather than being called damaged.
  let bytes: Uint8Array;
  try {
    bytes = new Uint8Array(await file.arrayBuffer());
  } catch (error) {
    throw new ImportFileError("unreadable", error instanceof Error ? error.message : String(error));
  }
  if (bytes.length === 0) {
    throw new ImportFileError("empty");
  }
  try {
    if (bytes[0] === 0x4f && bytes[1] === 0x67 && bytes[2] === 0x67 && bytes[3] === 0x53) {
      const { manifest } = await extractPortableManifestFromArrayBuffer(bytes);
      return describePortableManifest(manifest, "opus");
    }
    const text = stripUTF8BOM(new TextDecoder().decode(bytes));
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch (error) {
      // A Cassini transcription cut short (a half-finished download) names its
      // kind near the start: damaged, not some other file.
      if (/^\s*\{/.test(text) && text.slice(0, 4096).includes('"cassini-transcription"')) {
        throw new ImportFileError("damaged", error instanceof Error ? error.message : String(error));
      }
      throw new ImportFileError("not_cassini", "neither an Ogg/Opus recording nor a JSON transcription document");
    }
    const { manifest } = await extractTranscriptionDocument(parsed);
    return describePortableManifest(manifest, "json");
  } catch (error) {
    if (error instanceof ImportFileError) throw error;
    const detail = error instanceof Error ? error.message : String(error);
    throw new ImportFileError(error instanceof PortableFormatError ? error.problem : "damaged", detail);
  }
}

function describePortableManifest(
  manifest: PortableMeetingManifest,
  format: "opus" | "json",
): ImportPreview {
  const annotations = readPortableAnnotations(manifest);
  return {
    format,
    title: String(manifest.meeting?.title ?? "").trim() || get(LL).viewer.browse.import.untitled(),
    dateLabel: meetingDateLabel(manifest),
    durationMs: Number(manifest.meeting?.durationMs ?? manifest.audio?.durationMs ?? 0) || 0,
    speakerCount: Array.isArray(manifest.speakers) ? manifest.speakers.length : 0,
    tagCount: annotations?.tags.length ?? 0,
    markCount: annotations?.items.length ?? 0,
    hasSummary: readPortableSummaryMarkdown(manifest) !== null,
    hasAudio: format === "opus",
  };
}

// The date as the meeting states it: the local recording date when the file
// carries one, else the UTC creation date's day.
function meetingDateLabel(manifest: PortableMeetingManifest): string {
  const recorded = String(manifest.meeting?.recordedAtLocal ?? "").trim();
  if (recorded.length >= 10) {
    return recorded.slice(0, 10);
  }
  const created = String(manifest.meeting?.createdAtUtc ?? "").trim();
  return created.length >= 10 ? created.slice(0, 10) : "";
}

function stripUTF8BOM(text: string): string {
  return text.charCodeAt(0) === 0xfeff ? text.slice(1) : text;
}

// "2026-10-14" as "14 Oct 2026". Anything that is not a plain date passes
// through unchanged.
export function formatImportDate(dateLabel: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateLabel);
  if (!match) {
    return dateLabel;
  }
  const date = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])));
  return dateFormat({ day: "numeric", month: "short", year: "numeric", timeZone: "UTC" }).format(date);
}

export function formatImportDuration(durationMs: number): string {
  if (!Number.isFinite(durationMs) || durationMs <= 0) {
    return "";
  }
  const text = get(LL).viewer.browse.import;
  const minutes = Math.round(durationMs / 60000);
  if (minutes < 1) {
    return text.duration_under_minute();
  }
  return durationFormat({ hours: Math.floor(minutes / 60), minutes: minutes % 60 });
}

// The sharing line a settled row shows, and the names behind it on hover. The
// list is always the caller's own, so "you" is the importer.
export function describeImportSharing(job: Pick<ImportJobRecord, "sharing"> & { createdBy?: string }): { line: string; title: string } {
  const text = get(LL).viewer.browse.import;
  const audience = job.sharing.audience ?? [];
  const waiting = job.sharing.waiting ?? [];
  const notShared = job.sharing.notShared ?? [];
  const nameOf = (who: { type: string; id: string; label?: string }) => {
    if (who.type === "user" && who.id === job.createdBy) return text.sharing_you();
    const known = audience.find((sharee) => sharee.type === who.type && sharee.id === who.id);
    const name = (who.label ?? known?.label ?? "").trim() || who.id;
    return who.type === "group" ? text.sharee_group({ name }) : who.type === "circle" ? text.sharee_team({ name }) : name;
  };
  let line: string;
  let title: string;
  if (!job.sharing.share) {
    line = text.sharing_only_you();
    title = text.sharing_only_you_title();
  } else {
    title = [text.sharing_you(), ...audience.map(nameOf)].join(", ");
    const shared = audience.length + 1 - waiting.length - notShared.length;
    line = shared > 0 ? text.sharing_shared_with({ count: shared }) : text.sharing_not_yet();
  }
  // #484: who is still waiting for Nextcloud's limit, and whom it refused.
  const parts = [line];
  const titles = [title];
  if (waiting.length > 0) {
    if (!job.sharing.share) parts[0] = text.sharing_not_yet();
    parts.push(text.sharing_waiting({ count: waiting.length }));
    titles.push(text.sharing_waiting_title({ names: waiting.map(nameOf).join(", ") }));
  }
  if (notShared.length > 0) {
    parts.push(text.sharing_not_shared({ count: notShared.length }));
    titles.push(text.sharing_not_shared_title({
      names: notShared.map((who) => (who.reason ? `${nameOf(who)} (${who.reason})` : nameOf(who))).join(", "),
    }));
  }
  return { line: parts.join(" · "), title: titles.join("\n") };
}
