import { get, writable } from "svelte/store";
import { createWriteQueue } from "../../viewer/listTags";
import { createMarkIdentities, identifier, markIntent, optimisticMarks, type MarkIntent } from "./optimistic";

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
  // Network activity does not disable editing. Kept for toolbar compatibility.
  busy: boolean;
  saving: boolean;
  retryable: boolean;
  // Colours chosen for new tags, by label, until the vocabulary has them.
  newColors: Record<string, TagColorId>;
  // Whether this session was opened with somewhere to write to (D-775). It
  // lives on the state rather than as a prop on every component because the
  // session is what already knows, and because `write()` silently returning
  // false is not something a reader can see: a control that cannot work has to
  // be absent, not disabled.
  editable: boolean;
}

const OFF: MarksState = { status: "off", annotations: null, error: "", errorFrom: "meeting", busy: false, saving: false, retryable: false, newColors: {}, editable: false };

export interface MarksSessionOptions {
  schedule?: (write: () => Promise<void>) => Promise<void>;
}

interface PendingMark {
  intent: MarkIntent;
  from: MarksState["errorFrom"];
  sent?: AnnotationRequest;
  scheduled: boolean;
}

export function createMarksSession(onChanged: (result: AnnotationResult) => void, options: MarksSessionOptions = {}) {
  const state = writable<MarksState>(OFF);
  const schedule = options.schedule ?? createWriteQueue(() => undefined);
  let identities = createMarkIdentities();
  let confirmed: MeetingAnnotations | null = null;
  let pending: PendingMark[] = [];
  let blocked: PendingMark | null = null;
  let apply: ApplyAnnotations | null = null;
  let loader: LoadAnnotations | null = null;
  let generation = 0;
  let writes = 0;
  let active = true;
  let lastTurn = Promise.resolve();
  let pollTimer: ReturnType<typeof setTimeout> | undefined;
  let retry: ReturnType<typeof setTimeout> | undefined;

  function publish() {
    const visible = confirmed && pending.reduce((value, action) => optimisticMarks(value, action.intent), identities.display(confirmed));
    state.update((s) => ({ ...s, annotations: visible, busy: false, saving: pending.length > 0, retryable: blocked !== null }));
  }

  function receive(answer: MeetingAnnotations) {
    // A receipt replay may describe an older commit than a list/bulk write.
    const epoch = (token?: string) => token?.split(":")[0];
    if (confirmed?.sync && answer.sync && epoch(confirmed.stateToken) === epoch(answer.stateToken) &&
      answer.sync.desired < confirmed.sync.desired) return false;
    confirmed = answer;
    writes++;
    publish();
    return true;
  }

  function schedulePoll() {
    clearTimeout(pollTimer);
    if (!active || pending.length || get(state).status !== "ready") return;
    const sync = confirmed?.sync;
    if (sync?.state === "pending" || sync?.state === "delayed") {
      pollTimer = setTimeout(() => void refresh(), sync.state === "delayed" ? 5000 : 1000);
    }
  }

  async function refresh() {
    if (!loader || pending.length) return;
    const current = generation;
    const version = writes;
    try {
      const answer = await loader();
      if (current !== generation || version !== writes) return;
      receive(answer);
    } catch (error) {
      if (current === generation && version === writes && error instanceof AnnotationError && [401, 403, 404].includes(error.status)) {
        confirmed = null;
        state.update((s) => ({ ...s, annotations: null, status: "failed", error: describeAnnotationError(error) }));
      }
    } finally {
      if (current === generation) schedulePoll();
    }
  }

  async function open(load: LoadAnnotations | null, applyWith: ApplyAnnotations | null, attempt = 0) {
    const current = ++generation;
    clearTimeout(retry);
    clearTimeout(pollTimer);
    identities = createMarkIdentities();
    confirmed = null;
    pending = [];
    blocked = null;
    active = true;
    loader = load;
    apply = applyWith;
    if (!load) { state.set(OFF); return; }
    const editable = applyWith !== null;
    const version = writes;
    state.set({ ...OFF, status: "loading", editable });
    try {
      const annotations = await load();
      if (current === generation) {
        if (version === writes || !confirmed) confirmed = annotations;
        state.set({ ...OFF, status: "ready", annotations: confirmed, editable });
        schedulePoll();
      }
    } catch (error) {
      if (current !== generation) return;
      const delay = retryDelay(error, attempt);
      state.set({ ...OFF, status: delay === null ? "failed" : "preparing", error: delay === null ? describeAnnotationError(error) : "", editable });
      if (delay !== null && active) retry = setTimeout(() => current === generation && void open(load, applyWith, attempt + 1), delay);
    }
  }

  const outcomeUnknown = (error: unknown) => !(error instanceof AnnotationError) || error.status >= 500;

  function report(action: PendingMark, message: string) {
    state.update((s) => ({ ...s, error: message, errorFrom: action.from }));
  }

  function queue(action: PendingMark) {
    if (action.scheduled) return;
    action.scheduled = true;
    const current = generation;
    lastTurn = schedule(async () => {
      if (current !== generation) return;
      action.scheduled = false;
      if (blocked || pending[0] !== action || !apply || !confirmed) return;
      let settled = false;
      try {
        // Compile only at the head of the shared queue, using the latest IDs
        // and precondition. Compilation failures are definitive local refusals.
        if (!action.sent) {
          try {
            action.sent = { ...identities.request(action.intent, confirmed), requestId: identifier(), stateToken: confirmed.stateToken };
          } catch (error) {
            report(action, describeAnnotationError(error));
            settled = true;
            return;
          }
        }
        if (!action.sent.ops.length && !action.sent.retrySync) { settled = true; return; }
        let result: AnnotationResult;
        for (let attempt = 0; ; attempt++) {
          try { result = await apply(action.sent); break; }
          catch (error) {
            const delay = retryDelay(error, attempt) ?? (outcomeUnknown(error) && attempt === 0 ? 250 : null);
            if (delay === null || attempt >= 5) throw error;
            await new Promise((resolve) => setTimeout(resolve, delay));
            if (current !== generation) return;
          }
        }
        if (current !== generation) return;
        identities.reconcile(action.intent, result);
        pending = pending.filter((candidate) => candidate !== action);
        if (receive(result)) onChanged(result);
        settled = true;
      } catch (error) {
        if (current !== generation) return;
        if (outcomeUnknown(error) || error instanceof AnnotationError && error.status === 429) {
          blocked = action;
          report(action, `Could not confirm the annotation update: ${describeAnnotationError(error)}`);
        } else {
          settled = true;
          report(action, describeAnnotationError(error));
          if (error instanceof AnnotationError && error.status === 409 && loader) {
            // Never replay a conflicting move automatically. Refresh before
            // dispatching later actions; their captured sources are revalidated.
            try { receive(await loader()); }
            catch { /* The old token still fails closed on subsequent writes. */ }
          }
        }
      } finally {
        if (current === generation) {
          if (settled) pending = pending.filter((candidate) => candidate !== action);
          publish();
          if (!blocked) pending.forEach(queue);
          schedulePoll();
        }
      }
    });
  }

  // The boolean means accepted into the local queue, not saved on the server.
  // The UI can release the submitted selection immediately and show Saving….
  function write(request: AnnotationRequest, from: MarksState["errorFrom"] = "meeting"): boolean {
    const current = get(state);
    if (!apply || current.status !== "ready" || !current.annotations) return false;
    if (request.ops.some((op) => !["mark", "unmark", "unmark-tag"].includes(op.op))) return false;
    if (request.ops.some((op) => op.op === "mark") && request.ops.some((op) => op.op === "unmark" &&
      !current.annotations?.annotations?.items.some((item) => item.id === op.itemId))) {
      state.update((s) => ({ ...s, error: "This section changed or was removed. Select it again before moving it.", errorFrom: from }));
      return false;
    }
    if (current.annotations.resolved === false && request.ops.some((op) => op.op === "mark")) {
      state.update((s) => ({ ...s, error: "Remove the marks that can't be placed first.", errorFrom: from }));
      return false;
    }
    const action: PendingMark = { intent: markIntent(request, current.annotations), from, scheduled: false };
    pending.push(action);
    writes++;
    clearTimeout(pollTimer);
    if (!blocked) state.update((s) => ({ ...s, error: "" }));
    publish();
    queue(action);
    return true;
  }

  function retryWrite() {
    if (!blocked) return false;
    blocked = null;
    state.update((s) => ({ ...s, error: "" }));
    publish();
    pending.forEach(queue);
    return true;
  }

  return {
    subscribe: state.subscribe, open, write, receive, retryWrite,
    dismissError() { if (!blocked) state.update((s) => ({ ...s, error: "" })); },
    retrySync: () => write({ ops: [], retrySync: true }),
    // A registry retains this session while the overlay is closed. Stop reads,
    // but keep writes and receipts alive so navigating cannot drop user intent.
    suspend() { active = false; clearTimeout(pollTimer); clearTimeout(retry); },
    async resume() {
      active = true;
      if (!confirmed) await open(loader, apply);
      else await refresh();
    },
    async whenIdle() {
      let turn: Promise<void>;
      do { turn = lastTurn; await turn; } while (turn !== lastTurn);
    },
    close: () => open(null, null),
  };
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

  // Deal colours only to tags without saved appearance or a known default.
  // Archived colours remain authoritative, including in standalone recordings.
  const dealt = new Map<string, TagColorId>();
  const seen: { id: string; color: TagColorId }[] = [];
  for (const group of groups) {
    const entry = known.get(group.tag.id);
    const chosen = state.newColors[labelKey(group.tag.label)];
    if (group.tag.color || entry || chosen) {
      seen.push({ id: group.tag.id, color: colorFor({ id: group.tag.id, color: group.tag.color ?? entry?.color ?? chosen }) });
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
      icon: tag.icon === "" || TAG_ICONS.includes(tag.icon as TagIconId) ? (tag.icon as TagIconId | "") : (entry?.icon ?? ""),
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
