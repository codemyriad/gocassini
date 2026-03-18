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
    const words = splitTextIntoWords(text, startMs, endMs);
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

export function buildReadableTranscriptFromPortable(
  portable: PortableMeetingManifest,
  transcript: TranscriptWordsV1,
): ReadableTranscriptV1 {
  const provided = asRecord(portable.readableTranscript);
  const speakers = normalizeSpeakers(portable.speakers || transcript.speakers || []);
  const validSpeakerIds = new Set(speakers.map((speaker) => speaker.id));

  if (
    provided.version === "transcript.readable.v1" &&
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
        const sourceSegmentIds = Array.isArray(segment.sourceSegmentIds)
          ? segment.sourceSegmentIds.filter((entry): entry is string => typeof entry === "string" && entry.trim() !== "")
          : [];
        const speaker =
          typeof segment.speaker === "string" &&
          segment.speaker.trim() !== "" &&
          validSpeakerIds.has(segment.speaker)
            ? segment.speaker
            : undefined;
        return {
          id:
            typeof segment.id === "string" && segment.id.trim() !== ""
              ? segment.id
              : `readable_${String(index).padStart(6, "0")}`,
          speaker,
          startMs: safeToInt(segment.startMs, 0),
          endMs: safeToInt(segment.endMs, safeToInt(segment.startMs, 0)),
          text: typeof segment.text === "string" ? segment.text : "",
          sourceSegmentIds,
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
      ? readable.segments
      : transcriptSegments.map((segment) => ({
          id: `d_${segment.id}`,
          speaker: segment.speaker,
          speakerLabel: segment.speaker ? speakerLabels.get(segment.speaker) || segment.speaker : "Unknown speaker",
          startMs: segment.startMs,
          endMs: segment.endMs,
          text: segment.text,
          sourceSegmentIds: [segment.id],
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
      const sourceWords = sourceSegments.flatMap((segment) =>
        Array.isArray(segment.words) ? segment.words.filter((word) => word && typeof word === "object") : [],
      );
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
      const tokens = alignment.tokens.map((token) => {
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
      const wordCount = tokens.filter((token) => token.kind === "word").length;
      const timedWordCount = tokens.filter(
        (token) =>
          token.kind === "word" &&
          token.alignment === "source" &&
          Number.isInteger(token.startMs) &&
          Number.isInteger(token.endMs),
      ).length;
      const timedTokens = tokens.filter(
        (token) =>
          token.alignment === "source" &&
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
