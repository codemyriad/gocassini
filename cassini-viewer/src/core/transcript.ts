import type {
  DisplayTranscriptBlock,
  DisplayTranscriptToken,
  DisplayTranscriptV1,
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
import { getActiveTimedRange } from "./timing";

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

function asOptionalBoolean(value: unknown, label: string): boolean | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value !== "boolean") fail(`${label} must be a boolean`);
  return value as boolean;
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

// For attributionGapDb: `null` must read as "not measured", matching how
// asOptionalBoolean treats `lowConfidenceSpeaker: null`. A producer that
// always emits the key writes null when the attribution stage did not run,
// and a missing measurement must degrade to "no evidence" — never to a
// transcript that refuses to load.
function asOptionalNumberOrNull(value: unknown, label: string): number | undefined {
  if (value === null) {
    return undefined;
  }
  return asOptionalNumber(value, label);
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

  const attributionGapDb = asOptionalNumberOrNull(
    word.attributionGapDb,
    `segment ${segmentId} word ${wordIndex} attributionGapDb`,
  );
  const lowConfidenceSpeaker = asOptionalBoolean(
    word.lowConfidenceSpeaker,
    `segment ${segmentId} word ${wordIndex} lowConfidenceSpeaker`,
  );

  return {
    id: asOptionalString(word.id, `segment ${segmentId} word ${wordIndex} id`),
    text: asString(word.text, `segment ${segmentId} word ${wordIndex} text`),
    startMs,
    endMs,
    confidence,
    attributionGapDb,
    lowConfidenceSpeaker,
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

function validateDisplayToken(input: unknown, blockId: string, tokenIndex: number): DisplayTranscriptToken {
  const token = asObject(input, `display block ${blockId} token ${tokenIndex}`);
  const kind = asString(token.kind, `display block ${blockId} token ${tokenIndex} kind`);
  if (kind !== "word" && kind !== "punctuation") {
    fail(`display block ${blockId} token ${tokenIndex} kind must be "word" or "punctuation"`);
  }
  const startMs = token.startMs === undefined
    ? undefined
    : asInteger(token.startMs, `display block ${blockId} token ${tokenIndex} startMs`);
  const endMs = token.endMs === undefined
    ? undefined
    : asInteger(token.endMs, `display block ${blockId} token ${tokenIndex} endMs`);
  if ((startMs === undefined) !== (endMs === undefined)) {
    fail(`display block ${blockId} token ${tokenIndex} must provide both startMs and endMs`);
  }
  if (startMs !== undefined && endMs !== undefined && startMs > endMs) {
    fail(`display block ${blockId} token ${tokenIndex} startMs must be <= endMs`);
  }
  const alignment = asOptionalString(
    token.alignment,
    `display block ${blockId} token ${tokenIndex} alignment`,
  );
  if (
    alignment !== undefined &&
    alignment !== "source" &&
    alignment !== "interpolated" &&
    alignment !== "none"
  ) {
    fail(
      `display block ${blockId} token ${tokenIndex} alignment must be "source", "interpolated", or "none"`,
    );
  }
  if (!Array.isArray(token.sourceWordIds)) {
    fail(`display block ${blockId} token ${tokenIndex} sourceWordIds must be an array`);
  }
  return {
    text: asString(token.text, `display block ${blockId} token ${tokenIndex} text`),
    spaceBefore: Boolean(token.spaceBefore),
    kind,
    sourceWordIds: token.sourceWordIds.map((value, sourceIndex) =>
      asString(value, `display block ${blockId} token ${tokenIndex} sourceWordIds[${sourceIndex}]`),
    ),
    startMs,
    endMs,
    alignment,
  };
}

function validateDisplayBlock(
  input: unknown,
  blockIndex: number,
  speakerIds: Set<string>,
): DisplayTranscriptBlock {
  const block = asObject(input, `display block ${blockIndex}`);
  const id = asString(block.id, `display block ${blockIndex} id`);
  const speaker = asOptionalString(block.speaker, `display block ${blockIndex} speaker`);
  if (speaker && !speakerIds.has(speaker)) {
    fail(`display block ${id} references unknown speaker ${speaker}`);
  }
  const startMs = asInteger(block.startMs, `display block ${id} startMs`);
  const endMs = asInteger(block.endMs, `display block ${id} endMs`);
  if (startMs > endMs) {
    fail(`display block ${id} startMs must be <= endMs`);
  }
  if (!Array.isArray(block.sourceSegmentIds)) {
    fail(`display block ${id} sourceSegmentIds must be an array`);
  }
  if (!Array.isArray(block.tokens)) {
    fail(`display block ${id} tokens must be an array`);
  }
  const timingCoverage = asOptionalNumber(block.timingCoverage, `display block ${id} timingCoverage`);
  if (timingCoverage !== undefined && (timingCoverage < 0 || timingCoverage > 1)) {
    fail(`display block ${id} timingCoverage must be between 0 and 1`);
  }
  return {
    id,
    speaker,
    speakerLabel: asString(block.speakerLabel, `display block ${id} speakerLabel`),
    startMs,
    endMs,
    text: asString(block.text, `display block ${id} text`),
    sourceSegmentIds: block.sourceSegmentIds.map((value, sourceIndex) =>
      asString(value, `display block ${id} sourceSegmentIds[${sourceIndex}]`),
    ),
    wordCount: asInteger(block.wordCount, `display block ${id} wordCount`),
    timedWordCount: asInteger(block.timedWordCount, `display block ${id} timedWordCount`),
    timingCoverage: timingCoverage ?? 0,
    tokens: block.tokens.map((token, tokenIndex) => validateDisplayToken(token, id, tokenIndex)),
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

export function validateDisplayTranscriptV1(input: unknown): DisplayTranscriptV1 {
  const root = asObject(input, "display transcript");
  if (root.version !== "transcript.display.v1") {
    fail(`version must be "transcript.display.v1"`);
  }

  const media = asObject(root.media, "display media");
  const speakersRaw = root.speakers;
  if (!Array.isArray(speakersRaw)) {
    fail("display speakers must be an array");
  }
  const speakers = speakersRaw.map((speaker, speakerIndex) => {
    const item = asObject(speaker, `display speaker ${speakerIndex}`);
    return {
      id: asString(item.id, `display speaker ${speakerIndex} id`),
      label: asString(item.label, `display speaker ${speakerIndex} label`),
    } satisfies TranscriptSpeaker;
  });

  const speakerIds = new Set<string>();
  for (const speaker of speakers) {
    if (speakerIds.has(speaker.id)) {
      fail(`display speaker id ${speaker.id} is duplicated`);
    }
    speakerIds.add(speaker.id);
  }

  const blocksRaw = root.blocks;
  if (!Array.isArray(blocksRaw)) {
    fail("display blocks must be an array");
  }

  return {
    version: "transcript.display.v1",
    media: {
      src: asString(media.src, "display media.src"),
      durationMs: asInteger(media.durationMs, "display media.durationMs"),
      sha256: asOptionalString(media.sha256, "display media.sha256"),
    },
    speakers,
    blocks: blocksRaw.map((block, blockIndex) => validateDisplayBlock(block, blockIndex, speakerIds)),
    sourceTranscriptVersion: asOptionalString(
      root.sourceTranscriptVersion,
      "display sourceTranscriptVersion",
    ),
    sourceReadableTranscriptVersion: asOptionalString(
      root.sourceReadableTranscriptVersion,
      "display sourceReadableTranscriptVersion",
    ),
  };
}

/**
 * The label both this index and the display builders fall back to when a
 * segment or block carries no speaker at all.
 *
 * It names nobody, so isCompatibleWithBlock must not read a disagreement into
 * it: "Unknown speaker" against "Alice" is a missing answer, not a different
 * person.
 */
export const UNKNOWN_SPEAKER_LABEL = "Unknown speaker";

export function buildTranscriptIndex(transcript: TranscriptWordsV1): TranscriptIndex {
  const speakersById = new Map(transcript.speakers.map((speaker) => [speaker.id, speaker]));
  const segments = transcript.segments.map((segment, segmentIndex) => {
    const speakerLabel = segment.speaker
      ? speakersById.get(segment.speaker)?.label ?? segment.speaker
      : UNKNOWN_SPEAKER_LABEL;
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

interface IndexLookups {
  wordsById: Map<string, IndexedWord>;
  segmentsById: Map<string, IndexedSegment>;
}

// Lazy per-index lookup maps for canonicalWordsForBlock. Keyed weakly on the
// index so repeated per-block calls during one render stay O(block size) after
// the first, and a replaced index (transcript switch) drops its maps.
const indexLookupsCache = new WeakMap<TranscriptIndex, IndexLookups>();

function lookupsFor(index: TranscriptIndex): IndexLookups {
  let lookups = indexLookupsCache.get(index);
  if (!lookups) {
    const wordsById = new Map<string, IndexedWord>();
    const segmentsById = new Map<string, IndexedSegment>();
    for (const segment of index.segments) {
      segmentsById.set(segment.id, segment);
      for (const word of segment.words) {
        if (!wordsById.has(word.id)) {
          wordsById.set(word.id, word);
        }
      }
    }
    lookups = { wordsById, segmentsById };
    indexLookupsCache.set(index, lookups);
  }
  return lookups;
}

/**
 * Every canonical timed word a display block should be judged on — the crosstalk
 * badge, per-word attribution styling, and (through MeetingView) the audible
 * spans src/core/overlap.ts measures simultaneity and playback highlighting on.
 *
 * The two mappings are UNIONED, not tried in turn, because neither reaches all
 * of a cleaned block's words on its own:
 *
 *   - Display tokens name the exact canonical words they were aligned to via
 *     `sourceWordIds`, and those ids are minted by the same code that builds the
 *     canonical index — so they resolve on both the JSON-directory path and the
 *     portable (.opus) path. But they only name the words that SURVIVED cleanup:
 *     a token the cleanup rewrote carries `sourceWordIds: []`, and a canonical
 *     word cleanup deleted outright is named by no token at all.
 *   - The block-level `sourceSegmentIds` gather every canonical word of the
 *     segments the block was built from — the mapping the JSON-directory path
 *     has always used, and the only one that sees the words no token kept. The
 *     producer emits one readable block per canonical segment and names it
 *     (`seg_%06d`, format.go), so on that path the ids resolve and no two blocks
 *     share one. They reach nothing on the portable path: 662 of the 692 display
 *     blocks in the nine portable meetings in this repo's export tree leave the
 *     field empty, and the 30 that do not are legacy split halves whose only id
 *     is the literal string "undefined". That is why judging via
 *     sourceSegmentIds alone silently found no words for portable meetings.
 *
 * Returning whichever came back non-empty first meant a partially rewritten
 * block — one token still aligned, the rest of the passage rewritten — was
 * judged on that one token's word and nothing else, and every canonical word
 * covering the remainder was discarded. That is a claim about which stretches of
 * tape the block was sounding on, so dropping them put the playback ring out and
 * hid real simultaneity across the rest of the passage.
 *
 * Returned in canonical order (segment, then word), so a caller reading them as
 * a sequence gets the order they were spoken in rather than the order cleanup
 * happened to reference them in.
 *
 * The two mappings can only disagree about MEMBERSHIP, never about timing: both
 * hand back the same IndexedWord objects out of the same index.
 *
 * RESOLVABLE IS NOT THE SAME AS COMPATIBLE, which is why every reference is
 * checked against the block before it is taken. The portable path re-projects
 * `transcript.items[]` into one SYNTHETIC segment per word and names them
 * `seg_%06d` (portable.ts) — the very shape the Go producer names its real,
 * many-word segments (format.go). So a display block carrying a producer id
 * from the pack it was cleaned against resolves, on the portable path, against
 * whichever single word happens to sit at that ordinal: any speaker, anywhere
 * in the meeting. These words are what the block's AUDIBLE SPANS are measured
 * from (src/core/overlap.ts), so one stale id is enough to put another person's
 * speech into this block's evidence and invent cross-speaker overlap out of it.
 *
 * The two checks are in isCompatibleWithBlock. Neither costs anything on real
 * data: across the 51 baked display transcripts in this repo's export tree,
 * 153,474 token-resolved words and 153,686 resolved source-segment references
 * produced ZERO speaker mismatches, and no resolved word fell outside its
 * block's extent by more than 2.69 s.
 */
export function canonicalWordsForBlock(
  index: TranscriptIndex,
  block: {
    speaker?: string;
    speakerLabel?: string;
    startMs: number;
    endMs: number;
    sourceSegmentIds: readonly string[];
    tokens: ReadonlyArray<{ sourceWordIds: readonly string[] }>;
  },
): IndexedWord[] {
  return canonicalEvidenceForBlock(index, block).words;
}

/** What one display block may be judged on: its words, and its voting tokens. */
export interface BlockCanonicalEvidence<T> {
  /** Canonical words compatible with this block, in canonical order. */
  readonly words: IndexedWord[];
  /**
   * The block's own tokens, except that any whose canonical references were
   * resolved and REJECTED carry `sourceWordsRejected: true`. Returned by
   * identity when nothing was rejected.
   */
  readonly tokens: readonly T[];
}

/**
 * canonicalWordsForBlock's answer, plus the verdict on each display token.
 *
 * REJECTING A WORD IS NOT ENOUGH ON ITS OWN. A display token carries its own
 * `startMs`/`endMs`, and src/core/overlap.ts unions the token pool with the
 * word pool — so throwing a stale canonical word out of `words` while handing
 * the token that named it back untouched leaves the token free to fabricate the
 * very overlap the rejection was meant to prevent, and to hold the playback
 * highlight through it. It is the same shape as a token outliving the canonical
 * word the repair clipped (boundTokensBySourceWords): a token still standing
 * after the thing that justified it has gone.
 *
 * A token's references land in one of three states, and only the middle one
 * loses the token its acoustic vote:
 *
 *   1. NAMES NOTHING (`sourceWordIds: []`) — a rewritten or unaligned token.
 *      Untouched: there was never a canonical claim behind it to withdraw, and
 *      whatever times it has came from somewhere else entirely.
 *   2. NAMES WORDS THAT RESOLVE, AND EVERY ONE WAS REJECTED — the stale-id
 *      case. The index HAS those words; they belong to another speaker or to
 *      another part of the tape. The token's times were derived from words this
 *      block does not own, so it gets no vote. If even one named word survives,
 *      the token keeps its vote and boundTokensBySourceWords holds it to the
 *      survivors.
 *   3. NAMES WORDS THAT DO NOT RESOLVE AT ALL — untouched. This is the case we
 *      genuinely cannot judge, and it is not rare: 27,837 of the 181,311
 *      token-to-word references in the 51 baked display transcripts in this
 *      repo's export tree resolve against nothing (15%), because a display
 *      transcript baked against one transcript can be read beside another. On
 *      the runtime portable path the opposite holds — all 26,739 references
 *      across the nine portable meetings resolve. Silencing an unresolvable
 *      reference would therefore blank a seventh of the aligned tokens in the
 *      archive on no evidence, so the error is deliberately taken in the other
 *      direction: absent proof of a bad reference, the token keeps its vote.
 *
 * States 2 and 3 are told apart by whether the id is IN THE INDEX at all —
 * `wordsById.get(wordId)` returning a word means the reference resolved and the
 * compatibility check had something real to judge; returning nothing means
 * there was nothing to judge.
 */
export function canonicalEvidenceForBlock<T extends { sourceWordIds: readonly string[] }>(
  index: TranscriptIndex,
  block: {
    speaker?: string;
    speakerLabel?: string;
    startMs: number;
    endMs: number;
    sourceSegmentIds: readonly string[];
    tokens: readonly T[];
  },
): BlockCanonicalEvidence<T> {
  const { wordsById, segmentsById } = lookupsFor(index);

  const seen = new Set<string>();
  const words: IndexedWord[] = [];
  const take = (word: IndexedWord | undefined): void => {
    if (!word || seen.has(word.id)) {
      return;
    }
    seen.add(word.id);
    words.push(word);
  };

  const rejectedTokenIndexes = new Set<number>();
  for (let index_ = 0; index_ < block.tokens.length; index_ += 1) {
    let keptCount = 0;
    let rejectedCount = 0;
    for (const wordId of block.tokens[index_]!.sourceWordIds) {
      const word = wordsById.get(wordId);
      if (!word) {
        // State 3: nothing to judge. Neither taken nor held against the token.
        continue;
      }
      if (isCompatibleWithBlock(block, segmentsById.get(word.segmentId), word)) {
        keptCount += 1;
        take(word);
      } else {
        rejectedCount += 1;
      }
    }
    if (keptCount === 0 && rejectedCount > 0) {
      rejectedTokenIndexes.add(index_);
    }
  }

  for (const segmentId of block.sourceSegmentIds) {
    const segment = segmentsById.get(segmentId);
    if (!segment || !isCompatibleWithBlock(block, segment, segment)) {
      continue;
    }
    for (const word of segment.words) {
      take(word);
    }
  }

  words.sort(
    (left, right) =>
      left.segmentIndex - right.segmentIndex || left.wordIndex - right.wordIndex,
  );

  if (rejectedTokenIndexes.size === 0) {
    return { words, tokens: block.tokens };
  }
  // Copied, never mutated: the loaded artifact keeps its own tokens, and the
  // marked ones keep every field they had — text, spacing, times, alignment —
  // so they still render and still seek. They only stop being evidence.
  return {
    words,
    tokens: block.tokens.map((token, index_) =>
      rejectedTokenIndexes.has(index_) ? ({ ...token, sourceWordsRejected: true } as T) : token,
    ),
  };
}

/**
 * How far outside a display block's own extent a canonical reference may sit
 * before it is read as a stale id rather than as this block's own speech.
 *
 * A block's extent is derived from its TIMED tokens, so when cleanup rewrites a
 * block's head or tail the extent is pulled INSIDE the source segment that
 * produced it, and a legitimate reference lands outside. Measured across the
 * 153,686 resolved source-segment references in this repo's export tree, that
 * happens 155 times and the worst case is 2.69 s (median 0 — the reference
 * merely touches the boundary); every one of the 153,474 token-resolved words
 * touches at worst. 5 s leaves headroom over the worst case measured, because
 * dropping a legitimate reference costs real audible time, while admitting a
 * word 5 s away costs at most a slightly wider highlight — the speaker check,
 * not this one, is what stops another person's speech getting in.
 */
export const MAX_CANONICAL_REFERENCE_DRIFT_MS = 5000;

/**
 * Whether a canonical reference belongs to this block at all.
 *
 * SPEAKER, in two steps, because identity is recorded two different ways and a
 * reference may carry one on each side. Ids first when BOTH sides have one —
 * they are the canonical identity. Otherwise fall back to the labels whenever
 * both are MEANINGFUL, which covers the mixed case: a block naming only
 * "Alice", a segment carrying id `spk_bob` and label "Bob", and no id to
 * compare. Comparing nothing there let a plainly different person through.
 *
 * A label is meaningful when it is non-empty and is not UNKNOWN_SPEAKER_LABEL:
 * both the index and the display builders mint that placeholder for anything
 * with no speaker, so reading "Unknown speaker" against "Alice" as a
 * disagreement would throw away a block's own words on the strength of a
 * missing field. Only when neither pair is comparable do we accept unjudged.
 *
 * TIME. The reference must sit within MAX_CANONICAL_REFERENCE_DRIFT_MS of the
 * block's extent. Skipped when the block has no usable extent, since there is
 * then nothing to judge against.
 */
function isCompatibleWithBlock(
  block: { speaker?: string; speakerLabel?: string; startMs: number; endMs: number },
  segment: IndexedSegment | undefined,
  span: { startMs: number; endMs: number },
): boolean {
  if (!segment) {
    return false;
  }
  if (block.speaker && segment.speaker) {
    if (block.speaker !== segment.speaker) {
      return false;
    }
  } else {
    const blockLabel = meaningfulSpeakerLabel(block.speakerLabel);
    const segmentLabel = meaningfulSpeakerLabel(segment.speakerLabel);
    if (blockLabel && segmentLabel && blockLabel !== segmentLabel) {
      return false;
    }
  }

  if (
    !Number.isFinite(block.startMs) ||
    !Number.isFinite(block.endMs) ||
    block.endMs <= block.startMs
  ) {
    return true;
  }
  return (
    span.endMs >= block.startMs - MAX_CANONICAL_REFERENCE_DRIFT_MS &&
    span.startMs <= block.endMs + MAX_CANONICAL_REFERENCE_DRIFT_MS
  );
}

/**
 * One display block projected into what the viewer renders AND judges it on.
 *
 * Lives here rather than in the component because the projection is the whole
 * point: the words and the tokens have to come out of the same compatibility
 * pass. Reading `words` from canonicalEvidenceForBlock while taking `tokens`
 * straight off the block hands the audible evidence back through the other
 * pool — src/core/overlap.ts unions them — so the two must be assembled
 * together, where a test can see it happen.
 */
export interface JudgedDisplaySegment {
  id: string;
  speaker?: string;
  speakerLabel: string;
  startMs: number;
  endMs: number;
  text: string;
  tokens: readonly DisplayTranscriptToken[];
  words: IndexedWord[];
  sourceSegmentIds: string[];
}

/** Trailing " audio"/" video" is a device name, not part of a person's name. */
export function normalizeSpeakerLabel(label: string): string {
  return label.replace(/\s+(audio|video)\s*$/i, "").trim() || label;
}

/**
 * Every block of a display transcript, carrying the canonical words it is
 * judged on and the tokens that are still allowed to vote on that judgement.
 *
 * A display transcript is LLM-cleaned prose whose tokens hold no attribution,
 * and portable meetings always build one — so without the canonical words the
 * crosstalk badge could never appear for the common case, only for raw JSON
 * with the exact-word view switched on. The words are not rendered; they are
 * what the segment is judged on, here and in src/core/overlap.ts.
 */
export function judgedDisplaySegments(
  index: TranscriptIndex,
  display: DisplayTranscriptV1,
): JudgedDisplaySegment[] {
  return display.blocks.map((block) => {
    const evidence = canonicalEvidenceForBlock(index, block);
    return {
      id: block.id,
      speaker: block.speaker,
      speakerLabel: normalizeSpeakerLabel(block.speakerLabel),
      startMs: block.startMs,
      endMs: block.endMs,
      text: block.text,
      tokens: evidence.tokens,
      words: evidence.words,
      sourceSegmentIds: [...block.sourceSegmentIds],
    };
  });
}

/** A label that names somebody, or undefined when it is empty or a placeholder. */
function meaningfulSpeakerLabel(label: string | undefined): string | undefined {
  const trimmed = label?.trim();
  if (!trimmed || trimmed === UNKNOWN_SPEAKER_LABEL) {
    return undefined;
  }
  return trimmed;
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
  return getActiveTimedRange(segment.words, timeMs);
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
  // The seek time is the last param in the viewer's hash. It may be preceded by
  // `#` (bare deep-link "#t=12.5") or `&` (combined "#meeting=…&t=12500ms" —
  // hashRouting.buildViewerHash always writes t= last). Anchored at
  // end-of-string either way.
  const match = hash.match(/[#&]t=([0-9]+(?:\.[0-9]+)?)(ms|s)?$/);
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

/**
 * How many of a turn's words the producer flagged as probable crosstalk.
 *
 * Cassini records, per word, how far the loudest other microphone sat above its
 * own noise floor compared with the attributed speaker's. A large gap means
 * somebody else was talking and this participant's track merely picked them up.
 */
export function lowConfidenceWordCount(words: readonly TranscriptWord[]): number {
  return words.reduce((count, word) => (word.lowConfidenceSpeaker ? count + 1 : count), 0);
}

/**
 * A turn built entirely from words the acoustic evidence attributes to somebody
 * else — the shape where a quiet track picks up whoever is actually speaking and
 * the decoder renders it as a short interjection by the wrong person.
 *
 * Requires every word to be flagged, not merely some: a real turn that happens
 * to overlap someone louder should not be written off wholesale.
 */
export function isLikelyCrosstalkTurn(words: readonly TranscriptWord[]): boolean {
  if (words.length === 0) return false;
  return lowConfidenceWordCount(words) === words.length;
}
