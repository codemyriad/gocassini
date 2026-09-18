import { get, writable } from "svelte/store";
import {
  AnnotationError, describeAnnotationError, groupByTag, plural, retryDelay,
  type AnnotationBatchRequest, type AnnotationBatchResult, type AnnotationRequest,
  type TagVocabulary,
} from "./annotations";

export const MAX_TAG_BATCH_MEETINGS = 100;

// Build the complete next vocabulary without publishing intermediate rows.
// Preserve counts outside the changed meetings (including unresolved marks).
export function withAnnotationBatch(current: TagVocabulary, batch: AnnotationBatchResult): TagVocabulary {
  const meetings = new Map(current.meetings.map((meeting) => [meeting.meetingId, meeting]));
  const tags = new Map(current.tags.map((tag) => [tag.tagId, { ...tag }]));
  for (const tag of batch.tags) {
    if (!tags.has(tag.tagId)) tags.set(tag.tagId, { ...tag, meetings: 0, marks: 0 });
  }
  for (const result of batch.results) {
    for (const old of meetings.get(result.meetingId)?.tags ?? []) {
      const tag = tags.get(old.tagId);
      if (tag) { tag.meetings--; tag.marks -= Number(old.whole) + old.stretches; }
    }
    const refs = result.resolved === false ? [] : groupByTag(result.annotations).map(({ tag, whole, stretches }) => ({
      tagId: tag.id, whole: whole !== null, stretches: stretches.length,
    }));
    meetings.set(result.meetingId, { meetingId: result.meetingId, tags: refs });
    for (const ref of refs) {
      const tag = tags.get(ref.tagId);
      if (tag) { tag.meetings++; tag.marks += Number(ref.whole) + ref.stretches; }
    }
  }
  return { ...current, meetings: [...meetings.values()], tags: [...tags.values()].filter((tag) => tag.meetings > 0) };
}

export function createBulkTagSession(
  apply: (request: AnnotationBatchRequest) => Promise<AnnotationBatchResult>,
  committed: (result: AnnotationBatchResult) => void,
) {
  const state = writable({ busy: false, retryable: false, report: "" });
  let pending: { request: AnnotationBatchRequest; remove: boolean } | null = null;

  async function execute(): Promise<boolean> {
    if (!pending || get(state).busy) return false;
    const { request, remove } = pending;
    const count = request.meetingIds.length;
    state.set({ busy: true, retryable: false, report: `${remove ? "Removing tag from" : "Tagging"} ${plural(count, "meeting")}…` });
    try {
      let result: AnnotationBatchResult;
      for (let attempt = 0; ; attempt++) {
        try {
          result = await apply(request);
          break;
        } catch (error) {
          const delay = retryDelay(error, attempt);
          if (delay === null || attempt >= 5) throw error;
          state.set({ busy: true, retryable: false, report: `Waiting to update tags for ${plural(count, "meeting")}… ${describeAnnotationError(error)}` });
          await new Promise((resolve) => setTimeout(resolve, delay));
        }
      }
      committed(result);
      pending = null;
      state.set({ busy: false, retryable: false, report: `${remove ? "Untagged" : "Tagged"} ${plural(count, "meeting")}` });
      return true;
    } catch (error) {
      const retryable = !(error instanceof AnnotationError) || error.status >= 500 || error.status === 429;
      if (!retryable) pending = null;
      state.set({ busy: false, retryable, report: `Could not confirm the tag update: ${describeAnnotationError(error)}` });
      return false;
    }
  }

  return {
    subscribe: state.subscribe,
    async write(meetingIds: readonly string[], request: AnnotationRequest, remove: boolean) {
      // An ambiguous batch must be settled before another edit replaces its key.
      if (get(state).busy || pending) return false;
      if (meetingIds.length === 0) return true;
      if (meetingIds.length > MAX_TAG_BATCH_MEETINGS) {
        state.set({ busy: false, retryable: false, report: `Tag at most ${MAX_TAG_BATCH_MEETINGS} meetings at once.` });
        return false;
      }
      const requestId = crypto.randomUUID?.() ?? Array.from(crypto.getRandomValues(new Uint8Array(16)), (n) => n.toString(16).padStart(2, "0")).join("");
      pending = { request: { ...request, meetingIds: [...meetingIds], requestId }, remove };
      return execute();
    },
    retry: execute,
    clearReport() { if (!pending && !get(state).busy) state.set({ busy: false, retryable: false, report: "" }); },
  };
}
