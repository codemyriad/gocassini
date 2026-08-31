/**
 * Simultaneous speech, and the legacy timing repair that keeps it honest (D-690).
 *
 * Three problems live here, because the later ones cannot be trusted without
 * the earlier ones.
 *
 * 1. OVERLAP IS INVISIBLE. Both viewer load paths hand the transcript pane an
 *    array in producer order and render one full-width paragraph per element,
 *    top to bottom. Two turns that intersect in time therefore read as ordinary
 *    consecutive paragraphs — the reader has no way to tell that two people
 *    were talking at once, and the audio does not match the page.
 *
 * 2. MOST RENDERED OVERLAP IN PUBLISHED MEETINGS IS FAKE. Parakeet stamps a
 *    trailing punctuation token at the NEXT acoustic onset, and the producer
 *    glues that token onto the preceding word — inflating that word's end
 *    across the whole following silence. Measured on one published meeting:
 *    words ending in .?!,;: have median span 560 ms / p95 2959 ms / max 8560 ms
 *    and 62 of them run past 1 s, while every other word has median 240 ms /
 *    p95 639 ms and only 5 pass 1 s; 14 of the 15 longest words in the meeting
 *    end in a period. The consequence is that 23.3 s of that meeting's
 *    cross-speaker word overlap is fabricated against 2.4 s that is genuine —
 *    about 86% of the overlapped time is a decoder artifact, and the pattern
 *    holds across every meeting sampled from the archive.
 *
 *    The producer-side fix only reaches meetings recorded from now on. 197
 *    meetings are already published and will not be reprocessed, so repairing
 *    the timing HERE, at display time, reaches all of them on the next deploy
 *    with no repacking — whether the viewer reads their baked display
 *    transcript or rebuilds one at runtime, both go through this module.
 *    Without the repair, feature 1 would spend most of its time confidently
 *    labelling silence as crosstalk. An artifact whose producer already
 *    bounded word ends by the measured audio says so in its manifest and skips
 *    the repair; see repairTurnFinalWordInflation.
 *
 * 3. THE PRODUCER HIDES THE COMMONEST OVERLAP ENTIRELY. `MergeAndSortSegments`
 *    interleaves every speaker's words by start time and flushes a segment on
 *    each speaker change, so a backchannel inside a continuous turn is not
 *    emitted as two overlapping turns at all — it is emitted as three
 *    consecutive blocks, A / B / A, with A's turn cut in half exactly where B's
 *    words begin. Measured on the overlap-and-pause fixture, whose ground truth
 *    is known: Cara speaks continuously 23.0–30.1 s while Ana says "Perfect." at
 *    26.5, and the pipeline emitted `cara 23.48–26.68`, `ana 26.56–27.24`,
 *    `cara 26.68–29.36`. Rendered naively that reads as "Cara finished, Ana
 *    spoke, Cara started again" when in fact Cara never stopped — and Cara's
 *    second paragraph opens mid-clause, on "link in the channel", because the
 *    cut fell inside her sentence. Span intersection alone understates these
 *    badly (0.42 s, 0.12/0.56 s and 0.23 s measured against 0.63 s, 0.77 s and
 *    0.38 s of ground truth), so the A/B/A sandwich is detected STRUCTURALLY
 *    rather than by intersecting spans.
 *
 * The repair is TIMING ONLY. It never changes displayed text, word order, or
 * the words themselves; it clips one word's end back to a defensible bound so
 * that overlap detection, the audio-sync highlight and word seek targets all
 * agree with what a listener actually hears.
 *
 * Pure module: no Svelte, no DOM, no I/O.
 */

import { containsPlaybackTime } from "./timing";

// ─────────────────────────────────────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────────────────────────────────────

/**
 * Intersections shorter than this are not reported, so a boundary touch between
 * two adjacent turns never draws an affordance.
 *
 * The starting point was 250 ms, mirroring the floor the readable-block splitter
 * used for "a fragment of speech worth keeping" (portable.ts), with its other
 * constant — 400 ms for "this counts as an interruption" — rejected because the
 * fixture's one genuine floor-change interruption is only 379 ms of ground-truth
 * simultaneity. Measurement then pushed it lower still: because the producer
 * flushes a segment at every speaker change, the measurable intersection is
 * systematically far smaller than the real simultaneity. The three genuine
 * overlaps in the ground-truth fixture measure 0.42 s, 0.12/0.56 s and 0.23 s
 * against 0.63/0.77/0.38 s of ground truth; at 250 ms the floor-change
 * interruption vanishes entirely.
 *
 * 150 ms is where it lands: above word-boundary estimation jitter, comfortably
 * above a producer boundary touch (which is exactly 0 ms — consecutive blocks
 * are cut at a shared word boundary, so they cannot graze each other by
 * accident), and below every genuine case measured. The fabricated overlaps
 * this would otherwise admit are removed upstream by the timing repair, not by
 * this threshold.
 */
export const MIN_CREDIBLE_OVERLAP_MS = 150;

/**
 * A turn-final punctuated word is never allowed to run longer than
 * `max(MAX_TURN_FINAL_WORD_MS, TURN_FINAL_WORD_MEDIAN_FACTOR × speaker median)`.
 *
 * The absolute floor exists so a speaker with an unusually clipped median can
 * still hold a genuinely drawn-out last word without it being cut. It sits at
 * 1 s because in the measured meeting only 5 non-punctuated words in the entire
 * meeting reached 1 s (p95 was 639 ms), so 1 s is already deep in the tail of
 * real speech.
 */
export const MAX_TURN_FINAL_WORD_MS = 1000;

/**
 * The multiple of a speaker's own median word duration a turn-final word may
 * reach before it is treated as inflated. 4× is deliberately generous: genuine
 * sentence-final lengthening is roughly 1.5–2× in conversational speech, so 4×
 * clips the decoder artifact and leaves real emphasis alone. Against the
 * measured 240–280 ms medians it lands at ~1.0–1.1 s: just past the p95 of
 * ordinary words, far below the 2959 ms p95 of punctuated ones.
 */
export const TURN_FINAL_WORD_MEDIAN_FACTOR = 4;

/**
 * A speaker's own median is trusted only once we hold this many timed words for
 * them; below it we fall back to the meeting-wide median, then to
 * FALLBACK_MEDIAN_WORD_MS. A one-word turn must not define its own bound.
 */
export const MIN_MEDIAN_SAMPLE_WORDS = 8;

/**
 * Stand-in median when neither the speaker nor the meeting has enough timed
 * words. 250 ms is the measured median of ordinary (non-turn-final) words; at
 * this value the bound collapses to MAX_TURN_FINAL_WORD_MS, the conservative
 * outcome.
 */
export const FALLBACK_MEDIAN_WORD_MS = 250;

