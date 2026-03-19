import type {
  DisplayTranscriptV1,
  ReadableTranscriptV1,
  TranscriptSpeaker,
  TranscriptWordsV1,
} from "../core/types";

export interface PortableMeetingManifest {
  meeting?: {
    durationMs?: number;
    createdAtUTC?: string;
    title?: string;
  };
  audio?: {
    sha256?: string;
  };
  speakers?: unknown[];
  transcript?: {
    items?: unknown[];
  };
  readableTranscript?: {
    version?: string;
    speakers?: unknown[];
    segments?: unknown[];
    sourceTranscriptVersion?: string;
  };
  displayTranscript?: {
    version?: string;
    media?: unknown;
    speakers?: unknown[];
    blocks?: unknown[];
    sourceTranscriptVersion?: string;
    sourceReadableTranscriptVersion?: string;
  };
  provenance?: unknown;
}

export async function extractPortableManifestFromArrayBuffer(
  value: ArrayBuffer | Uint8Array,
): Promise<PortableMeetingManifest> {
  const bytes = value instanceof Uint8Array ? value : new Uint8Array(value);
  const tags = parseOpusCommentTags(bytes);
  const chunkCount = safeToInt(tags.CASSINI_PAYLOAD_CHUNK_COUNT, 0);
  if (chunkCount <= 0) {
    throw new Error("missing or invalid CASSINI_PAYLOAD_CHUNK_COUNT");
  }

  let encoded = "";
  for (let index = 0; index < chunkCount; index += 1) {
    const key = `CASSINI_PAYLOAD_${String(index).padStart(3, "0")}`;
    const chunk = tags[key];
    if (!chunk) {
      throw new Error(`missing payload chunk ${key}`);
    }
    encoded += chunk;
  }

  const compressed = decodeBase64Url(encoded);
  const rawManifest = await gunzipBytes(compressed);
  return JSON.parse(new TextDecoder().decode(rawManifest)) as PortableMeetingManifest;
}

export function buildTranscriptWordsFromPortable(
  portable: PortableMeetingManifest,
  mediaSrc = "meeting.opus",
): TranscriptWordsV1 {
  const speakers = normalizeSpeakers(portable.speakers || []);
  const items = Array.isArray(portable.transcript?.items) ? portable.transcript.items : [];
  const segments = items.map((item, index) => {
    const segment = asRecord(item);
    const segmentId =
      typeof segment.id === "string" && segment.id.trim() !== ""
        ? segment.id
        : `seg_${String(index).padStart(6, "0")}`;
    const startMs = safeToInt(segment.startMs, 0);
    const endMs = safeToInt(segment.endMs, startMs);
    const text = typeof segment.text === "string" ? segment.text : "";
    // Portable meetings may contain either true word-level transcript items or
    // older segment-level text spans. Only the single-word case is safe to turn
    // back into a timed transcript word. Multi-word spans would fabricate
    // uniform word timings that were never produced by ASR.
    const words = isSinglePortableWord(text)
      ? splitTextIntoWords(text, startMs, endMs)
      : [];
    const speaker =
      typeof segment.speaker === "string" && segment.speaker.trim() !== "" ? segment.speaker : undefined;

    return {
      id: segmentId,
      speaker,
      startMs,
      endMs,
      text,
      words: words.map((word) => ({
        ...word,
        id: `${segmentId}:${word.id}`,
      })),
    };
  });

  return {
    version: "transcript.words.v1",
    media: {
      src: mediaSrc,
      durationMs: safeToInt(portable.meeting?.durationMs, 0),
      sha256: safeToString(portable.audio?.sha256) || undefined,
    },
    speakers,
    segments,
  };
}

function isSinglePortableWord(text: string): boolean {
  return typeof text === "string" && text.trim().split(/\s+/).filter(Boolean).length <= 1;
}

function extractPortableReadableWords(
  value: Record<string, unknown>,
  segmentId: string,
): TranscriptWordsV1["segments"][number]["words"] {
  if (!Array.isArray(value.words)) {
    return [];
  }
  return value.words.flatMap((wordValue, index) => {
    const word = asRecord(wordValue);
    const text = safeToString(word.text).trim();
    if (!text) {
      return [];
    }
    const startMs = safeToInt(word.startMs, NaN);
    const endMs = safeToInt(word.endMs, startMs);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) {
      return [];
    }
    return [{
      id: typeof word.id === "string" && word.id.trim() !== "" ? word.id : `${segmentId}:w_${index}`,
      text,
      startMs,
      endMs,
    }];
  });
}

function extractTranscriptArtifactWords(
  value: Record<string, unknown>,
  segmentId: string,
): TranscriptWordsV1["segments"][number]["words"] {
  // Runtime portable files need the same synthetic source-word IDs as export
  // so cleaned-word seek still lands on exact transcript timings in the UI.
  if (!Array.isArray(value.words)) {
    return [];
  }
  return value.words.flatMap((wordValue, index) => {
    const word = asRecord(wordValue);
    const text = safeToString(word.text).trim();
    if (!text) {
      return [];
    }
    const startMs = safeToInt(word.startMs, Number.NaN);
    const endMs = safeToInt(word.endMs, startMs);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) {
      return [];
    }
    return [{
      id: typeof word.id === "string" && word.id.trim() !== "" ? word.id : `${segmentId}:w_${index}`,
      text,
      startMs,
      endMs,
    }];
  });
}

export function buildReadableTranscriptFromPortable(
  portable: PortableMeetingManifest,
  transcript: TranscriptWordsV1,
): ReadableTranscriptV1 {
  const provided = asRecord(portable.readableTranscript);
  const speakers = normalizeSpeakers(portable.speakers || transcript.speakers || []);
  const validSpeakerIds = new Set(speakers.map((speaker) => speaker.id));

  // Older portable meetings accidentally embedded cleaned transcripts with the
  // raw transcript version tag. The field name is still authoritative here, so
  // accept either tag when a readable segment payload is present.
  if (
    (provided.version === "transcript.readable.v1" || provided.version === "transcript.words.v1") &&
    Array.isArray(provided.segments)
  ) {
    return {
      version: "transcript.readable.v1",
      media: {
        src: transcript.media.src,
        durationMs: transcript.media.durationMs,
        sha256: transcript.media.sha256,
      },
      speakers: normalizeSpeakers(provided.speakers || speakers),
      segments: provided.segments.map((segmentValue, index) => {
        const segment = asRecord(segmentValue);
        const segmentId =
          typeof segment.id === "string" && segment.id.trim() !== ""
            ? segment.id
            : `readable_${String(index).padStart(6, "0")}`;
        const sourceSegmentIds = Array.isArray(segment.sourceSegmentIds)
          ? segment.sourceSegmentIds.filter((entry): entry is string => typeof entry === "string" && entry.trim() !== "")
          : [];
        const speaker =
          typeof segment.speaker === "string" &&
          segment.speaker.trim() !== "" &&
          validSpeakerIds.has(segment.speaker)
            ? segment.speaker
            : undefined;
        const words = extractPortableReadableWords(segment, segmentId);
        return {
          id: segmentId,
          speaker,
          startMs: safeToInt(segment.startMs, 0),
          endMs: safeToInt(segment.endMs, safeToInt(segment.startMs, 0)),
          text: typeof segment.text === "string" ? segment.text : "",
          sourceSegmentIds,
          ...(words.length > 0 ? ({ words } as object) : {}),
        };
      }),
      sourceTranscriptVersion:
        typeof provided.sourceTranscriptVersion === "string"
          ? provided.sourceTranscriptVersion
          : "transcript.words.v1",
    };
  }

  return {
    version: "transcript.readable.v1",
    media: {
      src: transcript.media.src,
      durationMs: transcript.media.durationMs,
      sha256: transcript.media.sha256,
    },
    speakers,
    sourceTranscriptVersion: "transcript.words.v1",
    segments: groupTranscriptSegmentsAsReadable(transcript.segments || []),
  };
}

