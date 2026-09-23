import type { MeetingCatalogEntry } from "./catalog";
import { writable } from "svelte/store";
import {
  WHOLE_MEETING,
  describeAnnotationError,
  findByLabel,
  groupByTag,
  markRequest,
  plural,
  retryDelay,
  untagMeetingRequest,
  type AnnotationRequest,
  type AnnotationResult,
  type MeetingAnnotations,
  type MeetingTag,
  type TagPick,
  type TagVocabulary,
} from "./annotations";

export type TagMatch = "any" | "all";
export type MeetingTags = ReadonlyMap<string, readonly MeetingTag[]>;

// filterByTagLabel narrows to the meetings carrying a tag whose NAME matches
// what was typed (D-772).
//
// Distinct from filterByTags, which narrows by tag ids the reader picked from
// the vocabulary. This is the search box: you type "dail" and expect the Daily
// meetings, without having gone looking for the chip first.
//
// Partial and case-insensitive, matching how the box already treats titles and
// dates — an exact match would be right for a filter and wrong for a search,
// where the whole point is finding the tag before you can name it exactly. That
// is also why this cannot reuse the server's taggedMeetings, which resolves
// `tag_id = ? OR label_folded = folded(?)` exactly.
//
// A blank query matches NOTHING here rather than everything: this is one half
// of a union with the title/date filter, and that filter already owns the rule
// that an empty box means "no filter". Returning everything from both halves
// would work by accident and break the moment the union changed.
export function filterByTagLabel<T extends { id: string }>(
  meetings: readonly T[],
  byMeeting: MeetingTags,
  query: string,
): T[] {
  const needle = query.trim().toLowerCase();
  if (needle === "") {
    return [];
  }
  return meetings.filter((meeting) =>
    (byMeeting.get(meeting.id) ?? []).some((held) =>
      held.tag.label.toLowerCase().includes(needle),
    ),
  );
}

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

// A tag on every meeting comes off them all; otherwise it goes on the ones without it.
export function planBulkTag(entries: readonly MeetingCatalogEntry[], byMeeting: MeetingTags, pick: TagPick) {
  const remove =
    "tagId" in pick && entries.length > 0 && entries.every((entry) => hasWholeTag(byMeeting, entry.id, pick.tagId));
  const targets =
    "tagId" in pick ? entries.filter((entry) => hasWholeTag(byMeeting, entry.id, pick.tagId) === remove) : [...entries];
  const request = "tagId" in pick && remove ? untagMeetingRequest(pick.tagId) : markRequest(pick, WHOLE_MEETING);
  return { remove, targets, request };
}

// A write answers with the meeting's document, so its row and the picker's
// ticks need not wait for the vocabulary to reload. A newly created tag is
// admitted from the returned document; aggregate counts still come from reload.
export function withMeetingResult(vocabulary: TagVocabulary, result: MeetingAnnotations): TagVocabulary {
  const meetings = new Map(vocabulary.meetings.map((meeting) => [meeting.meetingId, meeting]));
  const vocabularyTags = new Map(vocabulary.tags.map((tag) => [tag.tagId, { ...tag }]));
  const tags = groupByTag(result.annotations).map(({ tag, whole, stretches }) => {
    if (!vocabularyTags.has(tag.id)) {
      vocabularyTags.set(tag.id, {
        tagId: tag.id,
        namespace: result.annotations?.tagNamespace ?? "",
        label: tag.label,
        meetings: 1,
        marks: Number(whole !== null) + stretches.length,
        color: "",
        icon: "",
      });
    }
    return {
      tagId: tag.id,
      whole: whole !== null,
      stretches: stretches.length,
      color: tag.color,
      icon: tag.icon,
    };
  });
  meetings.set(result.meetingId, { meetingId: result.meetingId, tags });
  return {
    ...vocabulary,
    // Existing aggregate counts remain untouched until the authoritative
    // reload; only the changed meeting document is exact in this response.
    tags: [...vocabularyTags.values()],
    meetings: [...meetings.values()],
  };
}

function tagForPick(vocabulary: TagVocabulary, pick: TagPick) {
  return ("tagId" in pick
    ? vocabulary.tags.find(({ tagId }) => tagId === pick.tagId)
    : undefined) ?? findByLabel(vocabulary.tags, pick.label);
}

function wholeTagOn(vocabulary: TagVocabulary, meetingId: string, pick: TagPick): boolean {
  const tag = tagForPick(vocabulary, pick);
  if (!tag) return false;
  return vocabulary.meetings
    .find((meeting) => meeting.meetingId === meetingId)
    ?.tags.some((held) => held.tagId === tag.tagId && held.whole) ?? false;
}