/**
 * How far apart the two halves of a split turn may sit before we stop calling
 * the speaker continuous across the block between them.
 *
 * The producer cuts A's turn at the exact word boundary where B starts, so a
 * genuinely uninterrupted A has a seam of 0 ms — measured, to the millisecond,
 * on the fixture: `ana 1.02–5.20 / ben 4.78–5.26 / ana 5.20–8.24` and
 * `cara 23.48–26.68 / ana 26.56–27.24 / cara 26.68–29.36`. 400 ms absorbs
 * alignment noise and a breath while staying far below any real turn exchange:
 * for A to resume within 400 ms of stopping, B must have been talking OVER A
 * rather than answering A. This is the load-bearing gate — ordinary dialogue
 * forms A/B/A constantly and must keep rendering as three separate turns.
 */
export const INTERJECTION_MAX_SEAM_GAP_MS = 400;

/**
 * How long the interjecting block may run and still be treated as something
 * said inside somebody else's turn rather than a turn of its own.
 *
 * The fixture's backchannels are 0.48 s ("Right.") and 0.68 s ("Perfect."). At
 * conversational rate 2 s is around six or seven words — past that, B is making
 * a contribution, and calling it an interjection would demote a real turn.
 * A long B that genuinely talks over A is still reported, as ordinary
 * simultaneous speech with a measured duration.
 */
export const INTERJECTION_MAX_MIDDLE_MS = 2000;

/**
 * How much silence either side of the interjection tolerates before the shape
 * stops looking like a backchannel.
 *
 * In the fixture both sides are NEGATIVE — B starts before A's first half ends
 * and A's second half starts before B ends (−0.42/−0.06 s and −0.12/−0.56 s) —
 * because B genuinely spoke over A. 500 ms of slack on each side covers
 * alignment noise and a backchannel that lands in a breath, and rules out the
 * exchange shape where A finishes, a beat passes, and B replies.
 */
export const INTERJECTION_MAX_SIDE_GAP_MS = 500;

/**
 * How much of a turn's own audible time must fall inside one peer's audible
 * time before the UI is allowed to say the WHOLE turn happened during that
 * peer's.
 *
 * Paragraph containment is not the same claim: the readable writer glues a
 * speaker's turns into paragraphs that reach across other speakers, so a block
 * can sit entirely inside another block's extent while only 200 ms of its words
 * overlap. Measured across nine archived meetings, paragraph extents intersect
 * for 94.2 s against 15.7 s of word intersection — so containment judged on
 * extents would put "this whole turn falls inside X's turn" on turns that
 * mostly did not.
 *
 * 90% rather than 100% because the two edges are estimates: a backchannel's
 * first and last words routinely poke a few tens of milliseconds past the
 * surrounding speech, and on a 0.5 s "Right." that is already 10%.
 */
export const CONTAINED_TURN_MIN_COVERAGE = 0.9;

/**
 * Silence a block keeps its playback highlight across, so the ring does not
 * blink off in the gaps BETWEEN a paragraph's words.
 *
 * Highlight membership is judged on the same audible spans as the overlap
 * analysis — a paragraph that has stopped sounding must not keep the ring while
 * the other speaker talks — but words inside one continuous paragraph do not
 * abut. Measured over the 9437 positive gaps between consecutive timed tokens
 * inside a block across nine archived meetings: median 160 ms, p90 960 ms,
 * p99 4.3 s. Unbridged, the ring would blink several times a second.
 *
 * 400 ms is the same bound the seam gate uses and for the same reason: below it
 * a gap is word-boundary noise or a breath, above it the speaker really has
 * stopped — and 78% of those measured gaps fall under it, while the ones that
 * do not are the multi-second silences the other speaker talks into, which is
 * precisely where the ring must go out. It is also bounded damage: a bridged
 * gap can add at most 400 ms of highlight, the same order as
 * MIN_CREDIBLE_OVERLAP_MS, the smallest simultaneity this module is willing to
 * report at all.
 */
export const HIGHLIGHT_BRIDGE_MS = 400;

/**
 * The punctuation Parakeet emits as its own token and the producer glues onto
 * the preceding word.
 *
 * Terminal punctuation is matched BEFORE any closing quote or bracket, because
 * the cleanup pass adds them: without that, `Yeah."` escapes the repair
 * entirely. The class covers the Unicode sentence terminators as well as the
 * ASCII ones, so a non-English meeting is repaired too.
 */
const TURN_FINAL_PUNCTUATION = /[.?!,;:\u2026\u3002\uFF0E\uFF01\uFF1F\uFF1B\uFF1A\uFF0C\u3001\u0964\u0965\u061B\u061F][)\]}"'\u2019\u201D\u00BB\u203A\u300D\u300F\u3009\u300B]*$/u;

/** Anything carrying a letter or a digit is a word rather than punctuation. */
const WORD_LIKE = /[\p{L}\p{N}]/u;

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

/**
 * One timed thing inside a block: a display token, or a canonical word.
 *
 * The last three fields are what a DISPLAY TOKEN carries and a canonical word
 * does not, and they are what decides whether the span counts as ACOUSTIC
 * evidence at all: `id` names a canonical word, `sourceWordIds` names the
 * canonical words a token was aligned to, and `alignment` says how the token
 * came by its times. All optional, because the canonical-word pool and a plain
 * fixture carry only some of them — but where they are present they are
 * load-bearing. See boundTokensBySourceWords and isSyntheticTiming.
 */
export interface OverlapTimedSpan {
  readonly text: string;
  readonly startMs?: number;
  readonly endMs?: number;
  /** Canonical word id — carried by the canonical-word pool. */
  readonly id?: string;
  /** Canonical words this display token was aligned to. */
  readonly sourceWordIds?: readonly string[];
  /** How a display token got its times: `source`, `interpolated` or `none`. */
  readonly alignment?: string;
}

/**
 * The display-segment projection this module works on. Structural on purpose:
 * MeetingView's DisplaySegment, a DisplayTranscriptV1 block and a plain test
 * fixture all satisfy it, and every function preserves whatever extra fields
 * the caller's own type carries.
 */
export interface OverlapBlock {
  readonly id: string;
  readonly speaker?: string;
  readonly speakerLabel?: string;
  readonly startMs: number;
  readonly endMs: number;
  readonly tokens?: readonly OverlapTimedSpan[];
  readonly words?: readonly OverlapTimedSpan[];
}

/** One other block that was audible at the same time as this one. */
export interface OverlapPeer {
  readonly id: string;
  readonly speakerLabel: string;
  readonly overlapMs: number;
}

/** The turn this block landed inside, when the producer split that turn in two. */
export interface InterjectionContext {
  readonly speakerLabel: string;
  /** The first half of the interrupted turn. */
  readonly beforeId: string;
  /** The half that resumes after this block. */
  readonly afterId: string;
}

/** The block that interrupted the turn this block resumes. */
export interface ResumptionContext {
  readonly speakerLabel: string;
  readonly blockId: string;
}