interface DisplaySourceBlock {
  id: string;
  speaker?: string;
  speakerLabel: string;
  startMs: number;
  endMs: number;
  text: string;
  sourceSegmentIds: string[];
  words: TranscriptWordsV1["segments"][number]["words"];
  sourceWords: TranscriptWordsV1["segments"][number]["words"];
}

interface TimeWindow {
  startMs: number;
  endMs: number;
}

function buildReadableDisplaySourceBlocks(
  readable: ReadableTranscriptV1,
  speakerLabels: Map<string, string>,
  transcriptSegments: TranscriptWordsV1["segments"],
): DisplaySourceBlock[] {
  const segmentById = new Map((Array.isArray(transcriptSegments) ? transcriptSegments : []).map((segment) => [segment.id, segment]));
  const baseBlocks = readable.segments.map((segment, index) => {
    const segmentRecord = asRecord(segment as unknown);
    const blockId =
      typeof segment.id === "string" && segment.id.trim() !== ""
        ? segment.id
        : `rseg_${String(index).padStart(6, "0")}`;
    const sourceSegmentIds = Array.isArray(segment.sourceSegmentIds) ? [...segment.sourceSegmentIds] : [];
    return {
      id: blockId,
      speaker: segment.speaker,
      speakerLabel: segment.speaker
        ? speakerLabels.get(segment.speaker) || segment.speaker
        : "Unknown speaker",
      startMs: safeToInt(segment.startMs, 0),
      endMs: safeToInt(segment.endMs, safeToInt(segment.startMs, 0)),
      text: typeof segment.text === "string" ? segment.text : "",
      sourceSegmentIds,
      words: extractPortableReadableWords(segmentRecord, blockId),
      sourceWords: collectReadableSourceWords({
        segment,
        sourceSegmentIds,
        segmentById,
        transcriptSegments,
      }),
    } satisfies DisplaySourceBlock;
  });
  return splitReadableBlocksOnInterruptions(baseBlocks);
}

function collectReadableSourceWords({
  segment,
  sourceSegmentIds,
  segmentById,
  transcriptSegments,
}: {
  segment: ReadableTranscriptV1["segments"][number];
  sourceSegmentIds: string[];
  segmentById: Map<string, TranscriptWordsV1["segments"][number]>;
  transcriptSegments: TranscriptWordsV1["segments"];
}): TranscriptWordsV1["segments"][number]["words"] {
  const resolved = sourceSegmentIds
    .map((segmentId) => segmentById.get(segmentId))
    .filter((candidate): candidate is TranscriptWordsV1["segments"][number] => Boolean(candidate));
  const candidates = resolved.length > 0
    ? resolved
    : (Array.isArray(transcriptSegments) ? transcriptSegments : []).filter((candidate) => {
        const startMs = safeToInt(candidate?.startMs, 0);
        const endMs = safeToInt(candidate?.endMs, startMs);
        if (endMs < safeToInt(segment?.startMs, 0) || startMs > safeToInt(segment?.endMs, startMs)) {
          return false;
        }
        if (typeof segment?.speaker === "string" && candidate?.speaker && candidate.speaker !== segment.speaker) {
          return false;
        }
        return true;
      });
  return candidates.flatMap((candidate) =>
    Array.isArray(candidate?.words)
      ? candidate.words
          .filter((word) => word && typeof word === "object")
          .map((word, index) => ({
            id: typeof word.id === "string" && word.id.trim() !== "" ? word.id : `${candidate.id}:w_${index}`,
            text: safeToString(word.text).trim(),
            startMs: safeToInt(word.startMs, Number.NaN),
            endMs: safeToInt(word.endMs, safeToInt(word.startMs, Number.NaN)),
          }))
          .filter((word) => word.text && Number.isFinite(word.startMs) && Number.isFinite(word.endMs))
      : [],
  );
}

function splitReadableBlocksOnInterruptions(blocks: DisplaySourceBlock[]): DisplaySourceBlock[] {
  return blocks.flatMap((block, blockIndex) => splitReadableBlockOnInterruptions(block, blockIndex, blocks));
}

