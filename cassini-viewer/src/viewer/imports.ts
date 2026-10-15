// Meeting import (D-828): the types the Import tab exchanges with a provider,
// and the browser-side preview parse of a dropped portable meeting file.
//
// The preview runs locally, before a byte is uploaded: a dropped file is read
// with the same parsers the player uses, so the list can show what the file is
// — title, date, duration, speakers, tags — and a non-portable file is refused
// without costing any bandwidth. Detection is by CONTENT, never by the name the
// file arrived under, mirroring how the published routes tolerate a recipient's
// rename.

import {
  extractPortableManifestFromArrayBuffer,
  extractTranscriptionDocument,
  readPortableAnnotations,
  readPortableSummaryMarkdown,
  type PortableMeetingManifest,
} from "./portable";

// One recipient of the shares an import creates. `label` is display-only; the
// operator receives only type and id.
export interface ImportSharee {
  type: "user" | "group" | "circle";
  id: string;
  label: string;
}

// What POST imports sends per file, once the file is staged.
export interface ImportFileRequest {
  stagedPath: string;
  sourceName: string;
  share: boolean;
  audience: Array<{ type: string; id: string }>;
}

// One import job as the operator serves it (GET imports).
export interface ImportJobRecord {
  id: string;
  batchId: string;
  createdBy: string;
  sourceName: string;
  stagedPath: string;
  format?: string;
  state:
    | "queued"
    | "fetching"
    | "validating"
    | "delivering"
    | "sharing"
    | "indexing"
    | "done"
    | "failed"
    | "canceled";
  phase?: string;
  error?: string;
  sharing: { share: boolean; audience?: Array<{ type: string; id: string }> };
  contentId?: string;
  deliveredName?: string;
  importedAt?: string;
  createdAt: string;
  updatedAt: string;
}

export function importJobTerminal(state: ImportJobRecord["state"]): boolean {
  return state === "done" || state === "failed" || state === "canceled";
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

// parseImportPreview reads a dropped file far enough to describe it, or throws
// with a sentence the review list can show. The parsers are fail-closed — the
// same contract the operator re-checks server-side — so a file that previews
// here can still be refused there, but not the other way around.
export async function parseImportPreview(file: File): Promise<ImportPreview> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  if (bytes.length === 0) {
    throw new Error("The file is empty.");
  }
  if (bytes[0] === 0x4f && bytes[1] === 0x67 && bytes[2] === 0x67 && bytes[3] === 0x53) {
    const { manifest } = await extractPortableManifestFromArrayBuffer(bytes);
    return describePortableManifest(manifest, "opus");
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(stripUTF8BOM(new TextDecoder().decode(bytes)));
  } catch {
    throw new Error("Not a portable meeting: neither an Ogg/Opus recording nor a transcription document.");
  }
  const { manifest } = await extractTranscriptionDocument(parsed);
  return describePortableManifest(manifest, "json");
}

function describePortableManifest(
  manifest: PortableMeetingManifest,
  format: "opus" | "json",
): ImportPreview {
  const annotations = readPortableAnnotations(manifest);
  return {
    format,
    title: String(manifest.meeting?.title ?? "").trim() || "Untitled meeting",
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

export function formatImportDuration(durationMs: number): string {
  if (!Number.isFinite(durationMs) || durationMs <= 0) {
    return "";
  }
  const minutes = Math.round(durationMs / 60000);
  if (minutes < 1) {
    return "under a minute";
  }
  if (minutes < 60) {
    return `${minutes} min`;
  }
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0 ? `${hours} h` : `${hours} h ${rest} min`;
}
