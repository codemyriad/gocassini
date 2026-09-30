import { expect, vi } from "vitest";
import { buildTranscriptIndex } from "../../core/transcript";
import type { TranscriptWordsV1 } from "../../core/types";
import { AnnotationError, type AnnotationItem, type AnnotationRequest, type AnnotationResult,
  type AnnotationTag, type TagVocabulary } from "../../viewer/annotations";
import type { DataProvider } from "../../viewer/dataProvider";
import type { MeetingCatalogEntry } from "../../viewer/catalog";
import type { LoadedArtifact } from "../../viewer/loadArtifact";

const words = ["First", "second", "third", "fourth", "fifth", "sixth"].map((text, i) => ({
  text, startMs: i * 1000, endMs: (i + 1) * 1000,
}));
const transcript: TranscriptWordsV1 = {
  version: "transcript.words.v1", media: { src: "", durationMs: 6000 },
  speakers: [{ id: "ana", label: "Ana" }],
  segments: [{ id: "s1", speaker: "ana", startMs: 0, endMs: 6000,
    text: words.map(word => word.text).join(" "), words }],
};
const artifact: LoadedArtifact = {
  transcript, index: buildTranscriptIndex(transcript), displayTranscript: null,
  readableTranscript: null, summary: null, audioSrc: "", captionsSrc: null, chaptersSrc: null,
  timingPrecision: { level: "word", label: "Word", detail: "" }, metadata: null,
  wordEndsBoundedByAudio: true, availableTranscripts: [], currentTranscriptId: "",
};
const meeting = { id: "m1", title: "Annotation test", dateLabel: "2026-09-28",
  speakerCount: 1, segmentCount: 1, digestDurationMs: 6000 };

export const focusTag: AnnotationTag = { id: "t-focus", label: "Focus", color: "red", icon: "" };
export function savedMark(id: string, tagId: string, target: AnnotationItem["target"]): AnnotationItem {
  return { id, tagId, target, actor: { kind: "person", id: "ana" },
    createdAtUtc: "2026-09-28T12:00:00Z", operationId: `op-${id}` };
}

// A controlled provider, not a second implementation of the backend: each test
// asserts the outgoing request, then supplies an independent saved document.
// Cloned reads prevent optimistic mutations from leaking into the saved fixture.
export function annotationFixture(initialItems: AnnotationItem[] = [], initialTags: AnnotationTag[] = []) {
  let items = structuredClone(initialItems);
  let tags = structuredClone(initialTags);
  let revision = 1;
  const held: { request: AnnotationRequest; resolve: (result: AnnotationResult) => void;
    reject: (error: Error) => void }[] = [];
  const snapshot = (): AnnotationResult => structuredClone({
    meetingId: "m1", revision, stateToken: `epoch:${revision}`, resolved: true,
    sync: { state: "pending", desired: revision, confirmed: 1 },
    annotations: { format: "cassini.annotations.v1", revision, audioOpusSha256: "audio",
      tagNamespace: "ns", tags, items },
    operationId: `op-${revision}`, added: [], removed: [], notFound: [],
  });
  const load = vi.fn(async () => snapshot());
  const apply = vi.fn((_meeting: MeetingCatalogEntry, request: AnnotationRequest) => new Promise<AnnotationResult>((resolve, reject) => {
    held.push({ request: structuredClone(request), resolve, reject });
  }));
  const provider: DataProvider = {
    loadCatalog: async () => ({ version: "cassini.viewer.catalog.v1",
      meetings: [meeting, { ...meeting, id: "m2", title: "Other meeting" }] }),
    loadMeetingForEntry: async () => artifact, loadMeetingSummary: async () => null,
    loadBundledArtifact: async () => artifact, switchTranscript: async () => artifact,
    loadMeetingAnnotations: load, applyAnnotationOps: apply,
    loadTagVocabulary: async (): Promise<TagVocabulary> => ({
      tags: tags.map(tag => ({ tagId: tag.id, namespace: "ns", label: tag.label,
        color: "red", icon: "", meetings: 1, marks: items.filter(item => item.tagId === tag.id).length })),
      meetings: [{ meetingId: "m1", tags: tags.map(tag => ({ tagId: tag.id,
        whole: items.some(item => item.tagId === tag.id && item.target.kind === "meeting"),
        stretches: items.filter(item => item.tagId === tag.id && item.target.kind === "time-range").length })) }],
      coverage: { visible: 2, indexed: 2 },
    }),
  };
  return {
    provider, load, apply, snapshot,
    async acknowledge(savedItems: AnnotationItem[], savedTags: AnnotationTag[] = tags) {
      await expect.poll(() => held.length).toBe(1);
      const next = held.shift()!;
      expect(next.request.stateToken).toBe(`epoch:${revision}`);
      items = structuredClone(savedItems);
      tags = structuredClone(savedTags);
      revision++;
      next.resolve(snapshot());
    },
    dispose() {
      for (const pending of held.splice(0)) pending.reject(new AnnotationError(400, "Test finished"));
    },
  };
}