/** What the renderer needs to describe one block's simultaneity. */
export interface BlockOverlap {
  readonly id: string;
  /** Union of every credible intersection, so two peers over the same second count once. */
  readonly overlapMs: number;
  /** Overlapped ms contributed by peers ALREADY speaking when this turn began. */
  readonly overlapMsBefore: number;
  /** Overlapped ms contributed by peers who started speaking DURING this turn. */
  readonly overlapMsAfter: number;
  /** Peers, largest overlap first. */
  readonly peers: readonly OverlapPeer[];
  /** Set when this whole turn sits inside one peer's span — the backchannel case. */
  readonly containedIn?: string;
  /** Set when this block sits between the two halves of one continuous turn. */
  readonly interrupts?: InterjectionContext;
  /** Set when this block is the second half of a turn something landed inside. */
  readonly resumes?: ResumptionContext;
}

/** Copy for one affordance. */
export interface OverlapDescription {
  /** Compact badge text, e.g. `0.6 s during Ana Duarte`. Carries no icon. */
  readonly badge: string;
  /** Full sentence for `title` and assistive technology. */
  readonly detail: string;
}

// ─────────────────────────────────────────────────────────────────────────────
// Reading order
// ─────────────────────────────────────────────────────────────────────────────

/**
 * Blocks in the order a reader should meet them.
 *
 * Never assume the producer sorted them: it appends wordless segments after the
 * timed ones, so an untimed aside can land at the very end of the page hundreds
 * of turns away from when it was said. The sort is stable, so blocks starting at
 * the same millisecond keep producer order.
 */
export function sortBlocksInReadingOrder<B extends { readonly startMs: number }>(
  blocks: readonly B[],
): B[] {
  return [...blocks].sort((left, right) => left.startMs - right.startMs);
}

// ─────────────────────────────────────────────────────────────────────────────
// Legacy timing repair
// ─────────────────────────────────────────────────────────────────────────────

/**
 * Derive EFFECTIVE display spans by clipping the inflated end off each
 * sentence-final punctuated word.
 *
 * Returns a new array. Nothing is mutated: blocks needing no repair are
 * returned by identity, repaired blocks are shallow copies, and a clipped span
 * is a copy with ONLY `endMs` lowered. `startMs` is never touched — which is
 * what makes this low risk, because a seek target is a word START and so never
 * moves. Text, word order and the words themselves are untouched; the canonical
 * artifact the viewer loaded is left exactly as it was.
 *
 * The rule, in full:
 *   - a candidate is a word-like span together with the punctuation spans
 *     immediately following it (display tokenisation splits `everything.` into
 *     a word token and a `.` token, canonical ASR words keep them joined — this
 *     covers both);
 *   - the candidate's text must end in terminal punctuation, closing quotes and
 *     brackets allowed after it;
 *   - the candidate's span must exceed `max(MAX_TURN_FINAL_WORD_MS,
 *     TURN_FINAL_WORD_MEDIAN_FACTOR × that speaker's reference median)`;
 *   - if so, its end is pulled back to `start + that bound`.
 *
 * A word that ends in punctuation but runs for an ordinary length is left
 * exactly as it is, which is what preserves genuine simultaneity: a backchannel
 * landing inside another turn does not stop being an overlap because it happens
 * to end in a full stop.
 *
 * WHEN IT MUST NOT RUN. The repair exists because the PRODUCER used to let a
 * word's end run past its own audio. Now that the producer bounds word ends by
 * the measured signal, an artifact built by it carries
 * `provenance.wordTimings.endsBoundedByAudio: true`, and its long words are
 * MEASURED rather than fabricated — one such word runs 1.44 s against a 240 ms
 * median because the speaker really held it. Clipping that back to 1 s would
 * undo the production fix in the viewer, so `endsBoundedByAudio` skips the
 * repair entirely. Every one of the 197 already-published meetings lacks the
 * marker and keeps the repair, which is exactly what it was written for.
 *
 * WHY NOT ONLY THE LAST WORD OF EACH BLOCK. The artifact is created at the end
 * of an ASR TURN, and the readable writer glues many ASR turns into one display
 * paragraph, so most inflated words sit in the MIDDLE of a block. Measured over
 * nine archived meetings: 501 words ending in `.?!,;:` run past 1 s against 105
 * of every other kind (median 560 ms vs 240 ms), yet only about ten per meeting
 * are block-final. Restricting the repair to block-final words removed 12% of
 * the fabricated cross-speaker word overlap in that archive where the full rule
 * removes essentially all of it. The restriction is safe to drop because the
 * budget, not the position, is what identifies the artifact: an interior word
 * only reaches 4× its speaker's median when the decoder stretched it across a
 * silence.
 */
export function repairTurnFinalWordInflation<B extends OverlapBlock>(
  blocks: readonly B[],
  options: { readonly endsBoundedByAudio?: boolean } = {},
): B[] {
  if (blocks.length === 0) {
    return [];
  }
  if (options.endsBoundedByAudio) {
    return [...blocks];
  }
  const tokenBudgets = budgetsBySpeaker(blocks, (block) => block.tokens);
  const wordBudgets = budgetsBySpeaker(blocks, (block) => block.words);

  return blocks.map((block) => {
    const key = speakerKey(block);
    const clippedTokens = clipInflatedSpans(block.tokens, tokenBudgets.get(key));
    const words = clipInflatedSpans(block.words, wordBudgets.get(key));
    // A clip a canonical word took has to REACH the display tokens derived from
    // it. Clipping the two pools independently does not do that, because the
    // repair fires on text and cleanup is exactly what removes the punctuation
    // it fires on — so the token keeps the end the word just lost, and the
    // union in audibleIntervalsOf hands the fabricated span straight back.
    const tokens =
      boundTokensBySourceWords(clippedTokens ?? block.tokens, words ?? block.words) ??
      clippedTokens;
    if (!tokens && !words) {
      return block;
    }

    // A block's envelope is defined by its timed spans, so when the last of
    // them shrinks the block shrinks with it — otherwise the paragraph would
    // still claim silence it no longer contains, and overlap detection would
    // keep reading the fabricated span off the envelope instead of the word.
    // "Its timed spans" means BOTH pools: the two describe the same speech
    // through different lenses, and a block whose display tokens stop early
    // because cleanup rewrote its tail still sounds for as long as its canonical
    // words say it does.
    const endMs = Math.max(
      block.startMs,
      Math.min(block.endMs, latestTimedEnd(tokens ?? block.tokens, words ?? block.words, block.endMs)),
    );

    // Spreading a generic is not provably assignable back to B for the
    // compiler even though every field keeps its type; the cast is the narrow
    // escape hatch for that.
    return {
      ...block,
      endMs,
      ...(tokens ? { tokens } : {}),
      ...(words ? { words } : {}),
    } as B;
  });
}

