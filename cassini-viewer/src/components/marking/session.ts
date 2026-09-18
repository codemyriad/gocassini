import { get, writable } from "svelte/store";

import { stackColumns } from "../../core/marking";
import { formatClockTime } from "../../core/transcript";
import {
  AnnotationError,
  describeAnnotationError,
  groupByTag,
  labelKey,
  retryDelay,
  type AnnotationItem,
  type AnnotationRequest,
  type AnnotationResult,
  type AnnotationTag,
  type MeetingAnnotations,
  type TagPick,
  type VocabularyTag,
} from "../../viewer/annotations";
import { colorFor, leastUsedColor, TAG_ICONS, type TagColorId, type TagIconId } from "../../viewer/tagPalette";

export function pickColor(pick: TagPick, vocabulary: readonly VocabularyTag[]): TagColorId {
  return "tagId" in pick
    ? colorFor(vocabulary.find((tag) => tag.tagId === pick.tagId) ?? { id: pick.tagId })
    : pick.color;
}

export type LoadAnnotations = () => Promise<MeetingAnnotations>;
export type ApplyAnnotations = (request: AnnotationRequest) => Promise<AnnotationResult>;

export interface MarksState {
  status: "off" | "loading" | "preparing" | "ready" | "failed";
  annotations: MeetingAnnotations | null;
  error: string;
  // Where the failed write came from, so its error shows beside it.
  errorFrom: "meeting" | "stretch";
  busy: boolean;
  // Colours chosen for new tags, by label, until the vocabulary has them.
  newColors: Record<string, TagColorId>;
  // Whether this session was opened with somewhere to write to (D-775). It
  // lives on the state rather than as a prop on every component because the
  // session is what already knows, and because `write()` silently returning
  // false is not something a reader can see: a control that cannot work has to
  // be absent, not disabled.
  editable: boolean;
}

const OFF: MarksState = { status: "off", annotations: null, error: "", errorFrom: "meeting", busy: false, newColors: {}, editable: false };

export function createMarksSession(onChanged: (result: AnnotationResult) => void) {
  const state = writable<MarksState>(OFF);
  let apply: ApplyAnnotations | null = null;
  let generation = 0;
  let loader: LoadAnnotations | null = null;
  let writes = 0;
  let pending: { signature: string; request: AnnotationRequest } | null = null;
  let pollTimer: ReturnType<typeof setTimeout> | undefined;

  function schedulePoll() {
    clearTimeout(pollTimer);
    const sync = get(state).annotations?.sync;
    if (sync?.state === "pending" || sync?.state === "delayed") {
      pollTimer = setTimeout(() => void refresh(), sync.state === "delayed" ? 5000 : 1000);
    }
  }

  async function refresh() {
    if (!loader) return;
    if (get(state).busy) {
      schedulePoll();
      return;
    }
    const current = generation;
    const version = writes;
    try {
      const answer = await loader();
      if (current !== generation || version !== writes) return;
      state.update((s) => ({ ...s, annotations: answer }));
    } catch (error) {
      if (current === generation && error instanceof AnnotationError && [401, 403, 404].includes(error.status)) {
        state.update((s) => ({ ...s, annotations: null, status: "failed", error: describeAnnotationError(error) }));
      }
      // Keep acknowledged marks visible during a temporary archive outage.
    } finally {
      if (current === generation) schedulePoll();
    }
  }

  let retry: ReturnType<typeof setTimeout> | undefined;

  async function open(load: LoadAnnotations | null, applyWith: ApplyAnnotations | null, attempt = 0) {
    const current = ++generation;
    clearTimeout(retry);
    clearTimeout(pollTimer);
    pending = null;
    loader = load;
    apply = applyWith;
    if (!load) {
      state.set(OFF);
      return;
    }
    const editable = applyWith !== null;
    state.set({ ...OFF, status: "loading", editable });
    try {
      const annotations = await load();
      if (current === generation) {
        state.set({ ...OFF, status: "ready", annotations, editable });
        schedulePoll();
      }
    } catch (error) {
      if (current !== generation) {
        return;
      }
      const delay = retryDelay(error, attempt);
      state.set({ ...OFF, status: delay === null ? "failed" : "preparing", error: delay === null ? describeAnnotationError(error) : "", editable });
      if (delay !== null) {
        retry = setTimeout(() => current === generation && void open(load, applyWith, attempt + 1), delay);
      }
    }
  }

  // One write at a time: two in flight could answer out of order, and the
  // older document would be the one left on screen.
  async function write(request: AnnotationRequest, from: MarksState["errorFrom"] = "meeting"): Promise<boolean> {
    const current = generation;
    if (!apply || get(state).busy) {
      return false;
    }
    writes++;
    const signature = JSON.stringify(request);
    if (pending?.signature !== signature) {
      const requestId = crypto.randomUUID?.() ?? Array.from(
        crypto.getRandomValues(new Uint8Array(16)),
        (n) => n.toString(16).padStart(2, "0"),
      ).join("");
      pending = { signature, request: { ...request, requestId, stateToken: get(state).annotations?.stateToken } };
    }
    const sent = pending.request;
    state.update((s) => ({ ...s, busy: true, error: "" }));
    try {
      const result = await apply(sent);
      if (current === generation) {
        pending = null;
        writes++;
        const visible = get(state).annotations;
        if (result.sync && visible?.sync && result.sync.desired < visible.sync.desired) {
          state.update((s) => ({ ...s, busy: false }));
          schedulePoll();
          return true;
        }
        onChanged(result);
        state.update((s) => ({
          ...s,
          status: "ready",
          annotations: result,
          busy: false,
          newColors: {
            ...s.newColors,
            ...Object.fromEntries((request.tagStyles ?? []).map((style) => [labelKey(style.label), style.color])),
          },
        }));
        schedulePoll();
      }
      return true;
    } catch (error) {
      if (current === generation) {
        if (error instanceof AnnotationError && error.status < 500) pending = null;
        state.update((s) => ({ ...s, busy: false, error: describeAnnotationError(error), errorFrom: from }));
        if (error instanceof AnnotationError && error.status === 409) void refresh();
      }
      return false;
    }
  }

  return { subscribe: state.subscribe, open, write, retrySync: () => write({ ops: [], retrySync: true }), close: () => open(null, null) };
}

