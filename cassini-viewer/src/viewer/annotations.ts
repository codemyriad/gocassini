import type { TagColorId, TagIconId } from "./tagPalette";

// The wire of the annotation routes (D-737, D-746). Time ranges are half-open
// [startMs, endMs) integers.

export type TimeRangeTarget = { kind: "time-range"; startMs: number; endMs: number };
export type AnnotationTarget = { kind: "meeting" } | TimeRangeTarget;

export interface AnnotationTag {
  id: string;
  label: string;
}

export interface AnnotationItem {
  id: string;
  tagId: string;
  target: AnnotationTarget;
  createdAtUtc: string;
  actor: { kind: string; id: string };
  operationId: string;
}

export interface AnnotationsDocument {
  format: string;
  revision: number;
  audioOpusSha256: string;
  tagNamespace: string;
  tags: AnnotationTag[];
  items: AnnotationItem[];
}

// `resolved: false` means the marks were made against other audio, and their
// time ranges must not be drawn against this recording.
export interface MeetingAnnotations {
  meetingId: string;
  revision: number;
  annotations: AnnotationsDocument | null;
  resolved: boolean | null;
}

export interface AnnotationResult extends MeetingAnnotations {
  operationId: string;
  added: string[];
  removed: string[];
  notFound: string[];
}

export interface VocabularyTag {
  tagId: string;
  namespace: string;
  label: string;
  meetings: number;
  marks: number;
  color: TagColorId | "";
  icon: TagIconId | "";
  // Both empty until someone renames, recolours or merges the tag.
  changedBy: string;
  changedAtUtc: string;
}

export interface TagUpdate {
  label?: string;
  color?: TagColorId | "";
  icon?: TagIconId | "";
}

// A rename, merge or delete rewrites every recording that carries the tag, so
// it runs as a job the manager polls.
export interface TagJob {
  id: string;
  kind: "rename" | "merge" | "delete";
  tagId: string;
  into?: string;
  state: "running" | "finished" | "interrupted";
  total: number;
  done: number;
  failed: { meeting: string; error: string }[];
  actor: string;
  startedAtUtc: string;
  finishedAtUtc?: string;
}

export interface MeetingTagRef {
  tagId: string;
  whole: boolean;
  stretches: number;
}

export interface TagVocabulary {
  tags: VocabularyTag[];
  meetings: { meetingId: string; tags: MeetingTagRef[] }[];
  coverage: { visible: number; indexed: number };
}

export type AnnotationOp =
  | { op: "mark"; tag: { id?: string; label: string }; target: AnnotationTarget }
  | { op: "unmark"; itemId: string }
  | { op: "unmark-tag"; tagId: string; target?: AnnotationTarget }
  | { op: "relabel"; tagId: string; label: string }
  | { op: "merge-tag"; tagId: string; into: AnnotationTag }
  | { op: "undo-operation"; operationId: string };

export interface TagStyle {
  label: string;
  color: TagColorId;
  icon?: TagIconId | "";
}

export interface AnnotationRequest {
  ops: AnnotationOp[];
  expectRevision?: number;
  tagStyles?: TagStyle[];
}

// `code` is the operator's `error` value verbatim. A 409 carries exactly
// "revision-conflict", "unresolved" or "conflict" from a meeting, and "busy" or
// "label-exists" from the tag routes; with "label-exists", `tagId` is the tag
// that already has the label.
export class AnnotationError extends Error {
  status: number;
  code: string;
  tagId?: string;

  constructor(status: number, code: string, tagId?: string) {
    super(code || `HTTP ${status}`);
    this.name = "AnnotationError";
    this.status = status;
    this.code = code;
    this.tagId = tagId;
  }
}

export interface TagMarks {
  tag: AnnotationTag;
  whole: AnnotationItem | null;
  stretches: AnnotationItem[];
}