/**
 * The latest end left in EITHER pool after the repair, or `fallbackMs` when the
 * block has no timing at all.
 *
 * Taking the later of the two pools is only safe because the tokens have
 * already been bounded by the canonical words they were derived from
 * (boundTokensBySourceWords). Clipping the pools independently is NOT enough:
 * the repair fires on text, and cleanup strips exactly the punctuation that
 * identifies the artifact, so a clipped canonical `Yeah.` routinely sits under
 * an unclipped display token `Yeah` still carrying the fabricated end.
 *
 * Synthetic token spans do not count towards the envelope either — it is a
 * claim about how long the block was audible, and they are not evidence of
 * that. See isSyntheticTiming.
 */
function latestTimedEnd(
  tokens: readonly OverlapTimedSpan[] | undefined,
  words: readonly OverlapTimedSpan[] | undefined,
  fallbackMs: number,
): number {
  let latest = Number.NEGATIVE_INFINITY;
  for (const pool of [tokens, words]) {
    for (const span of pool ?? []) {
      if (hasTiming(span) && !isSyntheticTiming(span)) {
        latest = Math.max(latest, span.endMs as number);
      }
    }
  }
  return Number.isFinite(latest) ? latest : fallbackMs;
}

/**
 * Copy of `spans` with every display token pulled back to the end of the
 * canonical words it was aligned to, or null when none needed it.
 *
 * THE HOLE THIS CLOSES. The repair is a rule about TEXT: a span is a candidate
 * because it ends in terminal punctuation. The LLM cleanup pass is exactly what
 * takes that punctuation off the display token — canonical `Yeah.` is shown as
 * `Yeah` — so the canonical word is recognised and clipped while the token that
 * took its times FROM that word is not recognised at all and keeps the inflated
 * end. Before audibleIntervalsOf unioned the pools that only meant a slightly
 * wider paragraph; with the union it means the fabricated span comes back whole
 * and, with it, the invented cross-speaker overlap the repair exists to delete.
 *
 * The bound is exact rather than heuristic, because a display token's times are
 * not its own: portable.ts gives an aligned token the minimum start and the
 * maximum end of the canonical words it matched. A token may therefore not
 * outlive the words it was derived from, whatever text cleanup gave it. Starts
 * are never touched, as everywhere else in the repair, so no seek target moves.
 *
 * MEASURED, over the 51 baked display transcripts in this repo's export tree:
 * 268 display tokens run past 1 s while the canonical word they were aligned to
 * ends in terminal punctuation and the token itself no longer does — 126.2 s of
 * fabricated tail. Tokens that run past 1 s and DO still carry the punctuation:
 * zero. So on this corpus the token pool never once sees the artifact on its
 * own, and without this bound the union re-admits all of it.
 *
 * Tokens that name no canonical word, or name words this block does not carry,
 * are left exactly as they are: there is nothing here to bound them by.
 */
function boundTokensBySourceWords(
  spans: readonly OverlapTimedSpan[] | undefined,
  words: readonly OverlapTimedSpan[] | undefined,
): OverlapTimedSpan[] | null {
  if (!spans || spans.length === 0 || !words || words.length === 0) {
    return null;
  }
  const endByWordId = new Map<string, number>();
  for (const word of words) {
    if (word.id === undefined || !hasTiming(word)) {
      continue;
    }
    const known = endByWordId.get(word.id);
    endByWordId.set(word.id, known === undefined ? (word.endMs as number) : Math.max(known, word.endMs as number));
  }
  if (endByWordId.size === 0) {
    return null;
  }

  let next: OverlapTimedSpan[] | null = null;
  for (let index = 0; index < spans.length; index += 1) {
    const span = spans[index]!;
    if (!hasTiming(span) || !span.sourceWordIds || span.sourceWordIds.length === 0) {
      continue;
    }
    let boundMs = Number.NEGATIVE_INFINITY;
    for (const wordId of span.sourceWordIds) {
      const endMs = endByWordId.get(wordId);
      if (endMs !== undefined) {
        boundMs = Math.max(boundMs, endMs);
      }
    }
    if (!Number.isFinite(boundMs) || (span.endMs as number) <= boundMs) {
      continue;
    }
    next ??= [...spans];
    next[index] = { ...span, endMs: Math.max(span.startMs as number, boundMs) };
  }
  return next;
}

/**
 * Clipped copy of `spans`, or null when nothing needed clipping.
 *
 * A "candidate" is one word plus the punctuation glued after it; both the
 * word span and the punctuation spans that share its inflated end are pulled
 * back, or the block would keep the long end through whichever one survived.
 */
function clipInflatedSpans<S extends OverlapTimedSpan>(
  spans: readonly S[] | undefined,
  budgetMs: number | undefined,
): S[] | null {
  if (!spans || spans.length === 0 || budgetMs === undefined) {
    return null;
  }
  let next: S[] | null = null;
  for (const candidate of candidateRuns(spans)) {
    if (candidate.endMs - candidate.startMs <= budgetMs) {
      continue;
    }
    const clippedEndMs = candidate.startMs + budgetMs;
    for (const index of candidate.timedIndexes) {
      const span = spans[index]!;
      const startMs = span.startMs as number;
      const endMs = Math.max(startMs, Math.min(span.endMs as number, clippedEndMs));
      if (endMs === span.endMs) {
        continue;
      }
      next ??= [...spans];
      next[index] = { ...span, endMs } as S;
    }
  }
  return next;
}

interface CandidateRun {
  readonly timedIndexes: number[];
  readonly startMs: number;
  readonly endMs: number;
}

/**
 * Every word-plus-trailing-punctuation run whose text ends in terminal
 * punctuation and carries timing. Punctuation spans with no timing of their own
 * still count towards the TEXT of the run, so `everything` (timed) followed by
 * `.` (untimed) is recognised as sentence-final.
 */
function candidateRuns(spans: readonly OverlapTimedSpan[]): CandidateRun[] {
  const runs: CandidateRun[] = [];
  for (let index = 0; index < spans.length; index += 1) {
    if (!WORD_LIKE.test(spans[index]!.text ?? "")) {
      continue;
    }
    let end = index + 1;
    while (end < spans.length && !WORD_LIKE.test(spans[end]!.text ?? "")) {
      end += 1;
    }
    const text = spans
      .slice(index, end)
      .map((span) => span.text ?? "")
      .join("");
    if (!TURN_FINAL_PUNCTUATION.test(text.trimEnd())) {
      index = end - 1;
      continue;
    }
    const timedIndexes: number[] = [];
    let startMs = Number.POSITIVE_INFINITY;
    let endMs = Number.NEGATIVE_INFINITY;
    for (let inner = index; inner < end; inner += 1) {
      if (!hasTiming(spans[inner])) {
        continue;
      }
      timedIndexes.push(inner);
      startMs = Math.min(startMs, spans[inner]!.startMs as number);
      endMs = Math.max(endMs, spans[inner]!.endMs as number);
    }
    if (timedIndexes.length > 0) {
      runs.push({ timedIndexes, startMs, endMs });
    }
    index = end - 1;
  }
  return runs;
}

