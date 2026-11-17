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
  extractPortableManifestFromArrayBuffer,
  extractTranscriptionDocument,
  opusCommentKeys,
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
export interface ImportJobRecord {
  origin?: "upload" | "cli";
  /** "audio" rows are transcribed and summarised by Cassini before delivery. */
  kind?: "meeting" | "audio";
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
  sharing: { share: boolean; audience?: Array<{ type: string; id: string; label?: string }> };
  contentId?: string;
  deliveredName?: string;
  importedAt?: string;
  createdAt: string;
  updatedAt: string;
}

export function importJobTerminal(state: ImportJobRecord["state"]): boolean {
  return state === "upload_failed" || state === "staged" || state === "done" || state === "failed" || state === "canceled";
}

// What the local parse learned about a dropped file.
export interface ImportPreview {
  format: "opus" | "json" | "audio";
  title: string;
  dateLabel: string;
  durationMs: number;
  speakerCount: number;
  tagCount: number;
  markCount: number;
  hasSummary: boolean;
  hasAudio: boolean;
}

// parseImportPreview reads a dropped file far enough to describe it, or throws
// with a sentence the review list can show. The parsers are fail-closed — the
// same contract the operator re-checks server-side — so a file that previews
// here can still be refused there, but not the other way around.
export async function parseImportPreview(file: File): Promise<ImportPreview> {
  // Classify from the head of the file first. A raw recording can be hundreds
  // of megabytes, and nothing about it is read in the browser, so it is never
  // loaded whole just to be recognised.
  const head = new Uint8Array(await file.slice(0, SNIFF_BYTES).arrayBuffer());
  if (head.length === 0) {
    throw new Error(get(LL).viewer.browse.import.file_empty());
  }
  if (head[0] === 0x4f && head[1] === 0x67 && head[2] === 0x67 && head[3] === 0x53) {
    // An Ogg file with no Cassini tag at all is a plain recording to build. One
    // that carries Cassini tags is a portable meeting, and if it does not parse
    // the refusal stands: building it from its audio would silently drop its
    // transcript, speakers, tags, marks and summary.
    if (looksLikeAudio(file) && !(await oggCarriesCassiniTags(file, head))) return describeAudioUpload(file);
    const bytes = head.length < SNIFF_BYTES ? head : new Uint8Array(await file.arrayBuffer());
    const { manifest } = await extractPortableManifestFromArrayBuffer(bytes);
    return describePortableManifest(manifest, "opus");
  }
  if (!startsLikeJSON(head) && looksLikeAudio(file)) return describeAudioUpload(file);
  const bytes = head.length < SNIFF_BYTES ? head : new Uint8Array(await file.arrayBuffer());
  let parsed: unknown;
  try {
    parsed = JSON.parse(stripUTF8BOM(new TextDecoder().decode(bytes)));
  } catch {
    throw new Error(get(LL).viewer.browse.import.not_portable());
  }
  const { manifest } = await extractTranscriptionDocument(parsed);
  return describePortableManifest(manifest, "json");
}

// SNIFF_BYTES is how much of a file classification reads. The Opus comment
// header, where a portable meeting's Cassini tags start, sits in the first
// pages of the stream.
const SNIFF_BYTES = 1 << 20;

// oggCarriesCassiniTags says whether an Ogg stream's comment header has any
// Cassini key. It reads only as far as that header ends, which can be past the
// first MiB when cover art comes first. A stream whose header cannot be read
// counts as Cassini when its head mentions a Cassini key anywhere, so a
// damaged portable file is refused rather than re-transcribed.
async function oggCarriesCassiniTags(file: File, head: Uint8Array): Promise<boolean> {
  let bytes = head;
  for (;;) {
    const complete = bytes.length < SNIFF_BYTES || bytes.length >= file.size;
    const keys = opusCommentKeys(bytes, complete);
    if (keys === "more") {
      bytes = new Uint8Array(await file.slice(0, bytes.length * 4).arrayBuffer());
      continue;
    }
    if (keys === null) return mentionsCassini(head);
    return keys.some((key) => key.startsWith("CASSINI_"));
  }
}

function mentionsCassini(head: Uint8Array): boolean {
  const needle = [0x43, 0x41, 0x53, 0x53, 0x49, 0x4e, 0x49, 0x5f]; // "CASSINI_"
  outer: for (let i = 0; i + needle.length <= head.length; i += 1) {
    for (let j = 0; j < needle.length; j += 1) {
      if ((head[i + j] & 0xdf) !== needle[j]) continue outer; // & 0xdf folds a-z to A-Z
    }
    return true;
  }
  return false;
}

// startsLikeJSON says whether the first non-blank character, after an optional
// byte order mark, opens a JSON object or array.
function startsLikeJSON(head: Uint8Array): boolean {
  let i = head[0] === 0xef && head[1] === 0xbb && head[2] === 0xbf ? 3 : 0;
  while (i < head.length && (head[i] === 0x20 || head[i] === 0x09 || head[i] === 0x0a || head[i] === 0x0d)) i += 1;
  return head[i] === 0x7b || head[i] === 0x5b;
}

// looksLikeAudio is a filename/MIME hint only. It exists to keep a clearly
// non-meeting file from reaching the operator; the operator's ffprobe is the
// authority on whether the bytes are actually readable audio.
const AUDIO_EXTENSIONS = /\.(mp3|m4a|m4b|mp4a|wav|aac|flac|ogg|oga|opus|webm|weba|aif|aiff|wma|amr|3gp|3gpp)$/i;

function looksLikeAudio(file: File): boolean {
  return file.type.startsWith("audio/") || AUDIO_EXTENSIONS.test(file.name);
}

function describeAudioUpload(file: File): ImportPreview {
  const title = file.name.replace(/\.[^.]+$/, "").trim() || get(LL).viewer.browse.import.untitled();
  return {
    format: "audio",
    title,
    dateLabel: "",
    durationMs: 0,
    speakerCount: 0,
    tagCount: 0,
    markCount: 0,
    hasSummary: false,
    hasAudio: true,
  };
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
export function describeImportSharing(job: Pick<ImportJobRecord, "sharing">): { line: string; title: string } {
  const text = get(LL).viewer.browse.import;
  if (!job.sharing.share) {
    return { line: text.sharing_only_you(), title: text.sharing_only_you_title() };
  }
  const audience = job.sharing.audience ?? [];
  const names = audience.map((sharee) => {
    const name = (sharee.label ?? "").trim() || sharee.id;
    return sharee.type === "group" ? text.sharee_group({ name }) : sharee.type === "circle" ? text.sharee_team({ name }) : name;
  });
  return {
    line: text.sharing_shared_with({ count: audience.length + 1 }),
    title: [text.sharing_you(), ...names].join(", "),
  };
}