function splitReadableBlockOnInterruptions(
  block: DisplaySourceBlock,
  blockIndex: number,
  allBlocks: DisplaySourceBlock[],
): DisplaySourceBlock[] {
  if (block.words.length === 0 || !block.speaker) {
    return [block];
  }

  const interruptions = mergeTimeWindows(
    allBlocks
      .filter((other, index) => index !== blockIndex && other.speaker && other.speaker !== block.speaker)
      .map((other) => ({
        startMs: Math.max(block.startMs, other.startMs),
        endMs: Math.min(block.endMs, other.endMs),
      }))
      .filter((window) => window.endMs - window.startMs >= 400),
  );
  const windows = subtractTimeWindows(
    {
      startMs: block.startMs,
      endMs: block.endMs,
    },
    interruptions,
  ).filter((window) => window.endMs - window.startMs >= 250);

  if (windows.length <= 1) {
    return [block];
  }

  const transcriptWordCounts =
    Array.isArray(block.sourceWords) && block.sourceWords.length > 0
      ? assignWordsToWindows(block.sourceWords, windows)
      : null;
  const uniformlyInterpolated = wordsLookUniformlyInterpolated(block.words, block.startMs, block.endMs);
  const counts = transcriptWordCounts && transcriptWordCounts.some((count) => count > 0)
    ? transcriptWordCounts
    : uniformlyInterpolated
      ? allocateWordCountsAcrossWindows(block.words.length, windows)
      : assignWordsToWindows(block.words, windows);
  const plan = windows
    .map((window, index) => ({
      window,
      count: counts[index] ?? 0,
    }))
    .filter((entry) => entry.count > 0);

  if (plan.length <= 1) {
    return [block];
  }

  const textParts = splitTextByWordCounts(block.text, plan.map((entry) => entry.count));
  const derivedBlocks: DisplaySourceBlock[] = [];
  let wordOffset = 0;
  let sourceWordOffset = 0;
  for (let index = 0; index < plan.length; index += 1) {
    const { window, count } = plan[index]!;
    const words = block.words.slice(wordOffset, wordOffset + count);
    const sourceWords = Array.isArray(block.sourceWords)
      ? block.sourceWords.slice(sourceWordOffset, sourceWordOffset + count)
      : [];
    wordOffset += count;
    sourceWordOffset += count;
    if (words.length === 0) {
      continue;
    }
    const timedWords = uniformlyInterpolated ? retimeWordsWithinWindow(words, window) : words;
    derivedBlocks.push({
      ...block,
      id: `${block.id}__split_${String(index).padStart(2, "0")}`,
      startMs: sourceWords[0]?.startMs ?? timedWords[0]?.startMs ?? window.startMs,
      endMs: sourceWords[sourceWords.length - 1]?.endMs ?? timedWords[timedWords.length - 1]?.endMs ?? window.endMs,
      text: textParts[index] ?? rebuildTextFromWords(timedWords),
      words: timedWords,
      sourceWords,
      sourceSegmentIds: sourceWords.length > 0
        ? [...new Set(sourceWords.map((word) => String(word.id ?? "").split(":")[0]).filter(Boolean))]
        : block.sourceSegmentIds,
    });
  }

  return derivedBlocks.length > 0 ? derivedBlocks : [block];
}

function mergeTimeWindows(windows: TimeWindow[]): TimeWindow[] {
  const sorted = [...windows].sort((left, right) => left.startMs - right.startMs);
  const merged: TimeWindow[] = [];
  for (const window of sorted) {
    const previous = merged.at(-1);
    if (!previous || window.startMs > previous.endMs) {
      merged.push({ ...window });
      continue;
    }
    previous.endMs = Math.max(previous.endMs, window.endMs);
  }
  return merged;
}

function subtractTimeWindows(range: TimeWindow, removals: TimeWindow[]): TimeWindow[] {
  const windows: TimeWindow[] = [];
  let cursor = range.startMs;
  for (const removal of removals) {
    if (removal.endMs <= cursor) {
      continue;
    }
    if (removal.startMs > cursor) {
      windows.push({
        startMs: cursor,
        endMs: Math.min(removal.startMs, range.endMs),
      });
    }
    cursor = Math.max(cursor, removal.endMs);
    if (cursor >= range.endMs) {
      break;
    }
  }
  if (cursor < range.endMs) {
    windows.push({
      startMs: cursor,
      endMs: range.endMs,
    });
  }
  return windows.filter((window) => window.endMs > window.startMs);
}

function wordsLookUniformlyInterpolated(
  words: TranscriptWordsV1["segments"][number]["words"],
  startMs: number,
  endMs: number,
): boolean {
  if (words.length === 0) {
    return false;
  }
  const span = Math.max(0, endMs - startMs);
  const toleranceMs = Math.max(25, Math.floor(span / Math.max(words.length, 1) / 5));
  return words.every((word, index) => {
    const expectedStart = words.length <= 1
      ? startMs
      : startMs + Math.floor((span * index) / words.length);
    const expectedEnd = words.length <= 1
      ? endMs
      : startMs + Math.floor((span * (index + 1)) / words.length);
    return (
      Math.abs(safeToInt(word.startMs, expectedStart) - expectedStart) <= toleranceMs &&
      Math.abs(safeToInt(word.endMs, expectedEnd) - expectedEnd) <= toleranceMs
    );
  });
}

function allocateWordCountsAcrossWindows(totalWords: number, windows: TimeWindow[]): number[] {
  const totalDuration = windows.reduce((sum, window) => sum + Math.max(0, window.endMs - window.startMs), 0);
  if (totalDuration <= 0) {
    return windows.map((_, index) => (index === windows.length - 1 ? totalWords : 0));
  }

  const counts = windows.map((window) =>
    Math.floor((totalWords * Math.max(0, window.endMs - window.startMs)) / totalDuration),
  );
  let assigned = counts.reduce((sum, count) => sum + count, 0);
  const rankedRemainders = windows
    .map((window, index) => ({
      index,
      remainder:
        ((totalWords * Math.max(0, window.endMs - window.startMs)) / totalDuration) - (counts[index] ?? 0),
    }))
    .sort((left, right) => right.remainder - left.remainder);
  for (const { index } of rankedRemainders) {
    if (assigned >= totalWords) {
      break;
    }
    counts[index] = (counts[index] ?? 0) + 1;
    assigned += 1;
  }
  if (assigned < totalWords) {
    counts[counts.length - 1] = (counts[counts.length - 1] ?? 0) + (totalWords - assigned);
  }
  return counts;
}

function assignWordsToWindows(
  words: TranscriptWordsV1["segments"][number]["words"],
  windows: TimeWindow[],
): number[] {
  const counts = windows.map(() => 0);
  for (const word of words) {
    const midpoint = Math.floor((safeToInt(word.startMs, 0) + safeToInt(word.endMs, 0)) / 2);
    let winnerIndex = windows.findIndex((window) => midpoint >= window.startMs && midpoint < window.endMs);
    if (winnerIndex < 0) {
      winnerIndex = windows.reduce((bestIndex, window, index) => {
        const bestWindow = windows[bestIndex]!;
        const bestDistance = distanceToWindow(midpoint, bestWindow);
        const nextDistance = distanceToWindow(midpoint, window);
        return nextDistance < bestDistance ? index : bestIndex;
      }, 0);
    }
    counts[winnerIndex] = (counts[winnerIndex] ?? 0) + 1;
  }
  return counts;
}

function distanceToWindow(value: number, window: TimeWindow): number {
  if (value < window.startMs) {
    return window.startMs - value;
  }
  if (value > window.endMs) {
    return value - window.endMs;
  }
  return 0;
}

function retimeWordsWithinWindow(
  words: TranscriptWordsV1["segments"][number]["words"],
  window: TimeWindow,
): TranscriptWordsV1["segments"][number]["words"] {
  if (words.length === 0) {
    return [];
  }
  const span = Math.max(0, window.endMs - window.startMs);
  return words.map((word, index) => ({
    ...word,
    startMs: words.length <= 1 ? window.startMs : window.startMs + Math.floor((span * index) / words.length),
    endMs: words.length <= 1 ? window.endMs : window.startMs + Math.floor((span * (index + 1)) / words.length),
  }));
}