/**
 * Per-speaker clip budget, computed once per pool (tokens and canonical words
 * are measured separately because a caller may carry one, the other, or both).
 *
 * The reference population deliberately EXCLUDES every word the repair could
 * fire on, so a candidate can never inflate the bound that judges it. Without
 * that guard a speaker whose only contribution is a single inflated "Yeah."
 * has a 2.96 s median, a 11.8 s budget, and is never clipped. Excluded are:
 *   - every span the repair could fire on, i.e. every word followed by terminal
 *     punctuation, wherever in the block it sits;
 *   - pure punctuation spans, which duplicate their source word's span rather
 *     than being words in their own right;
 *   - synthetic (interpolated) token spans, which measure the gap cleanup left
 *     rather than how long this speaker's words run;
 *   - non-positive durations.
 *
 * A speaker with fewer than MIN_MEDIAN_SAMPLE_WORDS reference words falls back
 * to the meeting-wide median of the same population, and a meeting with too few
 * falls back to FALLBACK_MEDIAN_WORD_MS — at which point the budget is just the
 * MAX_TURN_FINAL_WORD_MS floor, the conservative outcome.
 */
function budgetsBySpeaker<B extends OverlapBlock>(
  blocks: readonly B[],
  pool: (block: B) => readonly OverlapTimedSpan[] | undefined,
): Map<string, number> {
  const durationsBySpeaker = new Map<string, number[]>();
  const allDurations: number[] = [];
  for (const block of blocks) {
    const spans = pool(block);
    if (!spans) {
      continue;
    }
    const key = speakerKey(block);
    let durations = durationsBySpeaker.get(key);
    if (!durations) {
      durations = [];
      durationsBySpeaker.set(key, durations);
    }
    const excluded = new Set<number>();
    for (const candidate of candidateRuns(spans)) {
      for (const index of candidate.timedIndexes) {
        excluded.add(index);
      }
    }
    for (let index = 0; index < spans.length; index += 1) {
      const span = spans[index]!;
      if (
        excluded.has(index) ||
        !hasTiming(span) ||
        isSyntheticTiming(span) ||
        !WORD_LIKE.test(span.text ?? "")
      ) {
        continue;
      }
      const durationMs = (span.endMs as number) - (span.startMs as number);
      if (durationMs <= 0) {
        continue;
      }
      durations.push(durationMs);
      allDurations.push(durationMs);
    }
  }

  const meetingMedian =
    allDurations.length >= MIN_MEDIAN_SAMPLE_WORDS ? median(allDurations) : FALLBACK_MEDIAN_WORD_MS;
  const budgets = new Map<string, number>();
  for (const [key, durations] of durationsBySpeaker) {
    const speakerMedian =
      durations.length >= MIN_MEDIAN_SAMPLE_WORDS ? median(durations) : meetingMedian;
    budgets.set(key, Math.max(MAX_TURN_FINAL_WORD_MS, TURN_FINAL_WORD_MEDIAN_FACTOR * speakerMedian));
  }
  return budgets;
}

function median(values: readonly number[]): number {
  const sorted = [...values].sort((left, right) => left - right);
  const middle = Math.floor(sorted.length / 2);
  if (sorted.length % 2 === 1) {
    return sorted[middle]!;
  }
  return (sorted[middle - 1]! + sorted[middle]!) / 2;
}

// ─────────────────────────────────────────────────────────────────────────────
// Overlap detection
// ─────────────────────────────────────────────────────────────────────────────

/** A stretch of tape one block was audible for. */
export interface Interval {
  startMs: number;
  endMs: number;
}

interface OverlapAccumulator {
  peers: Map<string, { speakerLabel: string; overlapMs: number }>;
  before: Interval[];
  after: Interval[];
  containedIn?: { id: string; peerDurationMs: number };
  interrupts?: InterjectionContext;
  resumes?: ResumptionContext;
}

/**
 * Credible concurrent peers per block, keyed by block id. Blocks with nothing to
 * report are absent from the map.
 *
 * Two blocks by the SAME speaker are never treated as overlapping each other.
 * One person is not simultaneous with themselves; when their own segments
 * intersect it is a producer segmentation artifact, not news for the reader.
 *
 * Overlap is measured between WORDS, not between block spans. This is not a
 * refinement, it is the difference between a true and a false answer: the
 * producer's readable writer groups a speaker's paragraphs across the other
 * speaker's turns, so on published meetings two paragraphs routinely span the
 * same stretch of tape while their words strictly alternate. Measured on one
 * archived meeting, an Ivan paragraph [965.00–983.32] and a Chris paragraph
 * [969.24–1004.60] intersect for 14.08 s of block span while their words never
 * once sound together — Ivan speaks 965.2–968.0, Chris 969.2–970.4, Ivan
 * 972.9–982.8, Chris 988.3 onwards. Reporting block-span intersection would
 * have put a "14 s of simultaneous speech" badge on an ordinary conversation.
 * Block spans are used only as a cheap prefilter for which pairs to compare.
 *
 * Sweep-line over a start-sorted COPY — O(n log n) for the prefilter plus a
 * linear merge per surviving candidate pair, of which a real meeting has a
 * handful. The input is never assumed to be sorted: the producer appends
 * wordless segments last.
 */
export function analyzeOverlap(
  blocks: readonly OverlapBlock[],
  options: { readonly minOverlapMs?: number } = {},
): Map<string, BlockOverlap> {
  const minOverlapMs = options.minOverlapMs ?? MIN_CREDIBLE_OVERLAP_MS;
  const ordered = blocks
    .filter(
      (block) =>
        Number.isFinite(block.startMs) && Number.isFinite(block.endMs) && block.endMs > block.startMs,
    )
    .sort((left, right) => left.startMs - right.startMs || left.endMs - right.endMs);

  const accumulators = new Map<string, OverlapAccumulator>();
  const accumulatorFor = (id: string): OverlapAccumulator => {
    let accumulator = accumulators.get(id);
    if (!accumulator) {
      accumulator = { peers: new Map(), before: [], after: [] };
      accumulators.set(id, accumulator);
    }
    return accumulator;
  };
  const spansCache = new Map<string, Interval[]>();
  const spansOf = (block: OverlapBlock): Interval[] => {
    let spans = spansCache.get(block.id);
    if (!spans) {
      spans = audibleIntervalsOf(block);
      spansCache.set(block.id, spans);
    }
    return spans;
  };

  const active: OverlapBlock[] = [];
  for (const current of ordered) {
    // Anything that ended at or before this start can never overlap this block
    // or a later one, since starts only increase from here.
    for (let index = active.length - 1; index >= 0; index -= 1) {
      if (active[index]!.endMs <= current.startMs) {
        active.splice(index, 1);
      }
    }

    for (const earlier of active) {
      if (isSameSpeaker(earlier, current)) {
        continue;
      }
      const intersections = intersectIntervals(spansOf(earlier), spansOf(current));
      const overlapMs = totalMs(intersections);
      if (overlapMs < minOverlapMs) {
        continue;
      }

      const currentAccumulator = accumulatorFor(current.id);
      const earlierAccumulator = accumulatorFor(earlier.id);
      addPeer(currentAccumulator, earlier, overlapMs);
      addPeer(earlierAccumulator, current, overlapMs);
      currentAccumulator.before.push(...intersections);
      earlierAccumulator.after.push(...intersections);

      // Containment is a claim about AUDIBLE time, not about extents. A block
      // can sit wholly inside another block's extent while only 200 ms of its
      // words overlap, and the badge for that says the whole turn happened
      // during the other speaker's — so it is decided from how much of each
      // turn's own audible time the intersection covers.
      noteContainment(currentAccumulator, earlier, overlapMs, totalMs(spansOf(current)));
      noteContainment(earlierAccumulator, current, overlapMs, totalMs(spansOf(earlier)));
    }

    active.push(current);
  }

  markSplitTurnInterjections(ordered, accumulatorFor);

  const result = new Map<string, BlockOverlap>();
  for (const [id, accumulator] of accumulators) {
    const peers = [...accumulator.peers.entries()]
      .map(([peerId, peer]) => ({
        id: peerId,
        speakerLabel: peer.speakerLabel,
        overlapMs: peer.overlapMs,
      }))
      .sort((left, right) => right.overlapMs - left.overlapMs || left.id.localeCompare(right.id));
    result.set(id, {
      id,
      overlapMs: unionMs([...accumulator.before, ...accumulator.after]),
      overlapMsBefore: unionMs(accumulator.before),
      overlapMsAfter: unionMs(accumulator.after),
      peers,
      ...(accumulator.containedIn ? { containedIn: accumulator.containedIn.id } : {}),
      ...(accumulator.interrupts ? { interrupts: accumulator.interrupts } : {}),
      ...(accumulator.resumes ? { resumes: accumulator.resumes } : {}),
    });
  }
  return result;
}

