import {
  groupByTag, type AnnotationBatchResult,
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