function splitTextByWordCounts(text: string, counts: number[]): string[] {
  const tokens = tokenizeDisplayText(text);
  const parts: string[] = [];
  let tokenStart = 0;
  for (const count of counts) {
    let tokenEnd = tokenStart;
    let wordsSeen = 0;
    while (tokenEnd < tokens.length && wordsSeen < count) {
      if (tokens[tokenEnd]?.kind === "word") {
        wordsSeen += 1;
      }
      tokenEnd += 1;
    }
    while (tokenEnd < tokens.length && tokens[tokenEnd]?.kind !== "word") {
      tokenEnd += 1;
    }
    parts.push(rebuildTextFromTokens(tokens.slice(tokenStart, tokenEnd)));
    tokenStart = tokenEnd;
  }
  if (parts.length > 0 && tokenStart < tokens.length) {
    parts[parts.length - 1] = `${parts[parts.length - 1]}${rebuildTextFromTokens(tokens.slice(tokenStart))}`;
  }
  return parts;
}

function rebuildTextFromTokens(
  tokens: Array<{ text: string; spaceBefore: boolean }>,
): string {
  let text = "";
  for (const token of tokens) {
    if (token.spaceBefore && text !== "") {
      text += " ";
    }
    text += token.text;
  }
  return text;
}

function rebuildTextFromWords(words: TranscriptWordsV1["segments"][number]["words"]): string {
  return words.map((word) => word.text).join(" ");
}

export function buildDisplayTranscriptFromArtifacts(
  transcript: TranscriptWordsV1,
  readable: ReadableTranscriptV1 | null,
): DisplayTranscriptV1 {
  const speakers = normalizeSpeakers(transcript.speakers || []);
  const speakerLabels = new Map(speakers.map((speaker) => [speaker.id, speaker.label]));
  const transcriptSegments = Array.isArray(transcript.segments) ? transcript.segments : [];
  const segmentById = new Map(transcriptSegments.map((segment) => [segment.id, segment]));
  const sourceBlocks =
    readable && readable.version === "transcript.readable.v1" && Array.isArray(readable.segments)
      ? buildReadableDisplaySourceBlocks(readable, speakerLabels, transcriptSegments)
      : transcriptSegments.map((segment) => ({
          id: `d_${segment.id}`,
          speaker: segment.speaker,
          speakerLabel: segment.speaker ? speakerLabels.get(segment.speaker) || segment.speaker : "Unknown speaker",
          startMs: segment.startMs,
          endMs: segment.endMs,
          text: segment.text,
          sourceSegmentIds: [segment.id],
          words: [],
          sourceWords: Array.isArray(segment.words) ? segment.words : [],
        }));

  return {
    version: "transcript.display.v1",
    media: { ...transcript.media },
    speakers,
    blocks: sourceBlocks.map((block, blockIndex) => {
      const sourceSegmentIds = Array.isArray(block.sourceSegmentIds)
        ? block.sourceSegmentIds.filter((value): value is string => typeof value === "string" && value.trim() !== "")
        : [];
      const sourceSegments = resolveDisplaySourceSegments({
        block,
        sourceSegmentIds,
        segmentById,
        transcriptSegments,
      });
      const sourceWordsFromTranscript = sourceSegments.flatMap((segment, index) =>
        extractTranscriptArtifactWords(
          asRecord(segment as unknown),
          typeof segment.id === "string" && segment.id.trim() !== ""
            ? segment.id
            : `seg_${String(index).padStart(6, "0")}`,
        ),
      );
      const sourceWordsFromReadable = extractPortableReadableWords(
        asRecord(block),
        `block_${String(blockIndex).padStart(6, "0")}`,
      );
      const sourceWords = sourceWordsFromTranscript.length > 0 ? sourceWordsFromTranscript : sourceWordsFromReadable;
      const sourceWordById = new Map(
        sourceWords
          .filter((word): word is NonNullable<typeof word> & { id: string } => typeof word.id === "string" && word.id.trim() !== "")
          .map((word) => [word.id, word]),
      );
      const alignment = alignReadableTokensToSourceWords(
        sourceWords,
        typeof block.text === "string" ? block.text : "",
      );
      const fallbackStartMs =
        sourceWords.length > 0
          ? safeToInt(sourceWords[0]?.startMs, safeToInt(block.startMs, 0))
          : sourceSegments.length > 0
            ? safeToInt(sourceSegments[0]?.startMs, safeToInt(block.startMs, 0))
            : safeToInt(block.startMs, 0);
      const fallbackEndMs =
        sourceWords.length > 0
          ? safeToInt(sourceWords[sourceWords.length - 1]?.endMs, safeToInt(block.endMs, fallbackStartMs))
          : sourceSegments.length > 0
            ? safeToInt(
                sourceSegments[sourceSegments.length - 1]?.endMs,
                safeToInt(block.endMs, fallbackStartMs),
              )
            : safeToInt(block.endMs, fallbackStartMs);
      const exactTokens = alignment.tokens.map((token) => {
        const matchedWords = token.sourceWordIds
          .map((wordId) => sourceWordById.get(wordId))
          .filter((word): word is NonNullable<typeof word> => Boolean(word));
        if (matchedWords.length > 0) {
          return {
            text: token.text,
            spaceBefore: token.spaceBefore,
            kind: token.kind,
            sourceWordIds: [...token.sourceWordIds],
            startMs: Math.min(...matchedWords.map((word) => safeToInt(word.startMs, fallbackStartMs))),
            endMs: Math.max(...matchedWords.map((word) => safeToInt(word.endMs, fallbackEndMs))),
            alignment: "source" as const,
          };
        }
        return {
          text: token.text,
          spaceBefore: token.spaceBefore,
          kind: token.kind,
          sourceWordIds: [...token.sourceWordIds],
          alignment: "none" as const,
        };
      });
      const tokens = interpolateUntimedWordRuns(exactTokens, fallbackStartMs, fallbackEndMs);
      const wordCount = tokens.filter((token) => token.kind === "word").length;
      const timedWordCount = tokens.filter(
        (token) =>
          token.kind === "word" &&
          Number.isInteger(token.startMs) &&
          Number.isInteger(token.endMs),
      ).length;
      const timedTokens = tokens.filter(
        (token) =>
          Number.isInteger(token.startMs) &&
          Number.isInteger(token.endMs),
      );
      return {
        id:
          typeof block.id === "string" && block.id.trim() !== ""
            ? block.id
            : `dseg_${String(blockIndex).padStart(6, "0")}`,
        speaker:
          typeof block.speaker === "string" && speakerLabels.has(block.speaker) ? block.speaker : undefined,
        speakerLabel:
          typeof block.speakerLabel === "string" && block.speakerLabel.trim() !== ""
            ? block.speakerLabel
            : speakerLabels.get(block.speaker) || "Unknown speaker",
        startMs: timedTokens.length > 0 ? safeToInt(timedTokens[0]?.startMs, fallbackStartMs) : fallbackStartMs,
        endMs:
          timedTokens.length > 0
            ? safeToInt(timedTokens[timedTokens.length - 1]?.endMs, fallbackEndMs)
            : fallbackEndMs,
        text: typeof block.text === "string" ? block.text : "",
        sourceSegmentIds,
        wordCount,
        timedWordCount,
        timingCoverage: wordCount === 0 ? 1 : timedWordCount / wordCount,
        tokens,
      };
    }),
    sourceTranscriptVersion: transcript.version,
    sourceReadableTranscriptVersion: readable?.version,
  };
}