/**
 * When this block was actually audible, as disjoint ascending intervals: the
 * UNION of every timed span it carries, in either pool, with the block extent
 * as the last resort — a wordless segment still occupies its stretch of tape
 * and can still be genuinely simultaneous with someone.
 *
 * UNION, NOT A CHOICE BETWEEN THE POOLS. Display tokens are what the reader sees
 * highlighted and what the seek targets hang off; canonical words are the ASR's
 * own record of when this speaker made a noise. Neither is complete on its own,
 * and every rule that picked one of them lost real audible time:
 *
 *   - picking the LONGER array dropped a fully rewritten cleaned block — a
 *     complete set of word tokens with `alignment: "none"` and no times at all
 *     (portable.test.ts, "leaves fully rewritten cleaned blocks untimed at the
 *     word level") — onto its paragraph envelope, the very thing this module
 *     exists to stop trusting;
 *   - picking the first pool that carries ANY timed span lost the mixed case,
 *     which is not exotic: 30 of the 421 display blocks in the nine portable
 *     meetings in this repo's export tree carry some timed word tokens and some
 *     untimed ones, because cleanup rewrote part of the passage. One token
 *     spanning 0–500 ms over canonical words spanning 0–2500 ms collapsed the
 *     block to [0, 500] — the ring went dark for the two seconds the speaker
 *     was still talking through, and any simultaneity in them went unreported.
 *
 * ONLY ACOUSTIC EVIDENCE GETS IN, which the union does not give for free. Two
 * things that look like timing are not evidence of anybody making a noise:
 *
 *   - a display token that OUTLIVES the canonical word it was aligned to. An
 *     aligned token takes the minimum start and maximum end of the words it
 *     matched (portable.ts), so it should not be able to — but the legacy repair
 *     recognises an inflated word by its terminal punctuation, and cleanup is
 *     exactly what strips that punctuation off the display token. The clipped
 *     canonical `Yeah.` then sits under an unclipped token `Yeah` still carrying
 *     the fabricated end, and this union would restore it. Those tokens are
 *     pulled back to their source words by the repair before this runs; see
 *     boundTokensBySourceWords;
 *
 *   - an INTERPOLATED token, whose times nobody measured: portable.ts spreads a
 *     rewritten run evenly between its two aligned neighbours, so the span it
 *     covers is BY CONSTRUCTION the stretch where this block's own aligned
 *     tokens were not sounding — the block's silence, plus whatever anybody else
 *     said in it. Excluded here (isSyntheticTiming carries the measurement).
 *
 *     Excluding is the same answer as the other available fix, bounding those
 *     spans by the block's canonical audible intervals — identical, not merely
 *     close. Those intervals are ALREADY part of this union, so intersecting a
 *     synthetic span with them can only return time the union covers anyway:
 *     the bounded variant contributes exactly nothing the canonical words have
 *     not already contributed. Excluding reaches that set without the pass.
 *     Interpolated tokens keep their times for rendering and seeking; they just
 *     get no vote on whether the speaker was audible.
 */
export function audibleIntervalsOf(block: OverlapBlock): Interval[] {
  const spans: Interval[] = [];
  collectAudibleSpans(block.tokens, spans);
  collectAudibleSpans(block.words, spans);
  if (spans.length === 0) {
    return [{ startMs: block.startMs, endMs: block.endMs }];
  }
  return mergeIntervals(spans);
}

function collectAudibleSpans(
  pool: readonly OverlapTimedSpan[] | undefined,
  into: Interval[],
): void {
  for (const span of pool ?? []) {
    if (isAudibleSpan(span)) {
      into.push({ startMs: span.startMs as number, endMs: span.endMs as number });
    }
  }
}

function isAudibleSpan(span: OverlapTimedSpan): boolean {
  return (
    hasTiming(span) &&
    !isSyntheticTiming(span) &&
    (span.endMs as number) > (span.startMs as number)
  );
}

/**
 * Times a display token was GIVEN rather than measured.
 *
 * `interpolated` marks the tokens portable.ts spread evenly across the gap
 * between two aligned neighbours when cleanup rewrote the words in between. It
 * is a rendering and seeking convenience, not an observation: the run is laid
 * out uniformly over the whole anchor-to-anchor interval whatever is actually
 * in it, and that interval is precisely where this block's own aligned tokens
 * are silent.
 *
 * Measured over the 354 interpolated tokens in the 51 baked display transcripts
 * in this repo's export tree — 251.7 s of synthetic span in total:
 *   - 76.4 s falls on canonical words the block itself carries, so the union
 *     already covers it with real evidence behind it;
 *   - 86.3 s falls where the ASR recorded no word by anybody — silence;
 *   - 9.7 s falls squarely inside ANOTHER speaker's canonical words, which is
 *     fabricated crosstalk of exactly the kind this module exists to delete;
 *   - the rest falls on the same speaker's words in other blocks, which those
 *     blocks already report.
 * Median span 400 ms, p90 1.5 s, longest 15.2 s — so the short ones disappear
 * under HIGHLIGHT_BRIDGE_MS anyway, and the long ones are the ones that must
 * not be trusted.
 */
