import type {
  IndexedSegment,
  IndexedWord,
  ReadableTranscriptSegment,
  ReadableTranscriptV1,
  TranscriptIndex,
  TranscriptSegment,
  TranscriptSpeaker,
  TranscriptWord,
  TranscriptWordsV1,
} from "./types";

function fail(message: string): never {
  throw new Error(message);
}

function asObject(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    fail(`${label} must be an object`);
  }
  return value as Record<string, unknown>;
}

function asString(value: unknown, label: string): string {
  if (typeof value !== "string") {
    fail(`${label} must be a string`);
  }
  return value;
}

function asInteger(value: unknown, label: string): number {
  if (!Number.isInteger(value)) {
    fail(`${label} must be an integer`);
  }
  return value as number;
}

function asOptionalString(value: unknown, label: string): string | undefined {
  if (value === undefined) {
    return undefined;
  }
  return asString(value, label);
}

function asOptionalNumber(value: unknown, label: string): number | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (typeof value !== "number" || Number.isNaN(value)) {
    fail(`${label} must be a number`);
  }
  return value;
}

function validateWord(input: unknown, segmentId: string, wordIndex: number): TranscriptWord {
  const word = asObject(input, `segment ${segmentId} word ${wordIndex}`);
  const startMs = asInteger(word.startMs, `segment ${segmentId} word ${wordIndex} startMs`);
  const endMs = asInteger(word.endMs, `segment ${segmentId} word ${wordIndex} endMs`);
  if (startMs > endMs) {
    fail(`segment ${segmentId} word ${wordIndex} startMs must be <= endMs`);
  }
  const confidence = asOptionalNumber(
    word.confidence,
    `segment ${segmentId} word ${wordIndex} confidence`,
  );
  if (confidence !== undefined && (confidence < 0 || confidence > 1)) {
    fail(`segment ${segmentId} word ${wordIndex} confidence must be between 0 and 1`);
  }

  return {
    id: asOptionalString(word.id, `segment ${segmentId} word ${wordIndex} id`),
    text: asString(word.text, `segment ${segmentId} word ${wordIndex} text`),
    startMs,
    endMs,
    confidence,
  };
}

function validateSegment(
  input: unknown,
  segmentIndex: number,
  speakerIds: Set<string>,
): TranscriptSegment {
  const segment = asObject(input, `segment ${segmentIndex}`);
  const id = asString(segment.id, `segment ${segmentIndex} id`);
  const speaker = asOptionalString(segment.speaker, `segment ${segmentIndex} speaker`);
  if (speaker && !speakerIds.has(speaker)) {
    fail(`segment ${id} references unknown speaker ${speaker}`);
  }
  const startMs = asInteger(segment.startMs, `segment ${id} startMs`);
  const endMs = asInteger(segment.endMs, `segment ${id} endMs`);
  if (startMs > endMs) {
    fail(`segment ${id} startMs must be <= endMs`);
  }
  if (!Array.isArray(segment.words)) {
    fail(`segment ${id} words must be an array`);
  }

  return {
    id,
    speaker,
    startMs,
    endMs,
    text: asString(segment.text, `segment ${id} text`),
    words: segment.words.map((word, wordIndex) => validateWord(word, id, wordIndex)),
  };
}

function validateReadableSegment(
  input: unknown,
  segmentIndex: number,
  speakerIds: Set<string>,
): ReadableTranscriptSegment {
  const segment = asObject(input, `readable segment ${segmentIndex}`);
  const id = asString(segment.id, `readable segment ${segmentIndex} id`);
  const speaker = asOptionalString(segment.speaker, `readable segment ${segmentIndex} speaker`);
  if (speaker && !speakerIds.has(speaker)) {
    fail(`readable segment ${id} references unknown speaker ${speaker}`);
  }
  const startMs = asInteger(segment.startMs, `readable segment ${id} startMs`);
  const endMs = asInteger(segment.endMs, `readable segment ${id} endMs`);
  if (startMs > endMs) {
    fail(`readable segment ${id} startMs must be <= endMs`);
  }
  if (!Array.isArray(segment.sourceSegmentIds)) {
    fail(`readable segment ${id} sourceSegmentIds must be an array`);
  }
  return {
    id,
    speaker,
    startMs,
    endMs,
    text: asString(segment.text, `readable segment ${id} text`),
    sourceSegmentIds: segment.sourceSegmentIds.map((value, sourceIndex) =>
      asString(value, `readable segment ${id} sourceSegmentIds[${sourceIndex}]`),
    ),
  };
}

