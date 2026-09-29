import {
  labelKey,
  type AnnotationItem,
  type AnnotationOp,
  type AnnotationRequest,
  type AnnotationTarget,
  type MeetingAnnotations,
} from "../../viewer/annotations";

export const identifier = () => crypto.randomUUID?.() ?? Array.from(
  crypto.getRandomValues(new Uint8Array(16)), (n) => n.toString(16).padStart(2, "0"),
).join("");

export const sameTarget = (a: AnnotationTarget, b: AnnotationTarget) => a.kind === b.kind &&
  (a.kind === "meeting" || (b.kind === "time-range" && a.startMs === b.startMs && a.endMs === b.endMs));

export interface MarkIntent {
  request: AnnotationRequest;
  itemIds: string[];
  tagIds: string[];
  tagLabels: Map<string, string>;
  // A move is an atomic remove + add. Remember its source so rebasing cannot
  // recreate a mark another editor removed or moved in the meantime.
  sources: AnnotationItem[];
}

export function markIntent(request: AnnotationRequest, visible: MeetingAnnotations): MarkIntent {
  const doc = visible.annotations;
  const intent: MarkIntent = {
    request: structuredClone(request),
    itemIds: request.ops.map(() => `local-item-${identifier()}`),
    tagIds: request.ops.map((op) => op.op === "mark"
      ? doc?.tags.find((tag) => tag.id === op.tag.id || labelKey(tag.label) === labelKey(op.tag.label))?.id ?? op.tag.id ?? `local-tag-${identifier()}`
      : ""),
    tagLabels: new Map(doc?.tags.map((tag) => [tag.id, tag.label])),
    sources: request.ops.some((op) => op.op === "mark")
      ? request.ops.flatMap((op) => op.op === "unmark" ? doc?.items.filter((item) => item.id === op.itemId) ?? [] : [])
      : [],
  };
  const projected = optimisticMarks(visible, intent).annotations;
  request.ops.forEach((op, index) => {
    if (op.op !== "mark") return;
    const tag = projected?.tags.find((tag) => labelKey(tag.label) === labelKey(op.tag.label));
    const item = projected?.items.find((item) => item.tagId === tag?.id && sameTarget(item.target, op.target));
    if (tag) intent.tagIds[index] = tag.id;
    if (item) intent.itemIds[index] = item.id;
  });
  return intent;
}

// Keep UI identities stable when the server assigns IDs (including the new ID
// produced by a move). Only requests and confirmed documents use server IDs.
export function createMarkIdentities() {
  const items = new Map<string, string>();
  const tags = new Map<string, string>();
  const localItem = (id: string) => [...items].find(([, server]) => server === id)?.[0] ?? id;
  const localTag = (id: string) => [...tags].find(([, server]) => server === id)?.[0] ?? id;
  const serverItem = (id: string) => items.get(id) ?? id;
  const serverTag = (id: string) => tags.get(id) ?? id;

  return {
    display(value: MeetingAnnotations): MeetingAnnotations {
      if (!value.annotations || (items.size === 0 && tags.size === 0)) return value;
      return { ...value, annotations: { ...value.annotations,
        tags: value.annotations.tags.map((tag) => ({ ...tag, id: localTag(tag.id) })),
        items: value.annotations.items.map((item) => ({ ...item, id: localItem(item.id), tagId: localTag(item.tagId) })),
      } };
    },
    reconcile(intent: MarkIntent, answer: MeetingAnnotations) {
      intent.request.ops.forEach((op, index) => {
        if (op.op !== "mark") return;
        const tag = answer.annotations?.tags.find((tag) => tag.id === serverTag(op.tag.id ?? "")) ??
          answer.annotations?.tags.find((tag) => labelKey(tag.label) === labelKey(op.tag.label));
        const item = answer.annotations?.items.find((item) => item.tagId === tag?.id && sameTarget(item.target, op.target));
        if (tag && intent.tagIds[index].startsWith("local-tag-")) tags.set(intent.tagIds[index], tag.id);
        if (item && intent.itemIds[index].startsWith("local-item-")) items.set(intent.itemIds[index], item.id);
      });
    },
    request(intent: MarkIntent, confirmed: MeetingAnnotations): AnnotationRequest {
      const doc = confirmed.annotations;
      for (const source of intent.sources) {
        const current = doc?.items.find((item) => item.id === serverItem(source.id));
        if (!current || current.tagId !== serverTag(source.tagId) || !sameTarget(current.target, source.target)) {
          throw new Error("This section changed or was removed. Select it again before moving it.");
        }
      }
      return { ...intent.request, ops: intent.request.ops.flatMap<AnnotationOp>((op) => {
        switch (op.op) {
          case "mark": {
            const known = doc?.tags.find((tag) => tag.id === serverTag(op.tag.id ?? "")) ??
              doc?.tags.find((tag) => labelKey(tag.label) === labelKey(op.tag.label));
            const id = known?.id ?? (op.tag.id?.startsWith("local-tag-") ? undefined : op.tag.id);
            return [{ ...op, tag: { id, label: known?.label ?? op.tag.label } }];
          }
          case "unmark": {
            const itemId = serverItem(op.itemId);
            return doc?.items.some((item) => item.id === itemId) ? [{ ...op, itemId }] : [];
          }
          case "unmark-tag": {
            const known = doc?.tags.find((tag) => tag.id === serverTag(op.tagId)) ??
              doc?.tags.find((tag) => labelKey(tag.label) === labelKey(intent.tagLabels.get(op.tagId) ?? ""));
            return known ? [{ ...op, tagId: known.id }] : [];
          }
          default: throw new Error("This annotation operation is not supported in the meeting editor.");
        }
      }) };
    },
  };
}

// A small projection of the editor's operations, never a replacement for the
// server's validation. Rebuild from confirmed + pending after every response.
export function optimisticMarks(value: MeetingAnnotations, intent: MarkIntent): MeetingAnnotations {
  const doc = structuredClone(value.annotations ?? {
    format: "cassini.annotations.v1", revision: value.revision,
    audioOpusSha256: "", tagNamespace: "", tags: [], items: [],
  });
  // A dependent move whose creation failed should disappear with its source.
  if (intent.sources.some((source) => !doc.items.some((item) => item.id === source.id))) return value;
  intent.request.ops.forEach((op, index) => {
    switch (op.op) {
      case "mark": {
        let tag = doc.tags.find((tag) => tag.id === op.tag.id) ??
          doc.tags.find((tag) => labelKey(tag.label) === labelKey(op.tag.label));
        if (!tag) {
          const style = intent.request.tagStyles?.find((style) => labelKey(style.label) === labelKey(op.tag.label));
          tag = { id: op.tag.id ?? intent.tagIds[index], label: op.tag.label, color: style?.color, icon: style?.icon };
          doc.tags.push(tag);
        }
        if (!doc.items.some((item) => item.tagId === tag.id && sameTarget(item.target, op.target))) {
          doc.items.push({ id: intent.itemIds[index], tagId: tag.id, target: op.target,
            createdAtUtc: "", actor: { kind: "person", id: "You" }, operationId: "" });
        }
        break;
      }
      case "unmark": doc.items = doc.items.filter((item) => item.id !== op.itemId); break;
      case "unmark-tag": doc.items = doc.items.filter((item) => item.tagId !== op.tagId ||
        (op.target && !sameTarget(item.target, op.target))); break;
    }
  });
  doc.tags = doc.tags.filter((tag) => doc.items.some((item) => item.tagId === tag.id));
  return { ...value, annotations: doc };
}
