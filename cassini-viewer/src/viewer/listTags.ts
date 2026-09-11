import type { MeetingCatalogEntry } from "./catalog";
import { AnnotationError, type AnnotationRequest, type MeetingTag, type TagVocabulary } from "./annotations";
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

function hasWholeTag(byMeeting: MeetingTags, meetingId: string, tagId: string): boolean {
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

// Writes run in the order they were picked, never two at once and never dropped;
// `drained` runs once the last of a burst settles.
export function createWriteQueue(drained: () => void) {
  let tail: Promise<void> = Promise.resolve();
  let pending = 0;
  return (write: () => Promise<void>): Promise<void> => {
    pending += 1;
    tail = tail
      .then(write)
      .catch(() => undefined)
      .finally(() => {
        pending -= 1;
        if (pending === 0) {
          drained();
        }
      });
    return tail;
  };
}

// 503 while the operator first indexes the archive, 429 once the caller's search budget is spent.
export function tagRetryDelay(error: unknown, attempt: number): number | null {
  if (!(error instanceof AnnotationError) || (error.status !== 503 && error.status !== 429)) {
    return null;
  }
  return Math.min(2_000 * 2 ** attempt, 60_000);
}

// Each load spends one of the caller's searches, so only one is in flight. A
// plain reload is dropped while one runs; reload(true), for after a write,
// queues one more so the answer cannot predate the write. `loaded` gets null
// when a load fails.
export function createTagLoader(
  load: () => Promise<TagVocabulary>,
  loaded: (vocabulary: TagVocabulary | null) => void,
) {
  let running = false;
  let again = false;
  let stopped = false;
  let attempt = 0;
  let retry: ReturnType<typeof setTimeout> | undefined;

  async function reload(fresh = false): Promise<void> {
    if (running) {
      again ||= fresh;
      return;
    }
    if (stopped) {
      return;
    }
    clearTimeout(retry);
    running = true;
    try {
      const vocabulary = await load();
      attempt = 0;
      if (!stopped) {
        loaded(vocabulary);
      }
    } catch (error) {
      const delay = tagRetryDelay(error, attempt);
      attempt += 1;
      if (!stopped) {
        loaded(null);
        if (delay !== null) {
          retry = setTimeout(() => void reload(), delay);
        }
      }
    } finally {
      running = false;
      if (again) {
        again = false;
        void reload();
      }
    }
  }

  return {
    reload,
    stop() {
      stopped = true;
      clearTimeout(retry);
    },
  };
}
