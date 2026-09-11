import type { MeetingCatalogEntry } from "./catalog";
import type { AnnotationRequest, MeetingTag } from "./annotations";
import type { TagColorId } from "./tagPalette";

export type TagMatch = "any" | "all";
export type TagPick = { tagId: string; label: string } | { label: string; color: TagColorId; icon: "" };
export type MeetingTags = ReadonlyMap<string, readonly MeetingTag[]>;

const WHOLE_MEETING = { kind: "meeting" } as const;

export function filterByTags<T extends { id: string }>(
  meetings: T[],
  byMeeting: MeetingTags,
  tagIds: readonly string[],
  match: TagMatch,
): T[] {
  if (tagIds.length === 0) {
    return meetings;
  }
  return meetings.filter((meeting) => {
    const on = new Set(byMeeting.get(meeting.id)?.map(({ tag }) => tag.tagId));
    return match === "all" ? tagIds.every((id) => on.has(id)) : tagIds.some((id) => on.has(id));
  });
}

export function rowChips(tags: readonly MeetingTag[] = [], max = 3) {
  return { shown: tags.slice(0, max), rest: tags.slice(max) };
}

export function hasWholeTag(byMeeting: MeetingTags, meetingId: string, tagId: string): boolean {
  return byMeeting.get(meetingId)?.some(({ tag, whole }) => whole && tag.tagId === tagId) ?? false;
}

// For a picker over several meetings: whole-meeting tags on every one of them, and on only some.
export function wholeTagState(byMeeting: MeetingTags, meetingIds: readonly string[]) {
  const counts = new Map<string, number>();
  for (const id of meetingIds) {
    for (const { tag, whole } of byMeeting.get(id) ?? []) {
      if (whole) {
        counts.set(tag.tagId, (counts.get(tag.tagId) ?? 0) + 1);
      }
    }
  }
  const all = meetingIds.length;
  const ids = [...counts.keys()];
  return {
    selected: ids.filter((id) => counts.get(id) === all),
    mixed: ids.filter((id) => counts.get(id)! < all),
  };
}

export function wholeTagRequest(pick: TagPick, remove: boolean): AnnotationRequest {
  if (!("tagId" in pick)) {
    return {
      ops: [{ op: "mark", tag: { label: pick.label }, target: WHOLE_MEETING }],
      tagStyles: [{ label: pick.label, color: pick.color, icon: "" }],
    };
  }
  return remove
    ? { ops: [{ op: "unmark-tag", tagId: pick.tagId, target: WHOLE_MEETING }] }
    : { ops: [{ op: "mark", tag: { id: pick.tagId, label: pick.label }, target: WHOLE_MEETING }] };
}

// A tag on every meeting comes off them all; otherwise it goes on the ones without it.
export function planBulkTag(entries: readonly MeetingCatalogEntry[], byMeeting: MeetingTags, pick: TagPick) {
  const remove =
    "tagId" in pick && entries.length > 0 && entries.every((entry) => hasWholeTag(byMeeting, entry.id, pick.tagId));
  const targets =
    "tagId" in pick ? entries.filter((entry) => hasWholeTag(byMeeting, entry.id, pick.tagId) === remove) : [...entries];
  return { remove, targets, request: wholeTagRequest(pick, remove) };
}

// One at a time: each request rewrites a recording, and the operator serialises them anyway.
export async function applyEach<T>(targets: readonly T[], apply: (target: T) => Promise<unknown>) {
  let done = 0;
  for (const target of targets) {
    try {
      await apply(target);
      done += 1;
    } catch {
      // Counted, not thrown: the rest of the selection still gets its request.
    }
  }
  return { done, failed: targets.length - done };
}

export function bulkReport(remove: boolean, done: number, total: number): string {
  const verb = remove ? "Untagged" : "Tagged";
  if (done === total) {
    return `${verb} ${total} ${total === 1 ? "meeting" : "meetings"}`;
  }
  return `${verb} ${done} of ${total} — ${total - done} failed`;
}