// Apply one desired whole-meeting state without mutating the confirmed server
// vocabulary. Pending actions are replayed in click order after every answer,
// so an older answer can never erase a newer click from the picker.
function withOptimisticWholeTag(
  vocabulary: TagVocabulary,
  meetingId: string,
  pick: TagPick,
  desired: boolean,
  optimisticTagId: string,
): TagVocabulary {
  const tags = vocabulary.tags.map((tag) => ({ ...tag }));
  let tag = ("tagId" in pick
    ? tags.find(({ tagId }) => tagId === pick.tagId)
    : undefined) ?? findByLabel(tags, pick.label);
  if (!tag && desired) {
    tag = {
      tagId: optimisticTagId,
      namespace: "",
      label: pick.label,
      meetings: 0,
      marks: 0,
      color: "color" in pick ? pick.color : "",
      icon: "icon" in pick ? pick.icon : "",
    };
    tags.push(tag);
  }
  if (!tag) return vocabulary;

  const meetings = vocabulary.meetings.map((meeting) => ({
    ...meeting,
    tags: meeting.tags.map((held) => ({ ...held })),
  }));
  let meeting = meetings.find((candidate) => candidate.meetingId === meetingId);
  if (!meeting) {
    meeting = { meetingId, tags: [] };
    meetings.push(meeting);
  }
  const held = meeting.tags.find((candidate) => candidate.tagId === tag!.tagId);
  const current = held?.whole ?? false;
  if (current === desired) return vocabulary;

  if (desired) {
    if (held) held.whole = true;
    else meeting.tags.push({ tagId: tag.tagId, whole: true, stretches: 0 });
    tag.marks += 1;
    if (!held) tag.meetings += 1;
  } else if (held) {
    held.whole = false;
    tag.marks = Math.max(0, tag.marks - 1);
    if (held.stretches === 0) {
      meeting.tags = meeting.tags.filter((candidate) => candidate !== held);
      tag.meetings = Math.max(0, tag.meetings - 1);
    }
  }
  return { ...vocabulary, tags, meetings };
}

interface PendingTagAction {
  meeting: MeetingCatalogEntry;
  pick: TagPick;
  desired: boolean;
  optimisticTagId: string;
  request?: AnnotationRequest;
}

export interface ListTagSessionState {
  vocabulary: TagVocabulary | null;
  notice: string;
}

type TagWriteScheduler = (write: () => Promise<void>) => Promise<void>;

// List tagging has two layers: a confirmed server vocabulary and an ordered log
// of clicks not yet confirmed. The store publishes their composition immediately;
// the existing write queue still establishes one order with selection batches.
export function createListTagSession(
  apply: (meeting: MeetingCatalogEntry, request: AnnotationRequest) => Promise<AnnotationResult>,
  schedule: TagWriteScheduler,
) {
  const state = writable<ListTagSessionState>({ vocabulary: null, notice: "" });
  let confirmed: TagVocabulary | null = null;
  let pending: PendingTagAction[] = [];
  let notice = "";

  const identifier = () => crypto.randomUUID?.() ?? Array.from(
    crypto.getRandomValues(new Uint8Array(16)),
    (n) => n.toString(16).padStart(2, "0"),
  ).join("");

  function visible(): TagVocabulary | null {
    if (!confirmed) return null;
    return pending.reduce(
      (vocabulary, action) => withOptimisticWholeTag(
        vocabulary,
        action.meeting.id,
        action.pick,
        action.desired,
        action.optimisticTagId,
      ),
      confirmed,
    );
  }

  function publish() {
    state.set({ vocabulary: visible(), notice });
  }

  function remove(action: PendingTagAction) {
    pending = pending.filter((candidate) => candidate !== action);
  }

  function requestFor(action: PendingTagAction): AnnotationRequest | null {
    if (!confirmed || wholeTagOn(confirmed, action.meeting.id, action.pick) === action.desired) {
      return null;
    }
    const known = tagForPick(confirmed, action.pick);
    if (!action.desired && !known) return null;
    const base = action.desired
      ? markRequest(known ? { tagId: known.tagId, label: known.label } : action.pick, WHOLE_MEETING)
      : untagMeetingRequest(known!.tagId);
    return { ...base, requestId: identifier() };
  }

  async function execute(action: PendingTagAction) {
    try {
      action.request ??= requestFor(action) ?? undefined;
      if (!action.request) return;
      const result = await apply(action.meeting, action.request);
      if (confirmed) confirmed = withMeetingResult(confirmed, result);
    } catch (error) {
      notice = `Could not ${action.desired ? "tag" : "untag"} “${action.meeting.title}”: ${describeAnnotationError(error)}`;
    } finally {
      remove(action);
      publish();
    }
  }

  return {
    subscribe: state.subscribe,
    setConfirmed(vocabulary: TagVocabulary | null) {
      confirmed = vocabulary;
      publish();
    },
    updateConfirmed(update: (vocabulary: TagVocabulary) => TagVocabulary) {
      if (confirmed) confirmed = update(confirmed);
      publish();
    },
    toggle(meeting: MeetingCatalogEntry, pick: TagPick) {
      const current = visible();
      if (!current) return false;
      const action: PendingTagAction = {
        meeting,
        pick,
        desired: !wholeTagOn(current, meeting.id, pick),
        optimisticTagId: "tagId" in pick ? pick.tagId : `optimistic-${identifier()}`,
      };
      pending = [...pending, action];
      notice = "";
      publish();
      void schedule(() => execute(action));
      return true;
    },
    dismissNotice() {
      notice = "";
      publish();
    },
  };
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
  return done === total ? `${verb} ${plural(total, "meeting")}` : `${verb} ${done} of ${total} — ${total - done} failed`;
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
      if (!stopped && !again) {
        loaded(vocabulary);
      }
    } catch (error) {
      const delay = retryDelay(error, attempt);
      attempt += 1;
      if (!stopped && !again) {
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