export function validateTranscriptWordsV1(input: unknown): TranscriptWordsV1 {
  const root = asObject(input, "transcript");
  if (root.version !== "transcript.words.v1") {
    fail(`version must be "transcript.words.v1"`);
  }

  const media = asObject(root.media, "media");
  const speakersRaw = root.speakers;
  if (!Array.isArray(speakersRaw)) {
    fail("speakers must be an array");
  }
  const speakers = speakersRaw.map((speaker, speakerIndex) => {
    const item = asObject(speaker, `speaker ${speakerIndex}`);
    return {
      id: asString(item.id, `speaker ${speakerIndex} id`),
      label: asString(item.label, `speaker ${speakerIndex} label`),
    } satisfies TranscriptSpeaker;
  });

  const speakerIds = new Set<string>();
  for (const speaker of speakers) {
    if (speakerIds.has(speaker.id)) {
      fail(`speaker id ${speaker.id} is duplicated`);
    }
    speakerIds.add(speaker.id);
  }

  const segmentsRaw = root.segments;
  if (!Array.isArray(segmentsRaw)) {
    fail("segments must be an array");
  }

  return {
    version: "transcript.words.v1",
    media: {
      src: asString(media.src, "media.src"),
      durationMs: asInteger(media.durationMs, "media.durationMs"),
      sha256: asOptionalString(media.sha256, "media.sha256"),
    },
    speakers,
    segments: segmentsRaw.map((segment, segmentIndex) =>
      validateSegment(segment, segmentIndex, speakerIds),
    ),
  };
}

export function validateReadableTranscriptV1(input: unknown): ReadableTranscriptV1 {
  const root = asObject(input, "readable transcript");
  if (root.version !== "transcript.readable.v1") {
    fail(`version must be "transcript.readable.v1"`);
  }

  const media = asObject(root.media, "readable media");
  const speakersRaw = root.speakers;
  if (!Array.isArray(speakersRaw)) {
    fail("readable speakers must be an array");
  }
  const speakers = speakersRaw.map((speaker, speakerIndex) => {
    const item = asObject(speaker, `readable speaker ${speakerIndex}`);
    return {
      id: asString(item.id, `readable speaker ${speakerIndex} id`),
      label: asString(item.label, `readable speaker ${speakerIndex} label`),
    } satisfies TranscriptSpeaker;
  });

  const speakerIds = new Set<string>();
  for (const speaker of speakers) {
    if (speakerIds.has(speaker.id)) {
      fail(`readable speaker id ${speaker.id} is duplicated`);
    }
    speakerIds.add(speaker.id);
  }

  const segmentsRaw = root.segments;
  if (!Array.isArray(segmentsRaw)) {
    fail("readable segments must be an array");
  }

  return {
    version: "transcript.readable.v1",
    media: {
      src: asString(media.src, "readable media.src"),
      durationMs: asInteger(media.durationMs, "readable media.durationMs"),
      sha256: asOptionalString(media.sha256, "readable media.sha256"),
    },
    speakers,
    segments: segmentsRaw.map((segment, segmentIndex) =>
      validateReadableSegment(segment, segmentIndex, speakerIds),
    ),
    sourceTranscriptVersion: asOptionalString(
      root.sourceTranscriptVersion,
      "readable sourceTranscriptVersion",
    ),
  };
}

export function buildTranscriptIndex(transcript: TranscriptWordsV1): TranscriptIndex {
  const speakersById = new Map(transcript.speakers.map((speaker) => [speaker.id, speaker]));
  const segments = transcript.segments.map((segment, segmentIndex) => {
    const speakerLabel = segment.speaker
      ? speakersById.get(segment.speaker)?.label ?? segment.speaker
      : "Unknown speaker";
    const indexedWords = segment.words.map((word, wordIndex) => {
      const id = word.id ?? `${segment.id}:w${wordIndex}`;
      return {
        ...word,
        id,
        segmentId: segment.id,
        segmentIndex,
        wordIndex,
        speakerLabel,
      } satisfies IndexedWord;
    });
    return {
      ...segment,
      index: segmentIndex,
      speakerLabel,
      searchText: `${speakerLabel} ${segment.text}`.toLowerCase(),
      words: indexedWords,
    } satisfies IndexedSegment;
  });

  return {
    transcript,
    speakersById,
    segments,
    segmentStartTimes: segments.map((segment) => segment.startMs),
  };
}