export type MarksSession = ReturnType<typeof createMarksSession>;

interface TagLook {
  tag: AnnotationTag;
  color: TagColorId;
  icon: TagIconId | "";
}

export interface PlacedMark extends TagLook {
  item: AnnotationItem;
  startMs: number;
  endMs: number;
  column: number;
}

interface MarksView {
  whole: TagLook[];
  placed: PlacedMark[];
  lost: AnnotationItem[];
}

export const describeMark = (mark: PlacedMark) =>
  `${mark.tag.label}, ${formatClockTime(mark.startMs)} to ${formatClockTime(mark.endMs)}, marked by ${mark.item.actor.id}`;

// Stretches made against other audio are counted as lost and never placed.
export function viewMarks(state: MarksState, vocabulary: readonly VocabularyTag[]): MarksView {
  const groups = groupByTag(state.annotations?.annotations ?? null);
  const known = new Map(vocabulary.map((tag) => [tag.tagId, tag]));

  // Tags the vocabulary has never heard of are dealt a colour rather than given
  // the hash of their id (D-775). colorFor() hashes each id on its own, so two
  // of a handful of tags landing on the same colour is ordinary, not unlucky:
  // with three tags it happens about a quarter of the time, with five about
  // three times in five. That does not show in the app, where every tag is in
  // the vocabulary — but a recording read on its own carries only ids and
  // labels, so EVERY tag is in that case at once, and same-coloured brackets
  // down a transcript say two marks are the same thing when they are not.
  // Dealing in the document's tag order keeps it deterministic, and reuses the
  // palette's own dealer, so a read-only view looks the way a freshly tagged
  // meeting does. A tag the vocabulary does know is untouched here.
  const dealt = new Map<string, TagColorId>();
  const seen: { id: string; color: TagColorId }[] = [];
  for (const group of groups) {
    const entry = known.get(group.tag.id);
    const chosen = state.newColors[labelKey(group.tag.label)];
    if (entry || chosen) {
      seen.push({ id: group.tag.id, color: colorFor(entry ?? { id: group.tag.id, color: chosen }) });
      continue;
    }
    const color = leastUsedColor(seen);
    dealt.set(group.tag.id, color);
    seen.push({ id: group.tag.id, color });
  }

  const look = (tag: AnnotationTag): TagLook => {
    const entry = known.get(tag.id);
    const dealtColor = dealt.get(tag.id);
    return {
      tag,
		color: dealtColor ?? colorFor({ id: tag.id, color: tag.color ?? entry?.color ?? state.newColors[labelKey(tag.label)] }),
		icon: TAG_ICONS.includes(tag.icon as TagIconId) ? (tag.icon as TagIconId) : (entry?.icon ?? ""),
    };
  };
  const whole = groups.filter((group) => group.whole).map((group) => look(group.tag));
  const items = groups.flatMap((group) =>
    group.stretches.flatMap((item) =>
      item.target.kind === "time-range"
        ? [{ ...look(group.tag), item, startMs: item.target.startMs, endMs: item.target.endMs }]
        : [],
    ),
  );
  if (state.annotations?.resolved === false) {
    return { whole, placed: [], lost: items.map((mark) => mark.item) };
  }
  items.sort((a, b) => a.startMs - b.startMs || a.endMs - b.endMs);
  const columns = stackColumns(items);
  return { whole, placed: items.map((mark, index) => ({ ...mark, column: columns[index]! })), lost: [] };
}
