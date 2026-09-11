import { writable } from "svelte/store";

import { stackColumns } from "../../core/marking";
import {
  AnnotationError,
  moveStretchOps,
  splitByTarget,
  timeRange,
  type AnnotationItem,
  type AnnotationRequest,
  type AnnotationResult,
  type AnnotationTag,
  type AnnotationTarget,
  type MeetingAnnotations,
  type VocabularyTag,
} from "../../viewer/annotations";
import { colorFor, type TagColorId, type TagIconId } from "../../viewer/tagPalette";

// What TagPicker hands back: a tag that exists, or a new one with its colour.
export type TagPick = { tagId: string; label: string } | { label: string; color: TagColorId; icon: "" };

export function pickColor(pick: TagPick, vocabulary: readonly VocabularyTag[]): TagColorId {
  return "tagId" in pick
    ? colorFor(vocabulary.find((tag) => tag.tagId === pick.tagId) ?? { id: pick.tagId })
    : pick.color;
}

export type LoadAnnotations = () => Promise<MeetingAnnotations>;
export type ApplyAnnotations = (request: AnnotationRequest) => Promise<AnnotationResult>;

export function markRequest(pick: TagPick, target: AnnotationTarget): AnnotationRequest {
  if ("tagId" in pick) {
    return { ops: [{ op: "mark", tag: { id: pick.tagId, label: pick.label }, target }] };
  }
  return {
    ops: [{ op: "mark", tag: { label: pick.label }, target }],
    tagStyles: [{ label: pick.label, color: pick.color, icon: "" }],
  };
}

export const stretchRequest = (pick: TagPick, startMs: number, endMs: number) =>
  markRequest(pick, timeRange(startMs, endMs));

export const moveRequest = (itemId: string, tag: AnnotationTag, startMs: number, endMs: number) => ({
  ops: moveStretchOps(itemId, tag, startMs, endMs),
});

export const removeRequest = (itemIds: readonly string[]): AnnotationRequest => ({
  ops: itemIds.map((itemId) => ({ op: "unmark", itemId })),
});

export const untagMeetingRequest = (tagId: string): AnnotationRequest => ({
  ops: [{ op: "unmark-tag", tagId, target: { kind: "meeting" } }],
});

const WRITE_ERRORS: Record<string, string> = {
  unresolved: "Remove the marks that can't be placed first.",
  busy: "Tags are being changed elsewhere. Try again in a moment.",
};

function describe(error: unknown): string {
  if (error instanceof AnnotationError) {
    return WRITE_ERRORS[error.code] ?? error.message;
  }
  return error instanceof Error ? error.message : String(error);
}

export interface MarksState {
  status: "off" | "loading" | "preparing" | "ready" | "failed";
  annotations: MeetingAnnotations | null;
  error: string;
  busy: boolean;
  // Colours chosen for new tags, by label, until the vocabulary has them.
  newColors: Record<string, TagColorId>;
}

const OFF: MarksState = { status: "off", annotations: null, error: "", busy: false, newColors: {} };
export const PREPARING_RETRY_MS = 5000;

export function createMarksSession(onChanged: (result: AnnotationResult) => void) {
  const state = writable<MarksState>(OFF);
  let apply: ApplyAnnotations | null = null;
  let generation = 0;
  let retry: ReturnType<typeof setTimeout> | undefined;

  async function open(load: LoadAnnotations | null, applyWith: ApplyAnnotations | null) {
    const current = ++generation;
    clearTimeout(retry);
    apply = applyWith;
    if (!load) {
      state.set(OFF);
      return;
    }
    state.set({ ...OFF, status: "loading" });
    try {
      const annotations = await load();
      if (current === generation) {
        state.set({ ...OFF, status: "ready", annotations });
      }
    } catch (error) {
      if (current !== generation) {
        return;
      }
      const preparing = error instanceof AnnotationError && error.status === 503;
      state.set({ ...OFF, status: preparing ? "preparing" : "failed", error: preparing ? "" : describe(error) });
      if (preparing) {
        retry = setTimeout(() => current === generation && void open(load, applyWith), PREPARING_RETRY_MS);
      }
    }
  }

  async function write(request: AnnotationRequest): Promise<boolean> {
    const current = generation;
    if (!apply) {
      return false;
    }
    state.update((s) => ({ ...s, busy: true, error: "" }));
    try {
      const result = await apply(request);
      onChanged(result);
      if (current === generation) {
        state.update((s) => ({
          ...s,
          status: "ready",
          annotations: result,
          busy: false,
          newColors: {
            ...s.newColors,
            ...Object.fromEntries((request.tagStyles ?? []).map((style) => [style.label.toLowerCase(), style.color])),
          },
        }));
      }
      return true;
    } catch (error) {
      if (current === generation) {
        state.update((s) => ({ ...s, busy: false, error: describe(error) }));
      }
      return false;
    }
  }

  return { subscribe: state.subscribe, open, write, close: () => open(null, null) };
}

export type MarksSession = ReturnType<typeof createMarksSession>;

export interface TagLook {
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

export interface MarksView {
  whole: TagLook[];
  placed: PlacedMark[];
  lost: AnnotationItem[];
}

// Stretches made against other audio are counted as lost and never placed.
export function viewMarks(state: MarksState, vocabulary: readonly VocabularyTag[]): MarksView {
  const { whole, stretches } = splitByTarget(state.annotations?.annotations ?? null);
  const known = new Map(vocabulary.map((tag) => [tag.tagId, tag]));
  const look = (tag: AnnotationTag): TagLook => {
    const entry = known.get(tag.id);
    return { tag, color: colorFor(entry ?? { id: tag.id, color: state.newColors[tag.label.toLowerCase()] }), icon: entry?.icon ?? "" };
  };
  const items = stretches.flatMap((group) =>
    group.stretches.flatMap((item) =>
      item.target.kind === "time-range"
        ? [{ ...look(group.tag), item, startMs: item.target.startMs, endMs: item.target.endMs }]
        : [],
    ),
  );
  if (state.annotations?.resolved === false) {
    return { whole: whole.map((group) => look(group.tag)), placed: [], lost: items.map((mark) => mark.item) };
  }
  items.sort((a, b) => a.startMs - b.startMs || a.endMs - b.endMs);
  const columns = stackColumns(items);
  return {
    whole: whole.map((group) => look(group.tag)),
    placed: items.map((mark, index) => ({ ...mark, column: columns[index]! })),
    lost: [],
  };
}