function upperBound(values: number[], target: number): number {
  let low = 0;
  let high = values.length;
  while (low < high) {
    const mid = Math.floor((low + high) / 2);
    if (values[mid] <= target) {
      low = mid + 1;
    } else {
      high = mid;
    }
  }
  return low;
}

export function getActiveSegment(index: TranscriptIndex, timeMs: number): IndexedSegment | null {
  if (index.segments.length === 0) {
    return null;
  }

  const startIndex = upperBound(index.segmentStartTimes, timeMs) - 1;
  let winner: IndexedSegment | null = null;
  for (let segmentIndex = startIndex; segmentIndex >= 0; segmentIndex -= 1) {
    const segment = index.segments[segmentIndex];
    if (segment.startMs > timeMs) {
      continue;
    }
    if (segment.endMs < timeMs) {
      if (winner) {
        break;
      }
      if (timeMs - segment.endMs > 30_000) {
        break;
      }
      continue;
    }
    if (!winner || segment.startMs >= winner.startMs) {
      winner = segment;
    }
  }
  return winner;
}

export function getActiveWord(segment: IndexedSegment | null, timeMs: number): IndexedWord | null {
  if (!segment) {
    return null;
  }

  let winner: IndexedWord | null = null;
  for (const word of segment.words) {
    if (word.startMs <= timeMs && timeMs <= word.endMs) {
      return word;
    }
    if (word.startMs <= timeMs) {
      winner = word;
    }
  }
  return winner;
}

export function filterSegmentsBySpeaker(
  index: TranscriptIndex,
  selectedSpeakers: string[],
): IndexedSegment[] {
  if (selectedSpeakers.length === 0) {
    return index.segments;
  }
  const allowed = new Set(selectedSpeakers);
  return index.segments.filter((segment) => !segment.speaker || allowed.has(segment.speaker));
}

export function searchSegments(
  index: TranscriptIndex,
  query: string,
  selectedSpeakers: string[],
): IndexedSegment[] {
  const normalizedQuery = query.trim().toLowerCase();
  const segments = filterSegmentsBySpeaker(index, selectedSpeakers);
  if (!normalizedQuery) {
    return [];
  }
  const tokens = normalizedQuery.split(/\s+/).filter(Boolean);
  return segments.filter((segment) => tokens.every((token) => segment.searchText.includes(token)));
}

export function formatClockTime(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
  }
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

export function parseTimeHash(hash: string): number | null {
  const match = hash.match(/#t=([0-9]+(?:\.[0-9]+)?)(ms|s)?$/);
  if (!match) {
    return null;
  }
  const value = Number(match[1]);
  if (Number.isNaN(value)) {
    return null;
  }
  return match[2] === "ms" ? Math.round(value) : Math.round(value * 1000);
}

export function toCaptionsVtt(transcript: TranscriptWordsV1): string {
  const lines = ["WEBVTT", ""];
  for (const segment of transcript.segments) {
    lines.push(
      `${formatVttTimestamp(segment.startMs)} --> ${formatVttTimestamp(segment.endMs)}`,
      segment.text,
      "",
    );
  }
  return lines.join("\n");
}

function formatVttTimestamp(ms: number): string {
  const totalMilliseconds = Math.max(0, Math.floor(ms));
  const hours = Math.floor(totalMilliseconds / 3_600_000);
  const minutes = Math.floor((totalMilliseconds % 3_600_000) / 60_000);
  const seconds = Math.floor((totalMilliseconds % 60_000) / 1000);
  const milliseconds = totalMilliseconds % 1000;
  return `${String(hours).padStart(2, "0")}:${String(minutes).padStart(2, "0")}:${String(
    seconds,
  ).padStart(2, "0")}.${String(milliseconds).padStart(3, "0")}`;
}