function interpolateUntimedWordRuns(
  tokens: DisplayTranscriptV1["blocks"][number]["tokens"],
  fallbackStartMs: number,
  fallbackEndMs: number,
): DisplayTranscriptV1["blocks"][number]["tokens"] {
  const next = tokens.map((token) => ({ ...token, sourceWordIds: [...token.sourceWordIds] }));
  const wordTokenIndexes = next
    .map((token, index) => (token.kind === "word" ? index : -1))
    .filter((index) => index >= 0);

  let cursor = 0;
  while (cursor < wordTokenIndexes.length) {
    const tokenIndex = wordTokenIndexes[cursor];
    if (tokenHasTiming(next[tokenIndex])) {
      cursor += 1;
      continue;
    }

    const runStart = cursor;
    while (cursor < wordTokenIndexes.length && !tokenHasTiming(next[wordTokenIndexes[cursor]])) {
      cursor += 1;
    }
    const runTokenIndexes = wordTokenIndexes.slice(runStart, cursor);
    const prevTimedToken = runStart > 0 ? next[wordTokenIndexes[runStart - 1]] : null;
    const nextTimedToken = cursor < wordTokenIndexes.length ? next[wordTokenIndexes[cursor]] : null;
    const hasPrevAnchor = tokenHasTiming(prevTimedToken);
    const hasNextAnchor = tokenHasTiming(nextTimedToken);
    const isEntirelyUntimedBlock = !hasPrevAnchor && !hasNextAnchor && runTokenIndexes.length === wordTokenIndexes.length;
    if (!isEntirelyUntimedBlock && (!hasPrevAnchor || !hasNextAnchor)) {
      continue;
    }
    const { startMs, endMs } = resolveInterpolatedSpan({
      prevTimedToken,
      nextTimedToken,
      fallbackStartMs,
      fallbackEndMs,
    });
    const span = Math.max(0, endMs - startMs);

    // Preserve exact source matches when we have them, but keep rewritten runs
    // seekable by spreading them across the surrounding source span.
    for (let index = 0; index < runTokenIndexes.length; index += 1) {
      const runTokenIndex = runTokenIndexes[index];
      const tokenStart =
        runTokenIndexes.length <= 1
          ? startMs
          : startMs + Math.floor((span * index) / runTokenIndexes.length);
      const tokenEnd =
        runTokenIndexes.length <= 1
          ? endMs
          : startMs + Math.floor((span * (index + 1)) / runTokenIndexes.length);
      next[runTokenIndex] = {
        ...next[runTokenIndex],
        startMs: tokenStart,
        endMs: Math.max(tokenEnd, tokenStart),
        alignment: "interpolated" as const,
      };
    }
  }

  return next;
}

function resolveInterpolatedSpan({
  prevTimedToken,
  nextTimedToken,
  fallbackStartMs,
  fallbackEndMs,
}: {
  prevTimedToken: DisplayTranscriptV1["blocks"][number]["tokens"][number] | null;
  nextTimedToken: DisplayTranscriptV1["blocks"][number]["tokens"][number] | null;
  fallbackStartMs: number;
  fallbackEndMs: number;
}): { startMs: number; endMs: number } {
  let startMs = tokenHasTiming(prevTimedToken) ? safeToInt(prevTimedToken.endMs, fallbackStartMs) : fallbackStartMs;
  let endMs = tokenHasTiming(nextTimedToken) ? safeToInt(nextTimedToken.startMs, fallbackEndMs) : fallbackEndMs;

  if (endMs <= startMs) {
    const altStartMs = tokenHasTiming(prevTimedToken)
      ? safeToInt(prevTimedToken.startMs, fallbackStartMs)
      : fallbackStartMs;
    const altEndMs = tokenHasTiming(nextTimedToken)
      ? safeToInt(nextTimedToken.endMs, fallbackEndMs)
      : fallbackEndMs;
    startMs = Math.min(altStartMs, altEndMs);
    endMs = Math.max(altStartMs, altEndMs);
  }

  return {
    startMs,
    endMs: Math.max(endMs, startMs),
  };
}

function tokenHasTiming(token: DisplayTranscriptV1["blocks"][number]["tokens"][number] | null | undefined): boolean {
  return Boolean(token) && Number.isInteger(token.startMs) && Number.isInteger(token.endMs);
}