function isSyntheticTiming(span: OverlapTimedSpan): boolean {
  return span.alignment === "interpolated";
}

/**
 * Every block that is SOUNDING at `timeMs`, earliest start first.
 *
 * This is what the playback highlight and the follow-scroll anchor run on, and
 * it has to answer the same question the overlap analysis answers or the page
 * contradicts itself. Judged on paragraph extents it did not: the readable
 * writer glues a speaker's turns into paragraphs that reach across other
 * speakers, so across nine archived meetings extents intersect for 94.2 s
 * against 15.7 s of word intersection — the viewer ringed both speakers, and
 * follow-scroll anchored on the earlier paragraph, through stretches where
 * their words strictly alternate and only one of them was talking.
 *
 * A block with no timed spans at all still has its extent to be highlighted on
 * (audibleIntervalsOf falls back to it), so a wordless aside is not silently
 * unhighlightable. Gaps shorter than HIGHLIGHT_BRIDGE_MS are bridged so the
 * ring does not blink between a paragraph's words.
 */
export function getSoundingBlocks<B extends OverlapBlock>(
  blocks: readonly B[],
  timeMs: number,
): B[] {
  return blocks
    .filter((block) =>
      bridgeGaps(audibleIntervalsOf(block), HIGHLIGHT_BRIDGE_MS).some((interval) =>
        containsPlaybackTime(interval, timeMs),
      ),
    )
    .sort((left, right) => left.startMs - right.startMs);
}

/** Ascending disjoint intervals with sub-`toleranceMs` silence closed up. */
function bridgeGaps(intervals: readonly Interval[], toleranceMs: number): Interval[] {
  const bridged: Interval[] = [];
  for (const interval of intervals) {
    const previous = bridged.at(-1);
    if (previous && interval.startMs - previous.endMs <= toleranceMs) {
      previous.endMs = Math.max(previous.endMs, interval.endMs);
      continue;
    }
    bridged.push({ ...interval });
  }
  return bridged;
}

function mergeIntervals(intervals: readonly Interval[]): Interval[] {
  const sorted = [...intervals].sort((left, right) => left.startMs - right.startMs);
  const merged: Interval[] = [];
  for (const interval of sorted) {
    const previous = merged.at(-1);
    if (!previous || interval.startMs > previous.endMs) {
      merged.push({ ...interval });
      continue;
    }
    previous.endMs = Math.max(previous.endMs, interval.endMs);
  }
  return merged;
}

/** Two-pointer intersection of two ascending, disjoint interval lists. */
function intersectIntervals(left: readonly Interval[], right: readonly Interval[]): Interval[] {
  const result: Interval[] = [];
  let leftIndex = 0;
  let rightIndex = 0;
  while (leftIndex < left.length && rightIndex < right.length) {
    const a = left[leftIndex]!;
    const b = right[rightIndex]!;
    const startMs = Math.max(a.startMs, b.startMs);
    const endMs = Math.min(a.endMs, b.endMs);
    if (endMs > startMs) {
      result.push({ startMs, endMs });
    }
    if (a.endMs <= b.endMs) {
      leftIndex += 1;
    } else {
      rightIndex += 1;
    }
  }
  return result;
}

function totalMs(intervals: readonly Interval[]): number {
  return intervals.reduce((total, interval) => total + (interval.endMs - interval.startMs), 0);
}

/**
 * The A/B/A sandwich: one turn cut in half by the producer around the block that
 * landed inside it.
 *
 * `MergeAndSortSegments` interleaves every speaker's words by start time and
 * flushes a segment on each speaker change, so a backchannel inside a continuous
 * turn is not emitted as two overlapping turns at all — it is emitted as three
 * consecutive blocks with A's turn cut exactly where B's words begin. Measured
 * on the overlap-and-pause fixture through current main, A's halves are
 * CONTIGUOUS to the millisecond (`ana 1.02–5.20`, `ben 4.78–5.26`,
 * `ana 5.20–8.24`; `cara 23.48–26.68`, `ana 26.56–27.24`, `cara 26.68–29.36`),
 * which makes the seam a far stronger signal than the span intersection those
 * same turns produce (0.42 s and 0.12 s against 0.63 s and 0.77 s of ground
 * truth).
 *
 * Restricted to exactly ONE intervening block. That is the shape the producer
 * emits, and it is the shape we can defend: with two or more different speakers
 * between A's halves, "A never stopped" becomes a guess, and an affordance that
 * over-claims an interruption is worse than none.
 *
 * The seam test runs on repaired spans, so a turn whose first half merely ENDED
 * in an inflated punctuated word — leaving real silence for B to speak into — is
 * correctly not reported: the repair opens the seam past the tolerance.
 */
function markSplitTurnInterjections(
  ordered: readonly OverlapBlock[],
  accumulatorFor: (id: string) => OverlapAccumulator,
): void {
  for (let index = 1; index + 1 < ordered.length; index += 1) {
    const before = ordered[index - 1]!;
    const middle = ordered[index]!;
    const after = ordered[index + 1]!;
    if (!isSameSpeaker(before, after) || isSameSpeaker(before, middle)) {
      continue;
    }
    // Adjacency alone proves nothing: ordinary dialogue produces A/B/A all the
    // time and must keep rendering as three separate turns. All four tests have
    // to pass before we claim A never stopped talking.
    // The seam is bounded on BOTH sides. An upper bound alone let an
    // arbitrarily NEGATIVE seam through, which is not a narrow seam at all but
    // a massive self-overlap: A [0, 10000] / B [2000, 2500] / A [3000, 4000]
    // has A's second half starting 7 s before its first half ends, and calling
    // that one continuous turn interrupted by B is simply false. A genuinely
    // uninterrupted A measures 0 ms to the millisecond, so the tolerance is
    // symmetric around it.
    if (Math.abs(after.startMs - before.endMs) > INTERJECTION_MAX_SEAM_GAP_MS) {
      continue;
    }
    if (middle.endMs - middle.startMs > INTERJECTION_MAX_MIDDLE_MS) {
      continue;
    }
    if (middle.startMs - before.endMs > INTERJECTION_MAX_SIDE_GAP_MS) {
      continue;
    }
    if (after.startMs - middle.endMs > INTERJECTION_MAX_SIDE_GAP_MS) {
      continue;
    }
    const speakerLabel = before.speakerLabel?.trim() || "Unknown speaker";
    accumulatorFor(middle.id).interrupts = {
      speakerLabel,
      beforeId: before.id,
      afterId: after.id,
    };
    accumulatorFor(after.id).resumes = {
      speakerLabel: middle.speakerLabel?.trim() || "Unknown speaker",
      blockId: middle.id,
    };
  }
}

/**
 * Record `peer` as the turn this block happened during, when the intersection
 * covers essentially all of this block's audible time.
 *
 * Ties are broken towards the peer that was audible longest, so a backchannel
 * that lands inside two stacked turns names the one a reader would recognise as
 * "the turn" rather than whichever the sweep reached first.
 */