// One entry per tag that has at least one mark, in the document's tag order;
// stretches by time.
export function groupByTag(doc: AnnotationsDocument | null): TagMarks[] {
  if (!doc) {
    return [];
  }
  const groups = new Map<string, TagMarks>(
    doc.tags.map((tag) => [tag.id, { tag, whole: null, stretches: [] }]),
  );
  for (const item of doc.items) {
    const group = groups.get(item.tagId);
    if (!group) {
      continue;
    }
    if (item.target.kind === "meeting") {
      group.whole = item;
    } else if (item.target.kind === "time-range") {
      group.stretches.push(item);
    }
  }
  const result = [...groups.values()].filter((group) => group.whole || group.stretches.length > 0);
  for (const group of result) {
    group.stretches.sort((a, b) => rangeOf(a)[0] - rangeOf(b)[0] || rangeOf(a)[1] - rangeOf(b)[1]);
  }
  return result;
}

// A tag can be in both lists: on the whole meeting and on stretches of it.
export function splitByTarget(doc: AnnotationsDocument | null): {
  whole: TagMarks[];
  stretches: TagMarks[];
} {
  const groups = groupByTag(doc);
  return {
    whole: groups.filter((group) => group.whole),
    stretches: groups.filter((group) => group.stretches.length > 0),
  };
}

function rangeOf(item: AnnotationItem): [number, number] {
  return item.target.kind === "time-range" ? [item.target.startMs, item.target.endMs] : [0, 0];
}

export interface MeetingTag {
  tag: VocabularyTag;
  whole: boolean;
  stretches: number;
}

// Look a catalog entry up by its id. Whole-meeting tags come first, then by label.
export function tagsByMeeting(vocabulary: TagVocabulary): Map<string, MeetingTag[]> {
  const byId = new Map(vocabulary.tags.map((tag) => [tag.tagId, tag]));
  const result = new Map<string, MeetingTag[]>();
  for (const meeting of vocabulary.meetings) {
    const tags = meeting.tags.flatMap(({ tagId, whole, stretches }) => {
      const tag = byId.get(tagId);
      return tag ? [{ tag, whole, stretches }] : [];
    });
    tags.sort((a, b) => Number(b.whole) - Number(a.whole) || a.tag.label.localeCompare(b.tag.label));
    result.set(meeting.meetingId, tags);
  }
  return result;
}

// The operator matches labels trimmed and case-insensitively, so the picker does too.
function labelKey(label: string): string {
  return label.trim().toLowerCase();
}

export function findByLabel(
  tags: readonly VocabularyTag[],
  label: string,
): VocabularyTag | undefined {
  const key = labelKey(label);
  return tags.find((tag) => labelKey(tag.label) === key);
}

// Prefix matches first, then the most used, then by label.
export function matchTags(tags: readonly VocabularyTag[], query: string): VocabularyTag[] {
  const key = labelKey(query);
  const rank = (tag: VocabularyTag) => (labelKey(tag.label).startsWith(key) ? 0 : 1);
  return tags
    .filter((tag) => labelKey(tag.label).includes(key))
    .sort((a, b) => rank(a) - rank(b) || b.meetings - a.meetings || a.label.localeCompare(b.label));
}

export function timeRange(startMs: number, endMs: number): TimeRangeTarget {
  const start = Math.max(0, Math.round(startMs));
  const end = Math.round(endMs);
  if (!(end > start)) {
    throw new RangeError(`Not a time range: ${startMs}–${endMs}`);
  }
  return { kind: "time-range", startMs: start, endMs: end };
}

// One batch, so the stretch is never missing between two commits. Passing a
// different tag moves it to that tag.
export function moveStretchOps(
  itemId: string,
  tag: { id?: string; label: string },
  startMs: number,
  endMs: number,
): AnnotationOp[] {
  return [
    { op: "unmark", itemId },
    { op: "mark", tag: { id: tag.id, label: tag.label }, target: timeRange(startMs, endMs) },
  ];
}