export function describeMeeting(meetingId: string): { title: string; dateLabel: string } {
  const normalizedMeetingId = stripVariantSuffix(meetingId);
  const colonTimeStamp = parseTimestampFromDoubledDashParts(normalizedMeetingId, "--");
  if (colonTimeStamp) {
    return colonTimeStamp;
  }

  const modernStamp = /^(.*)--(\d{8})T(\d{2})(\d{2})(\d{2})$/.exec(normalizedMeetingId);
  if (modernStamp) {
    const [, rawTitle, yyyymmdd, hour, minute] = modernStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${yyyymmdd.slice(0, 4)}-${yyyymmdd.slice(4, 6)}-${yyyymmdd.slice(6, 8)} ${hour}:${minute}`,
    };
  }

  const legacyStamp = /^(.*)--(\d{4})-(\d{2})-(\d{2})--(\d{2})-(\d{2})-(\d{2})$/.exec(normalizedMeetingId);
  if (legacyStamp) {
    const [, rawTitle, year, month, day, hour, minute] = legacyStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
    };
  }

  return {
    title: toTitleCase(normalizedMeetingId),
    dateLabel: normalizedMeetingId,
  };
}

export function describeVariantSuffix(meetingId: string): string {
  const match = /--stt-([A-Za-z0-9._-]+)$/.exec(meetingId);
  if (!match) {
    return "";
  }
  return toTitleCase(match[1].replace(/\./g, "-"));
}

function parseOpusCommentTags(bytes: Uint8Array): Record<string, string> {
  const packets = extractOggPackets(bytes, 2);
  if (packets.length < 2) {
    throw new Error("portable opus file is missing OpusTags");
  }
  const tagsPacket = packets[1];
  const signature = new TextDecoder().decode(tagsPacket.subarray(0, 8));
  if (signature !== "OpusTags") {
    throw new Error("portable opus file does not contain a valid OpusTags packet");
  }

  const view = new DataView(tagsPacket.buffer, tagsPacket.byteOffset, tagsPacket.byteLength);
  let offset = 8;
  const vendorLength = view.getUint32(offset, true);
  offset += 4 + vendorLength;
  if (offset + 4 > tagsPacket.byteLength) {
    throw new Error("portable opus file has a truncated OpusTags vendor string");
  }

  const commentCount = view.getUint32(offset, true);
  offset += 4;
  const tags: Record<string, string> = {};
  const decoder = new TextDecoder();

  for (let index = 0; index < commentCount; index += 1) {
    if (offset + 4 > tagsPacket.byteLength) {
      throw new Error("portable opus file has a truncated OpusTags comment header");
    }
    const length = view.getUint32(offset, true);
    offset += 4;
    if (offset + length > tagsPacket.byteLength) {
      throw new Error("portable opus file has a truncated OpusTags comment value");
    }
    const comment = decoder.decode(tagsPacket.subarray(offset, offset + length));
    offset += length;
    const separator = comment.indexOf("=");
    if (separator <= 0) {
      continue;
    }
    tags[comment.slice(0, separator)] = comment.slice(separator + 1);
  }

  return tags;
}

function extractOggPackets(bytes: Uint8Array, targetPacketCount: number): Uint8Array[] {
  const packets: Uint8Array[] = [];
  let offset = 0;
  let currentPacketParts: Uint8Array[] = [];
  let currentPacketLength = 0;

  while (offset + 27 <= bytes.byteLength && packets.length < targetPacketCount) {
    if (readAscii(bytes, offset, 4) !== "OggS") {
      throw new Error("portable opus file is not a valid Ogg stream");
    }
    const pageSegments = bytes[offset + 26] ?? 0;
    const headerLength = 27 + pageSegments;
    if (offset + headerLength > bytes.byteLength) {
      throw new Error("portable opus file has a truncated Ogg page header");
    }
    const segmentTable = bytes.subarray(offset + 27, offset + 27 + pageSegments);
    const pageDataLength = segmentTable.reduce((sum, value) => sum + value, 0);
    const pageDataStart = offset + headerLength;
    const pageDataEnd = pageDataStart + pageDataLength;
    if (pageDataEnd > bytes.byteLength) {
      throw new Error("portable opus file has a truncated Ogg page payload");
    }
    const pageData = bytes.subarray(pageDataStart, pageDataEnd);

    let cursor = 0;
    for (const segmentLength of segmentTable) {
      const nextCursor = cursor + segmentLength;
      currentPacketParts.push(pageData.subarray(cursor, nextCursor));
      currentPacketLength += segmentLength;
      cursor = nextCursor;
      if (segmentLength < 255) {
        packets.push(concatenateChunks(currentPacketParts, currentPacketLength));
        currentPacketParts = [];
        currentPacketLength = 0;
        if (packets.length >= targetPacketCount) {
          break;
        }
      }
    }

    offset = pageDataEnd;
  }

  return packets;
}

function readAscii(bytes: Uint8Array, offset: number, length: number): string {
  return new TextDecoder().decode(bytes.subarray(offset, offset + length));
}

function concatenateChunks(chunks: Uint8Array[], totalLength: number): Uint8Array {
  const out = new Uint8Array(totalLength);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

function decodeBase64Url(value: string): Uint8Array {
  const normalized = value.replace(/-/g, "+").replace(/_/g, "/");
  const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
  const binary = atob(padded);
  const out = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    out[index] = binary.charCodeAt(index);
  }
  return out;
}

async function gunzipBytes(bytes: Uint8Array): Promise<Uint8Array> {
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("gzip"));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

function groupTranscriptSegmentsAsReadable(
  segments: TranscriptWordsV1["segments"],
): ReadableTranscriptV1["segments"] {
  const grouped: ReadableTranscriptV1["segments"] = [];
  let current:
    | {
        speaker?: string;
        startMs: number;
        endMs: number;
        wordCount: number;
        text: string;
        sourceSegments: TranscriptWordsV1["segments"];
      }
    | null = null;
  const hardGapMs = 4200;
  const softGapMs = 2200;
  const targetParagraphWords = 96;
  const targetParagraphDurationMs = 45_000;
  const maxParagraphWords = 140;
  const maxParagraphDurationMs = 90_000;
  const minStandaloneWords = 18;
  const minStandaloneDurationMs = 8000;

  const flush = () => {
    if (!current || current.sourceSegments.length === 0) {
      current = null;
      return;
    }
    grouped.push({
      id: `r_${current.sourceSegments[0]?.id ?? "segment"}`,
      speaker: current.speaker,
      startMs: current.startMs,
      endMs: current.endMs,
      text: joinSegmentTexts(current.sourceSegments),
      sourceSegmentIds: current.sourceSegments.map((segment) => segment.id),
    });
    current = null;
  };

  const normalizedSegments = Array.isArray(segments) ? segments : [];
  for (const segment of normalizedSegments) {
    const speaker = typeof segment.speaker === "string" && segment.speaker.trim() !== "" ? segment.speaker : undefined;
    const startMs = safeToInt(segment.startMs, 0);
    const endMs = safeToInt(segment.endMs, startMs);
    const segmentWordCount =
      Array.isArray(segment.words) && segment.words.length > 0
        ? segment.words.length
        : countWordsInText(segment.text);

    if (!current) {
      current = {
        speaker,
        startMs,
        endMs,
        wordCount: segmentWordCount,
        text: safeToString(segment.text).trim(),
        sourceSegments: [segment],
      };
      continue;
    }

    const gapMs = Math.max(0, startMs - current.endMs);
    const durationMs = Math.max(0, endMs - current.startMs);
    const nextWordCount = current.wordCount + segmentWordCount;
    const speakerChanged = current.speaker !== speaker;
    const paragraphTargetReached =
      current.wordCount >= targetParagraphWords ||
      current.endMs - current.startMs >= targetParagraphDurationMs;
    const naturalBoundary = endsSentence(current.text) || gapMs >= softGapMs;
    const shouldFlush =
      speakerChanged ||
      gapMs > hardGapMs ||
      durationMs > maxParagraphDurationMs ||
      nextWordCount > maxParagraphWords ||
      (paragraphTargetReached && naturalBoundary);

    if (shouldFlush) {
      flush();
      current = {
        speaker,
        startMs,
        endMs,
        wordCount: segmentWordCount,
        text: safeToString(segment.text).trim(),
        sourceSegments: [segment],
      };
      continue;
    }

    current.sourceSegments.push(segment);
    current.endMs = endMs;
    current.wordCount = nextWordCount;
    current.text = joinSegmentTexts(current.sourceSegments);
  }

  flush();
  return mergeSmallReadableSegments(grouped, {
    maxParagraphWords,
    maxParagraphDurationMs,
    hardGapMs,
    minStandaloneWords,
    minStandaloneDurationMs,
  });
}

function mergeSmallReadableSegments(
  grouped: ReadableTranscriptV1["segments"],
  options: {
    maxParagraphWords: number;
    maxParagraphDurationMs: number;
    hardGapMs: number;
    minStandaloneWords: number;
    minStandaloneDurationMs: number;
  },
): ReadableTranscriptV1["segments"] {
  const merged: ReadableTranscriptV1["segments"] = [];
  for (const segment of grouped) {
    const previous = merged.at(-1);
    if (!previous) {
      merged.push(segment);
      continue;
    }

    const previousWordCount = countWordsInText(previous.text);
    const currentWordCount = countWordsInText(segment.text);
    const previousDurationMs = Math.max(0, safeToInt(previous.endMs, 0) - safeToInt(previous.startMs, 0));
    const currentDurationMs = Math.max(0, safeToInt(segment.endMs, 0) - safeToInt(segment.startMs, 0));
    const gapMs = Math.max(0, safeToInt(segment.startMs, 0) - safeToInt(previous.endMs, 0));
    const combinedWordCount = previousWordCount + currentWordCount;
    const combinedDurationMs = Math.max(0, safeToInt(segment.endMs, 0) - safeToInt(previous.startMs, 0));
    const sameSpeaker = previous.speaker === segment.speaker;
    const oneSideTooSmall =
      previousWordCount < options.minStandaloneWords ||
      currentWordCount < options.minStandaloneWords ||
      previousDurationMs < options.minStandaloneDurationMs ||
      currentDurationMs < options.minStandaloneDurationMs;

    if (
      sameSpeaker &&
      gapMs <= options.hardGapMs &&
      combinedWordCount <= options.maxParagraphWords &&
      combinedDurationMs <= options.maxParagraphDurationMs &&
      oneSideTooSmall
    ) {
      previous.endMs = segment.endMs;
      previous.text = joinSegmentTexts([{ text: previous.text }, { text: segment.text }]);
      previous.sourceSegmentIds = [...previous.sourceSegmentIds, ...segment.sourceSegmentIds];
      continue;
    }

    merged.push(segment);
  }
  return merged;
}

export function splitTextIntoWords(
  text: string,
  startMs: number,
  endMs: number,
): Array<{ id: string; text: string; startMs: number; endMs: number }> {
  const parts = typeof text === "string" ? text.trim().split(/\s+/).filter(Boolean) : [];
  if (parts.length === 0) {
    return [];
  }

  const span = Math.max(0, endMs - startMs);
  return parts.map((part, index) => {
    const from = parts.length <= 1 ? startMs : startMs + Math.floor((span * index) / parts.length);
    const to = parts.length <= 1 ? endMs : startMs + Math.floor((span * (index + 1)) / parts.length);
    return {
      id: `w_${index}`,
      text: part,
      startMs: Math.min(Math.max(from, startMs), endMs),
      endMs: Math.max(Math.min(to, endMs), startMs),
    };
  });
}

function normalizeSpeakers(value: unknown): TranscriptSpeaker[] {
  if (!Array.isArray(value)) {
    return [];
  }

  const speakers: TranscriptSpeaker[] = [];
  const seen = new Set<string>();
  for (const item of value) {
    const speaker = asRecord(item);
    const id = safeToString(speaker.id);
    const label = safeToString(speaker.label) || id;
    if (id.trim() === "" || seen.has(id)) {
      continue;
    }
    seen.add(id);
    speakers.push({ id, label });
  }
  return speakers;
}

function stripVariantSuffix(meetingId: string): string {
  return meetingId.replace(/--stt-[A-Za-z0-9._-]+$/, "");
}

function parseTimestampFromDoubledDashParts(
  meetingId: string,
  joiner = "--",
): { title: string; dateLabel: string } | null {
  const parts = meetingId.split(joiner);
  if (parts.length < 2) {
    return null;
  }

  const timeParts = parts.at(-1);
  const timeMatch = /^([0-9]{2}):([0-9]{2})(?:\:([0-9]{2}))?$/.exec(timeParts ?? "");
  if (!timeMatch) {
    return null;
  }
  const hour = timeMatch[1];
  const minute = timeMatch[2];
  const dateCandidate = parts.at(-2);

  const dateMatch = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateCandidate ?? "");
  if (dateMatch) {
    const [, year, month, day] = dateMatch;
    const rawTitle = parts.slice(0, -2).join(joiner);
    if (!rawTitle) {
      return null;
    }
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
    };
  }

  if (parts.length !== 2) {
    return null;
  }

  const legacyDateMatch = /^(.*)-(\d{4})-(\d{2})-(\d{2})$/.exec(parts[0] ?? "");
  if (!legacyDateMatch) {
    return null;
  }
  const [, rawTitle, year, month, day] = legacyDateMatch;
  return {
    title: toTitleCase(rawTitle),
    dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
  };
}

function toTitleCase(text: string): string {
  return text
    .split("-")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function resolveDisplaySourceSegments({
  block,
  sourceSegmentIds,
  segmentById,
  transcriptSegments,
}: {
  block: {
    startMs?: unknown;
    endMs?: unknown;
    speaker?: unknown;
    text?: unknown;
  };
  sourceSegmentIds: string[];
  segmentById: Map<string, TranscriptWordsV1["segments"][number]>;
  transcriptSegments: TranscriptWordsV1["segments"];
}): TranscriptWordsV1["segments"] {
  const resolved = sourceSegmentIds.map((segmentId) => segmentById.get(segmentId)).filter(Boolean);
  if (
    resolved.length > 0 &&
    resolvedSegmentsLookCompatible({
      block,
      resolvedSegments: resolved,
    })
  ) {
    return resolved;
  }
  const blockStartMs = safeToInt(block.startMs, 0);
  const blockEndMs = safeToInt(block.endMs, blockStartMs);
  return transcriptSegments.filter((segment) => {
    const startMs = safeToInt(segment.startMs, 0);
    const endMs = safeToInt(segment.endMs, startMs);
    if (endMs < blockStartMs || startMs > blockEndMs) {
      return false;
    }
    if (typeof block.speaker === "string" && segment.speaker && segment.speaker !== block.speaker) {
      return false;
    }
    return true;
  });
}

function resolvedSegmentsLookCompatible({
  block,
  resolvedSegments,
}: {
  block: {
    startMs?: unknown;
    endMs?: unknown;
    text?: unknown;
  };
  resolvedSegments: TranscriptWordsV1["segments"];
}): boolean {
  const targetWordCount = tokenizeDisplayText(typeof block.text === "string" ? block.text : "").filter(
    (token) => token.kind === "word",
  ).length;
  const resolvedWordCount = resolvedSegments.flatMap((segment) =>
    Array.isArray(segment.words) ? segment.words : [],
  ).length;
  if (targetWordCount > 0 && resolvedWordCount < targetWordCount) {
    return false;
  }

  const blockStartMs = safeToInt(block.startMs, 0);
  const blockEndMs = safeToInt(block.endMs, blockStartMs);
  const blockDurationMs = Math.max(0, blockEndMs - blockStartMs);
  if (blockDurationMs <= 0) {
    return true;
  }
  const resolvedStartMs = Math.min(...resolvedSegments.map((segment) => safeToInt(segment.startMs, blockStartMs)));
  const resolvedEndMs = Math.max(...resolvedSegments.map((segment) => safeToInt(segment.endMs, blockEndMs)));
  const resolvedDurationMs = Math.max(0, resolvedEndMs - resolvedStartMs);
  return resolvedDurationMs >= Math.max(1000, Math.floor(blockDurationMs / 2));
}

function tokenizeDisplayText(text: string): Array<{
  text: string;
  spaceBefore: boolean;
  kind: "word" | "punctuation";
}> {
  const tokenPattern = /[A-Za-z0-9]+(?:[.'’/_-][A-Za-z0-9]+)*|[^\w\s]/gu;
  const tokens: Array<{ text: string; spaceBefore: boolean; kind: "word" | "punctuation" }> = [];
  let match: RegExpExecArray | null;
  let cursor = 0;
  while ((match = tokenPattern.exec(text)) !== null) {
    const prefix = text.slice(cursor, match.index);
    const tokenText = match[0];
    tokens.push({
      text: tokenText,
      spaceBefore: /\s/u.test(prefix),
      kind: isWordToken(tokenText) ? "word" : "punctuation",
    });
    cursor = match.index + tokenText.length;
  }
  return tokens;
}

function normalizeAlignmentToken(token: string): string {
  return String(token ?? "")
    .replaceAll("’", "'")
    .toLowerCase()
    .replace(/^[^\w']+|[^\w']+$/gu, "");
}

function isWordToken(token: string): boolean {
  return normalizeAlignmentToken(token) !== "";
}

function alignReadableTokensToSourceWords(
  sourceWords: Array<{ id?: string; text?: string; startMs?: number; endMs?: number }>,
  readableText: string,
): {
  tokens: Array<{
    text: string;
    spaceBefore: boolean;
    kind: "word" | "punctuation";
    sourceWordIds: string[];
  }>;
} {
  const tokens = tokenizeDisplayText(readableText);
  const targetWordPositions: number[] = [];
  const targetNorms: string[] = [];
  for (let index = 0; index < tokens.length; index += 1) {
    if (tokens[index]?.kind !== "word") {
      continue;
    }
    targetWordPositions.push(index);
    targetNorms.push(normalizeAlignmentToken(tokens[index]?.text ?? ""));
  }
  const sourceNorms = sourceWords.map((word) => normalizeAlignmentToken(word?.text ?? ""));
  const dp = buildLcsTable(sourceNorms, targetNorms);
  const targetIndexToSourceWordIds = reconstructLcsAlignment({
    sourceWords,
    sourceNorms,
    targetWordPositions,
    targetNorms,
    dp,
  });
  return {
    tokens: tokens.map((token, index) => ({
      text: token.text,
      spaceBefore: token.spaceBefore,
      kind: token.kind,
      sourceWordIds: targetIndexToSourceWordIds.get(index) ?? [],
    })),
  };
}

function buildLcsTable(sourceNorms: string[], targetNorms: string[]): number[][] {
  const dp = Array.from({ length: sourceNorms.length + 1 }, () =>
    Array.from({ length: targetNorms.length + 1 }, () => 0),
  );
  for (let sourceIndex = sourceNorms.length - 1; sourceIndex >= 0; sourceIndex -= 1) {
    for (let targetIndex = targetNorms.length - 1; targetIndex >= 0; targetIndex -= 1) {
      if (sourceNorms[sourceIndex] === targetNorms[targetIndex]) {
        dp[sourceIndex]![targetIndex] = dp[sourceIndex + 1]![targetIndex + 1]! + 1;
        continue;
      }
      dp[sourceIndex]![targetIndex] = Math.max(
        dp[sourceIndex + 1]![targetIndex]!,
        dp[sourceIndex]![targetIndex + 1]!,
      );
    }
  }
  return dp;
}

function reconstructLcsAlignment({
  sourceWords,
  sourceNorms,
  targetWordPositions,
  targetNorms,
  dp,
}: {
  sourceWords: Array<{ id?: string }>;
  sourceNorms: string[];
  targetWordPositions: number[];
  targetNorms: string[];
  dp: number[][];
}): Map<number, string[]> {
  const targetIndexToSourceWordIds = new Map<number, string[]>();
  let sourceIndex = 0;
  let targetIndex = 0;
  while (sourceIndex < sourceNorms.length && targetIndex < targetNorms.length) {
    if (sourceNorms[sourceIndex] === targetNorms[targetIndex]) {
      const targetWordIndex = targetWordPositions[targetIndex];
      const sourceWordId = sourceWords[sourceIndex]?.id;
      if (targetWordIndex !== undefined && typeof sourceWordId === "string" && sourceWordId.trim() !== "") {
        targetIndexToSourceWordIds.set(targetWordIndex, [sourceWordId]);
      }
      sourceIndex += 1;
      targetIndex += 1;
      continue;
    }
    if (dp[sourceIndex + 1]![targetIndex]! >= dp[sourceIndex]![targetIndex + 1]!) {
      sourceIndex += 1;
    } else {
      targetIndex += 1;
    }
  }
  return targetIndexToSourceWordIds;
}

function joinSegmentTexts(segments: Array<{ text?: string }>): string {
  let text = "";
  for (const segment of segments) {
    const part = safeToString(segment?.text).trim();
    if (!part) {
      continue;
    }
    if (!text) {
      text = part;
      continue;
    }
    if (/^[,.;:!?)]/.test(part)) {
      text += part;
      continue;
    }
    text += ` ${part}`;
  }
  return text;
}

function countWordsInText(text: string): number {
  return safeToString(text)
    .trim()
    .split(/\s+/)
    .filter(Boolean)
    .length;
}

function endsSentence(text: string): boolean {
  return /[.!?]["')\]]*$/.test(safeToString(text).trim());
}

function safeToInt(value: unknown, fallback: number): number {
  if (typeof value === "number" && Number.isFinite(value)) {
    return Math.trunc(value);
  }
  if (typeof value === "string") {
    const parsed = Number.parseInt(value, 10);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return fallback;
}

function safeToString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function asRecord(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null ? value as Record<string, unknown> : {};
}