function noteContainment(
  accumulator: OverlapAccumulator,
  peer: OverlapBlock,
  overlapMs: number,
  ownAudibleMs: number,
): void {
  if (ownAudibleMs <= 0 || overlapMs < ownAudibleMs * CONTAINED_TURN_MIN_COVERAGE) {
    return;
  }
  const peerDurationMs = peer.endMs - peer.startMs;
  if (!accumulator.containedIn || peerDurationMs > accumulator.containedIn.peerDurationMs) {
    accumulator.containedIn = { id: peer.id, peerDurationMs };
  }
}

function addPeer(accumulator: OverlapAccumulator, peer: OverlapBlock, overlapMs: number): void {
  const existing = accumulator.peers.get(peer.id);
  if (existing) {
    existing.overlapMs += overlapMs;
    return;
  }
  accumulator.peers.set(peer.id, {
    speakerLabel: peer.speakerLabel?.trim() || "Unknown speaker",
    overlapMs,
  });
}

/** Total covered milliseconds, counting time shared by several peers once. */
function unionMs(intervals: readonly Interval[]): number {
  return totalMs(mergeIntervals(intervals));
}

function isSameSpeaker(left: OverlapBlock, right: OverlapBlock): boolean {
  if (left.speaker && right.speaker) {
    return left.speaker === right.speaker;
  }
  return speakerKey(left) === speakerKey(right);
}

function speakerKey(block: OverlapBlock): string {
  return block.speaker || block.speakerLabel?.trim() || "";
}

function hasTiming(span: OverlapTimedSpan | undefined): boolean {
  return (
    span !== undefined &&
    Number.isFinite(span.startMs) &&
    Number.isFinite(span.endMs) &&
    (span.endMs as number) >= (span.startMs as number)
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Copy
// ─────────────────────────────────────────────────────────────────────────────

/**
 * Badge and screen-reader copy for one block's simultaneity, or null when there
 * is nothing credible to say.
 *
 * Three shapes, because the three situations read differently:
 *   - a block sitting between the two halves of one continuous turn is a remark
 *     made DURING that turn, and the measured intersection understates it, so
 *     the duration is only quoted when it clears the reporting threshold;
 *   - a turn wholly inside somebody else's span is a backchannel — "during";
 *   - anything else is a partial overlap, usually the floor changing hands —
 *     "with".
 */
export function describeOverlap(entry: BlockOverlap | undefined | null): OverlapDescription | null {
  if (!entry) {
    return null;
  }
  const peerList = entry.peers
    .map((peer) => `${peer.speakerLabel} (${formatOverlapDuration(peer.overlapMs)})`)
    .join(", ");

  if (entry.interrupts) {
    const label = entry.interrupts.speakerLabel;
    const measured = entry.overlapMs > 0 ? `${formatOverlapDuration(entry.overlapMs)} ` : "";
    return {
      badge: `${measured}during ${label}`,
      detail: `Simultaneous speech: this lands inside ${label}'s turn, which continues after it${
        entry.overlapMs > 0 ? ` — ${formatOverlapDuration(entry.overlapMs)} of overlapping audio` : ""
      }.`,
    };
  }

  if (entry.peers.length === 0 || entry.overlapMs <= 0) {
    return null;
  }
  const primary = entry.peers[0]!;
  const duration = formatOverlapDuration(entry.overlapMs);

  if (entry.containedIn === primary.id && entry.peers.length === 1) {
    return {
      badge: `${duration} during ${primary.speakerLabel}`,
      detail: `Simultaneous speech: this whole turn falls inside ${primary.speakerLabel}'s turn — ${duration} of overlapping audio.`,
    };
  }
  const remaining = entry.peers.length - 1;
  return {
    badge: `${duration} with ${primary.speakerLabel}${remaining > 0 ? ` +${remaining}` : ""}`,
    detail: `Simultaneous speech: ${duration} of this turn overlaps ${peerList}. ${
      entry.peers.length === 1 ? "Both voices are" : "Those voices are"
    } on the recording at once.`,
  };
}

/**
 * Copy for the second half of a turn something landed inside, or null.
 *
 * This is the marker that stops A/B/A reading as three separate turns: without
 * it, A's second half looks like a fresh turn taken back after B finished, when
 * in fact A never stopped talking.
 */
export function describeResumption(
  entry: BlockOverlap | undefined | null,
): OverlapDescription | null {
  if (!entry?.resumes) {
    return null;
  }
  return {
    badge: `continues past ${entry.resumes.speakerLabel}`,
    detail: `Same turn continued: this is the rest of the turn ${entry.resumes.speakerLabel} spoke into — the speaker did not stop.`,
  };
}

/**
 * Reading rows: consecutive blocks, with an interrupted turn's three parts
 * gathered into one row so the renderer can put a visual parent around them.
 *
 * The three canonical blocks are kept intact, in order, with their own ids,
 * their own timestamps and their own seek anchors — nothing is concatenated and
 * nothing is merged. Only the presentation changes: A-first-half, then the
 * interjection nested inside, then A's continuation, so the page stops claiming
 * that one continuous utterance was three separate turns.
 *
 * Expects blocks already in reading order (see sortBlocksInReadingOrder).
 */
export function groupInterruptedTurns<B extends OverlapBlock>(
  blocks: readonly B[],
  analysis: ReadonlyMap<string, BlockOverlap>,
): Array<InterruptedTurnRow<B>> {
  const rows: Array<InterruptedTurnRow<B>> = [];
  for (let index = 0; index < blocks.length; index += 1) {
    const first = blocks[index]!;
    const middle = blocks[index + 1];
    const last = blocks[index + 2];
    const interjection = middle ? analysis.get(middle.id)?.interrupts : undefined;
    if (
      middle &&
      last &&
      interjection &&
      interjection.beforeId === first.id &&
      interjection.afterId === last.id
    ) {
      rows.push({
        key: first.id,
        interrupted: true,
        speakerLabel: interjection.speakerLabel,
        interjectionId: middle.id,
        interjectorLabel: middle.speakerLabel?.trim() || "Unknown speaker",
        members: [first, middle, last],
      });
      index += 2;
      continue;
    }
    rows.push({
      key: first.id,
      interrupted: false,
      speakerLabel: first.speakerLabel?.trim() || "Unknown speaker",
      members: [first],
    });
  }
  return rows;
}

export interface InterruptedTurnRow<B> {
  readonly key: string;
  readonly interrupted: boolean;
  /** The interrupted speaker when `interrupted`, else this block's own speaker. */
  readonly speakerLabel: string;
  readonly interjectionId?: string;
  readonly interjectorLabel?: string;
  readonly members: readonly B[];
}

/** `0.6 s` below ten seconds, `12 s` above it — a decimal on a long overlap is noise. */
export function formatOverlapDuration(ms: number): string {
  const seconds = Math.max(0, ms) / 1000;
  return seconds < 10 ? `${seconds.toFixed(1)} s` : `${Math.round(seconds)} s`;
}
