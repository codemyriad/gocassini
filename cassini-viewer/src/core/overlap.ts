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
 * 4. THE SAME SEGMENTATION SHREDS TWO PEOPLE WHO TALK AT ONCE. A/B/A is only
 *    the small case. When two speakers genuinely hold the floor together the
 *    producer alternates on every word, and BOTH of their sentences come out as
 *    one- to three-word fragments: measured on the overlap-and-pause fixture,
 *    Cara's sentence 41.0–49.0 s and Ben's competing sentence 43.2–49.0 s were
 *    emitted as 31 alternating segments — "f the" / "final" / "sign" / "off".
 *    One published meeting shows the same at scale: 262 segments, 34% of them
 *    three words or fewer, sentences cut mid-clause. buildTurnModel is the
 *    answer: it puts each speaker's fragments back into the turn they were,
 *    says which short runs were said INSIDE somebody else's turn, and reports
 *    where two turns genuinely collided — as data, so a renderer can present it
 *    however it likes without redoing any of the judgement.
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
 * How much silence there may be in a speaker's OWN speech before we stop calling
 * them continuous across whatever somebody else said in the gap.
 *
 * The producer cuts A's turn at the exact word boundary where B starts, so a
 * genuinely uninterrupted A has a seam of 0 ms — measured, to the millisecond,
 * on the fixture: `ana 1.02–5.20 / ben 4.78–5.26 / ana 5.20–8.24` and
 * `cara 23.48–26.68 / ana 26.56–27.24 / cara 26.68–29.36`. 400 ms absorbs
 * alignment noise and a breath while staying far below any real turn exchange:
 * for A to resume within 400 ms of stopping, B must have been talking OVER A
 * rather than answering A. This is the load-bearing gate — ordinary dialogue
 * forms A/B/A constantly and must keep reading as separate turns.
 *
 * The same number governs the general case, where a whole turn is shredded into
 * a chain of fragments rather than cut once. On the shredded double-talk
 * fixture every gap inside one speaker's own run measures between −1 ms and
 * 241 ms, while the smallest gap across a real floor change in that meeting is
 * 4176 ms — an order of magnitude of clear air either side of 400 ms. The
 * producer's own 1500 ms segment-gap threshold was rejected as the bound here
 * because it answers a different question: how long a silence may be before a
 * MONOLOGUE is worth splitting, with nobody competing. Here somebody else is
 * talking in the gap, and a speaker silent for more than a breath while that
 * happens has yielded the floor. 400 ms is also the conservative direction: the
 * cost of being wrong is a turn that stays split, which is today's behaviour.
 */
export const INTERJECTION_MAX_SEAM_GAP_MS = 400;

/**
 * How much CONTINUOUS SPEECH OF THEIR OWN somebody may have and still be treated
 * as talking inside another person's turn rather than taking one.
 *
 * Measured on the speaker's whole re-joined run with the silences removed, not
 * on whatever the producer happened to emit as one segment. That is the
 * difference between a true and a false answer, not a refinement: across the
 * double-talk stretch Ben's competing sentence arrives as fifteen fragments of
 * 160–560 ms, and judged one at a time every single one looks like a
 * backchannel — his turn would be dismantled and scattered as fifteen asides
 * inside Cara's. As the run it really is, he speaks for 5.2 s.
 *
 * The same bound still admits the genuine backchannels in that meeting: Ben's
 * "Right." sounds for 401 ms and Ana's "Perfect." for 600 ms. So the decision on
 * this fixture is between 600 ms and 5200 ms, and 2 s — around six or seven
 * words at conversational rate, past which somebody is making a contribution
 * rather than acknowledging one — sits in the middle of that gap.
 *
 * It is never the only test. A duration alone cannot tell a short remark from
 * somebody holding the floor in short bursts, so the classification also asks
 * WHERE the run sits: a backchannel lands in ONE gap in the host's speech,
 * while a speaker who has the floor makes the host restart again and again.
 * See bestHostFor.
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
  /**
   * Set when ANY canonical word this token named resolved in the index and was
   * rejected as incompatible with the block (transcript.ts,
   * canonicalEvidenceForBlock). One is enough: a token's times are the min
   * start and max end of every word it matched, so a single rejected word
   * donates a bound that no later clipping can take back. The token still
   * renders and still seeks; it just no longer counts as evidence that its
   * speaker was audible, and its accepted words carry the real interval.
   */
  readonly sourceWordsRejected?: boolean;
}

/**
 * The display-segment projection this module works on. Structural on purpose:
 * MeetingView's DisplaySegment, a DisplayTranscriptV1 block and a plain test
 * fixture all satisfy it, and every function preserves whatever extra fields
 * the caller's own type carries.
 */
export interface OverlapBlock {
  /**
   * A canonical reference this block named resolved in the index and was
   * rejected as belonging to another speaker or another part of the tape
   * (transcript.ts, canonicalEvidenceForBlock). Absent on fixtures and on the
   * projections that have no references to reject.
   *
   * It is the difference between a block that never had timed evidence and one
   * whose evidence was taken away, which audibleIntervalsOf has to know: only
   * the first may fall back to its paragraph extent.
   */
  readonly referencesRejected?: boolean;
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
      if (hasTiming(span) && !isUnattestedSpan(span)) {
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
        isUnattestedSpan(span) ||
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
  const ordered = orderForSweep(blocks);
  const spansOf = audibleSpanCache();

  const accumulators = new Map<string, OverlapAccumulator>();
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

      const currentAccumulator = accumulatorFor(accumulators, current.id);
      const earlierAccumulator = accumulatorFor(accumulators, earlier.id);
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

  markRejoinedTurnInterjections(ordered, accumulators, spansOf);

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
 * The blocks the sweep may compare, earliest first.
 *
 * A block whose extent is not a real forward interval cannot intersect anything
 * and cannot bound anything, so it is dropped here rather than defended against
 * at every use. The tie-break on `endMs` keeps the sweep deterministic.
 */
function orderForSweep<B extends OverlapBlock>(blocks: readonly B[]): B[] {
  return blocks
    .filter(
      (block) =>
        Number.isFinite(block.startMs) && Number.isFinite(block.endMs) && block.endMs > block.startMs,
    )
    .sort((left, right) => left.startMs - right.startMs || left.endMs - right.endMs);
}

/** Memoised `audibleIntervalsOf`, keyed by block id. */
function audibleSpanCache(): (block: OverlapBlock) => Interval[] {
  const cache = new Map<string, Interval[]>();
  return (block) => {
    let spans = cache.get(block.id);
    if (!spans) {
      spans = audibleIntervalsOf(block);
      cache.set(block.id, spans);
    }
    return spans;
  };
}

function accumulatorFor(
  accumulators: Map<string, OverlapAccumulator>,
  id: string,
): OverlapAccumulator {
  let accumulator = accumulators.get(id);
  if (!accumulator) {
    accumulator = { peers: new Map(), before: [], after: [] };
    accumulators.set(id, accumulator);
  }
  return accumulator;
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
 * ONLY ACOUSTIC EVIDENCE GETS IN, which the union does not give for free. Three
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
 *   - a token whose canonical words were REJECTED as belonging to another
 *     speaker or another part of the tape. Its span was only ever the envelope
 *     of those words, so once they go it stands on nothing, and counting it
 *     would let one stale id fabricate through this pool exactly the overlap
 *     the compatibility check refused through the other; see
 *     isRejectedSourceTiming;
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
    // THE EXTENT FALLBACK IS FOR BLOCKS THAT NEVER HAD TIMED EVIDENCE, not for
    // blocks whose evidence was taken away. A wordless aside genuinely occupies
    // its stretch of tape and can genuinely be simultaneous with somebody, so
    // it keeps the extent. A block whose references resolved and were all
    // rejected is a different animal: falling back would hand the whole
    // paragraph back as audible time and recreate exactly the false overlap and
    // false playback ring the rejection was there to prevent — the rejection
    // would buy nothing at all. It has no defensible audible time, so it has
    // none.
    return block.referencesRejected ? [] : [{ startMs: block.startMs, endMs: block.endMs }];
  }
  return mergeIntervals(spans);
}

/**
 * A block nothing in the recording vouches for anywhere: no trustworthy span,
 * and no right to its extent either.
 *
 * Distinct from "quiet" — a wordless aside is not acoustically empty, it is
 * merely untimed, and it keeps its extent.
 */
function isAcousticallyEmpty(spans: readonly Interval[]): boolean {
  return spans.length === 0;
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
    !isUnattestedSpan(span) &&
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
 * Times that came from canonical words this block turned out not to own.
 *
 * A display token's span is not its own measurement — it is the envelope of the
 * canonical words it was aligned to. When every one of those words is thrown
 * out as incompatible with the block (another speaker, or another part of the
 * tape; see transcript.ts, canonicalEvidenceForBlock) the span is left standing
 * on nothing. Counting it would let a single stale id fabricate the exact
 * overlap the compatibility check just refused, through the other pool.
 */
function isRejectedSourceTiming(span: OverlapTimedSpan): boolean {
  return span.sourceWordsRejected === true;
}

/**
 * A span that nothing in the recording vouches for. Neither kind is deleted —
 * both still render, highlight their own token and seek — they simply do not
 * get to say that somebody was making a noise.
 */
function isUnattestedSpan(span: OverlapTimedSpan): boolean {
  return isSyntheticTiming(span) || isRejectedSourceTiming(span);
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
      soundingIntervalsOf(block).some((interval) => containsPlaybackTime(interval, timeMs)),
    )
    .sort((left, right) => left.startMs - right.startMs);
}

/**
 * The intervals a block's ring is actually drawn from: its audible intervals
 * with the sub-`HIGHLIGHT_BRIDGE_MS` silences closed up.
 *
 * Extracted from `getSoundingBlocks`'s filter so that playhead.ts, which
 * precomputes these once per transcript instead of once per animation frame,
 * shares this definition rather than restating it. The bridging tolerance and
 * what counts as audible stay this module's business; a second copy of either
 * would be free to drift from the D-690 rules the tests here pin.
 */
export function soundingIntervalsOf(block: OverlapBlock): Interval[] {
  return bridgeGaps(audibleIntervalsOf(block), HIGHLIGHT_BRIDGE_MS);
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

// ─────────────────────────────────────────────────────────────────────────────
// Turn structure: re-joining what the producer shredded
// ─────────────────────────────────────────────────────────────────────────────

/**
 * One rendered turn: the blocks of ONE speaker that the producer split apart
 * only because somebody else's words landed between them.
 *
 * `MergeAndSortSegments` interleaves every speaker's words by start time and
 * flushes a segment on each speaker change. One person talking alone therefore
 * comes out as one block, but the moment two people hold the floor at once the
 * pipeline alternates, and BOTH of their sentences are shredded into one- to
 * three-word fragments. Measured on the overlap-and-pause fixture, whose ground
 * truth is known: Cara speaks one sentence 41.0–49.0 s and Ben a different,
 * competing sentence 43.2–49.0 s, and the producer emitted 31 alternating
 * fragments — "f the" / "final" / "sign" / "off". A published meeting showed the
 * same shape at scale: 262 segments, 34% of them three words or fewer.
 *
 * A run is therefore the unit the reader actually cares about, and everything
 * downstream — the row, its badge, its nested interjections — is derived from
 * runs rather than from raw blocks.
 */
interface TurnRun<B extends OverlapBlock> {
  readonly speakerKey: string;
  readonly speakerLabel: string;
  readonly blocks: B[];
  /** First and last moment this speaker was AUDIBLE across the whole run. */
  startMs: number;
  endMs: number;
  /**
   * False when the run's block has no defensible audible time (its canonical
   * references resolved and were all rejected). Such a block never joins a run
   * and never nests: "this speaker never stopped" is a claim about sound.
   */
  readonly attested: boolean;
  /** Position of the latest block in the producer's reading order. */
  lastPosition: number;
}

/** The turn one run landed inside, and the two halves of it that it sits between. */
interface NestedTurn<B extends OverlapBlock> {
  readonly host: TurnRun<B>;
  readonly beforeId: string;
  readonly afterId: string;
}

/** First and last audible millisecond of one block, or null when it has none. */
function audibleBoundsOf(spans: readonly Interval[]): { startMs: number; endMs: number } | null {
  const first = spans[0];
  const last = spans.at(-1);
  if (!first || !last || !Number.isFinite(first.startMs) || !Number.isFinite(last.endMs)) {
    return null;
  }
  if (last.endMs <= first.startMs) {
    return null;
  }
  return { startMs: first.startMs, endMs: last.endMs };
}

/**
 * A speaker's blocks re-joined into the turns they were before the producer cut
 * them up, in first-block order.
 *
 * THE RULE, and it is deliberately one rule: take EACH SPEAKER'S OWN blocks in
 * time order and join two of them when the seam between them — measured on their
 * own audible speech, not on their paragraph extents — is at most
 * INTERJECTION_MAX_SEAM_GAP_MS in either direction. Whatever anybody else said
 * in that seam is not consulted, because it is not evidence about whether THIS
 * speaker stopped.
 *
 * Note what that does and a naive pass does not. In the shape this exists for,
 * two adjacent blocks in the sorted list are never the same speaker — the
 * producer flushes on every speaker change, so the fragments strictly alternate
 * — and "merge neighbouring same-speaker blocks" therefore does nothing at all.
 * The grouping has to be per speaker, ACROSS the alternation.
 *
 * WHY INTERJECTION_MAX_SEAM_GAP_MS (400 ms), REUSED RATHER THAN A NEW CONSTANT.
 * It already answers exactly this question for the single-interjection case —
 * "has this speaker actually stopped?" — and the measurements it was chosen from
 * are the same measurements that govern here. On the shredded double-talk
 * fixture every gap inside one speaker's own run is between −1 ms and 241 ms,
 * while the smallest gap across a real floor change in the same meeting is
 * 4176 ms: an order of magnitude of clear air on each side of 400 ms. The
 * producer's own 1500 ms segment-gap threshold was rejected as the re-join bound
 * because it answers a different question — how long a silence may be before a
 * MONOLOGUE is worth splitting into two segments, with nobody competing — while
 * here another speaker is talking in the seam, and a speaker who is silent for
 * more than a breath while somebody else talks has yielded the floor. 400 ms is
 * the conservative direction: it never merges across a real pause, and the cost
 * of being wrong is a turn that stays split, which is today's behaviour.
 *
 * The seam is bounded on BOTH sides. An upper bound alone lets an arbitrarily
 * NEGATIVE seam through, which is not a narrow seam at all but a massive
 * self-overlap; a genuinely uninterrupted speaker measures 0 ms to the
 * millisecond, so the tolerance is symmetric around it.
 */
function buildTurnRuns<B extends OverlapBlock>(
  ordered: readonly B[],
  spansOf: (block: OverlapBlock) => Interval[],
): Array<TurnRun<B>> {
  const runs: Array<TurnRun<B>> = [];
  const open = new Map<string, TurnRun<B>>();
  for (let position = 0; position < ordered.length; position += 1) {
    const block = ordered[position]!;
    const key = speakerKey(block);
    const bounds = audibleBoundsOf(spansOf(block));
    const run = open.get(key);
    if (
      run &&
      bounds &&
      run.attested &&
      Math.abs(bounds.startMs - run.endMs) <= INTERJECTION_MAX_SEAM_GAP_MS &&
      seamHasContinuityEvidence(run, block, position, ordered, spansOf)
    ) {
      run.blocks.push(block);
      run.endMs = Math.max(run.endMs, bounds.endMs);
      run.lastPosition = position;
    } else {
      const fresh: TurnRun<B> = {
        speakerKey: key,
        speakerLabel: block.speakerLabel?.trim() || "Unknown speaker",
        blocks: [block],
        startMs: bounds?.startMs ?? block.startMs,
        endMs: bounds?.endMs ?? block.endMs,
        attested: bounds !== null,
        lastPosition: position,
      };
      runs.push(fresh);
      open.set(key, fresh);
    }
  }
  return runs;
}

/**
 * Keep fast turn-taking in chronological order.
 *
 * A short same-speaker gap is enough when the blocks are adjacent. When another
 * speaker occupies the gap, however, it is evidence of one continuous turn
 * only if that speaker touches or overlaps either side of the seam. This
 * retains the producer's shredded double-talk/backchannel shape (whose word
 * boundaries can meet exactly) while leaving a clean A/B/A exchange as three
 * turns. It deliberately does not attempt to decide finer conversational
 * semantics from a few milliseconds of decoder jitter.
 */
function seamHasContinuityEvidence<B extends OverlapBlock>(
  run: TurnRun<B>,
  next: B,
  nextPosition: number,
  ordered: readonly B[],
  spansOf: (block: OverlapBlock) => Interval[],
): boolean {
  if (nextPosition === run.lastPosition + 1) {
    return true;
  }

  const previous = run.blocks.at(-1);
  if (!previous) {
    return false;
  }
  const previousSpans = spansOf(previous);
  const nextSpans = spansOf(next);
  let sawAttestedInterveningSpeech = false;
  for (let position = run.lastPosition + 1; position < nextPosition; position += 1) {
    const between = ordered[position]!;
    if (speakerKey(between) === run.speakerKey) {
      continue;
    }
    const betweenSpans = spansOf(between);
    if (betweenSpans.length === 0) {
      continue;
    }
    sawAttestedInterveningSpeech = true;
    if (
      intervalPoolsNearlyTouch(betweenSpans, previousSpans) ||
      intervalPoolsNearlyTouch(betweenSpans, nextSpans)
    ) {
      return true;
    }
  }
  return !sawAttestedInterveningSpeech;
}

// Treat a sub-perceptual timestamp crack as a boundary touch, not as evidence
// that somebody yielded the floor. The rapid-exchange control has 50 ms of
// clean air on both sides and remains separate.
const TURN_SEAM_TOUCH_TOLERANCE_MS = 20;

function intervalPoolsNearlyTouch(left: readonly Interval[], right: readonly Interval[]): boolean {
  return left.some((a) =>
    right.some(
      (b) =>
        Math.max(a.startMs, b.startMs) - Math.min(a.endMs, b.endMs) <=
        TURN_SEAM_TOUCH_TOLERANCE_MS,
    ),
  );
}

/**
 * Which runs are backchannels said INSIDE another run, and which are turns of
 * their own.
 *
 * THIS IS THE BOUNDARY THE WHOLE FEATURE TURNS ON, and it is the same one
 * INTERJECTION_MAX_MIDDLE_MS has always drawn — only now it is measured on the
 * interjector's whole RE-JOINED run rather than on a single block. That change
 * is the fix. In sustained double-talk the producer hands us Ben's competing
 * sentence as fifteen fragments of 160–560 ms each; judged one at a time every
 * single one looks like a backchannel, and Ben's turn would be dismantled and
 * scattered as fifteen insets inside Cara's. Judged as the run it really is,
 * Ben speaks for 5600 ms — nearly three times the bound — and comes out as what
 * he is: a competing turn, rendered beside Cara's with the ordinary
 * simultaneous-speech badge on both.
 *
 * The same bound still admits the genuine backchannels in that meeting: Ben's
 * "Right." runs 401 ms and Ana's "Perfect." 600 ms. So on this fixture the
 * decision is between 600 ms and 5600 ms, and 2000 ms — around six or seven
 * words at conversational rate, past which a speaker is making a contribution
 * rather than acknowledging one — sits in the middle of that gap with an order
 * of magnitude of slack.
 *
 * The other three conditions are structural rather than judgemental:
 *   - the host must actually be SPLIT around this run (a `before` half and an
 *     `after` half). A host that was never cut has no paragraph break to remove,
 *     and the block that landed inside it is already reported by the ordinary
 *     containment badge;
 *   - the run must not outlast the host's turn, or it is not inside it;
 *   - the silence either side must be at most INTERJECTION_MAX_SIDE_GAP_MS, which
 *     rules out the exchange shape where one speaker finishes, a beat passes, and
 *     the other replies.
 *
 * Nesting is ONE LEVEL deep: a run that is itself nested may not host anything.
 * Two levels of inset is not a shape the reader can parse, and the producer has
 * never been observed to emit one.
 */
function nestTurnRuns<B extends OverlapBlock>(
  runs: ReadonlyArray<TurnRun<B>>,
  spansOf: (block: OverlapBlock) => Interval[],
): Map<TurnRun<B>, NestedTurn<B>> {
  const candidates = new Map<TurnRun<B>, NestedTurn<B>>();
  for (const run of runs) {
    const host = bestHostFor(run, runs, spansOf);
    if (host) {
      candidates.set(run, host);
    }
  }
  const nested = new Map<TurnRun<B>, NestedTurn<B>>();
  for (const [run, context] of candidates) {
    if (!candidates.has(context.host)) {
      nested.set(run, context);
    }
  }
  return nested;
}

function bestHostFor<B extends OverlapBlock>(
  run: TurnRun<B>,
  runs: ReadonlyArray<TurnRun<B>>,
  spansOf: (block: OverlapBlock) => Interval[],
): NestedTurn<B> | null {
  // Judged on this speaker's OWN CONTINUOUS SPEECH — the whole re-joined run,
  // silences removed — not on whatever the producer happened to emit as one
  // segment. That is the difference between a true and a false answer: across
  // the double-talk stretch Ben's competing sentence arrives as fifteen
  // fragments of 160-560 ms, every one of which looks like a backchannel on its
  // own, and his turn would be dismantled and scattered as fifteen insets
  // inside Cara's. As the run it really is he speaks for 5.2 s.
  if (!run.attested || runAudibleMs(run, spansOf) > INTERJECTION_MAX_MIDDLE_MS) {
    return null;
  }
  let best: NestedTurn<B> | null = null;
  let bestBeforeEndMs = Number.NEGATIVE_INFINITY;
  let bestSpanMs = -1;
  for (const host of runs) {
    if (host === run || !host.attested) {
      continue;
    }
    if (host.speakerKey === run.speakerKey || run.endMs > host.endMs) {
      continue;
    }
    const seam = seamAround(host, run.startMs, spansOf);
    if (!seam) {
      continue;
    }
    // AND a structural test, so the classification does not rest on a duration
    // alone: a backchannel lands in ONE gap in the host's speech, while a
    // speaker who is holding the floor weaves through it and the host's
    // fragments keep restarting inside their run. One is allowed because the
    // host routinely resumes while a genuine backchannel is still finishing —
    // Ben's "Right." runs 4.86-5.26 s while Ana resumes at 5.20.
    if (hostFragmentsStartingInside(host, run, spansOf) > 1) {
      continue;
    }
    if (run.startMs - seam.beforeEndMs > INTERJECTION_MAX_SIDE_GAP_MS) {
      continue;
    }
    if (seam.afterStartMs - run.endMs > INTERJECTION_MAX_SIDE_GAP_MS) {
      continue;
    }
    // Ties go to the turn this run interrupted most immediately, then to the
    // longer turn — the one a reader would call "the turn" rather than
    // whichever the scan reached first.
    const spanMs = host.endMs - host.startMs;
    if (seam.beforeEndMs > bestBeforeEndMs || (seam.beforeEndMs === bestBeforeEndMs && spanMs > bestSpanMs)) {
      best = { host, beforeId: seam.beforeId, afterId: seam.afterId };
      bestBeforeEndMs = seam.beforeEndMs;
      bestSpanMs = spanMs;
    }
  }
  return best;
}

/** How many of the host's fragments begin while `run` is still going. */
function hostFragmentsStartingInside<B extends OverlapBlock>(
  host: TurnRun<B>,
  run: TurnRun<B>,
  spansOf: (block: OverlapBlock) => Interval[],
): number {
  let inside = 0;
  for (const block of host.blocks) {
    const bounds = audibleBoundsOf(spansOf(block));
    if (bounds && bounds.startMs > run.startMs && bounds.startMs <= run.endMs) {
      inside += 1;
    }
  }
  return inside;
}

/**
 * The two consecutive halves of `host` that `startMs` falls between, if any.
 *
 * Returning null when there is no half on both sides is what enforces "the host
 * must actually have been SPLIT around this run". A host that was never cut has
 * no paragraph break to remove, so a block that landed inside it is not nested;
 * it stays a turn of its own, reported by the ordinary containment badge.
 */
function seamAround<B extends OverlapBlock>(
  host: TurnRun<B>,
  startMs: number,
  spansOf: (block: OverlapBlock) => Interval[],
): { beforeId: string; afterId: string; beforeEndMs: number; afterStartMs: number } | null {
  let index = -1;
  for (let position = 0; position < host.blocks.length; position += 1) {
    const bounds = audibleBoundsOf(spansOf(host.blocks[position]!));
    if (bounds && bounds.startMs < startMs) {
      index = position;
    }
  }
  const before = index < 0 ? undefined : host.blocks[index];
  const after = index < 0 ? undefined : host.blocks[index + 1];
  if (!before || !after) {
    return null;
  }
  const beforeBounds = audibleBoundsOf(spansOf(before));
  const afterBounds = audibleBoundsOf(spansOf(after));
  if (!beforeBounds || !afterBounds) {
    return null;
  }
  return {
    beforeId: before.id,
    afterId: after.id,
    beforeEndMs: beforeBounds.endMs,
    afterStartMs: afterBounds.startMs,
  };
}

/**
 * Mark each block of a nested run as spoken inside the turn it landed in, and
 * the half that resumes as continuing past it.
 *
 * This replaces the old fixed A/B/A scan, which could only see exactly ONE
 * intervening block. Chains of ten and more alternations are the real production
 * shape, and a fixed three-block window sees none of them.
 */
function markRejoinedTurnInterjections<B extends OverlapBlock>(
  ordered: readonly B[],
  accumulators: Map<string, OverlapAccumulator>,
  spansOf: (block: OverlapBlock) => Interval[],
): void {
  const runs = buildTurnRuns(ordered, spansOf);
  for (const [run, context] of nestTurnRuns(runs, spansOf)) {
    for (const block of run.blocks) {
      accumulatorFor(accumulators, block.id).interrupts = {
        speakerLabel: context.host.speakerLabel,
        beforeId: context.beforeId,
        afterId: context.afterId,
      };
    }
    accumulatorFor(accumulators, context.afterId).resumes = {
      speakerLabel: run.speakerLabel,
      blockId: run.blocks[0]!.id,
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
// The turn model
// ─────────────────────────────────────────────────────────────────────────────

/**
 * One rendered turn: a speaker's fragments back in one piece, whatever the
 * producer cut them into.
 *
 * NOTHING IS MERGED AND NO TEXT IS CONCATENATED. `blocks` are the very objects
 * the caller passed in, in time order, each keeping its own id, its own words
 * and its own seek anchors. A turn is a statement about which blocks belong
 * together, not a new block.
 */
export interface Turn<B> {
  /** Stable key: the id of the turn's first block. */
  readonly key: string;
  /** The speaker key the blocks agree on (`speaker`, else the trimmed label). */
  readonly speaker: string;
  readonly speakerLabel: string;
  /** First and last moment this speaker was AUDIBLE across the whole turn. */
  readonly startMs: number;
  readonly endMs: number;
  /** How long this speaker's own speech actually sounds, silences removed. */
  readonly audibleMs: number;
  /** The blocks, in time order. */
  readonly blocks: readonly B[];
  /** True when the producer split this turn and it has been put back together. */
  readonly rejoined: boolean;
  /** Short runs said inside this turn, in time order. */
  readonly interjections: readonly Turn<B>[];
  /** The key of the turn this one was said inside, when it is an interjection. */
  readonly interjectionOf?: string;
  /** The turn's own halves either side of an interjection, for a caller that wants them. */
  readonly interjectionSeam?: { readonly beforeId: string; readonly afterId: string };
}

/**
 * One pair of turns that were genuinely audible at the same time.
 *
 * `intervals` is the tape itself — ascending, disjoint, and measured on words
 * rather than paragraph extents — which is what a presentation needs to draw a
 * band, a gutter mark or a margin note over the right stretch of the page.
 *
 * `totalMs` is kept because it is what the credibility floor is applied to and
 * because a caller may want to sort or filter by it. It is NOT a thing to put
 * on the page: "0.2 s during Ivan" is a precision nobody acts on.
 */
export interface SimultaneousStretch {
  /** The two turns, the one that started first named first. */
  readonly turnKeys: readonly [string, string];
  readonly speakerLabels: readonly [string, string];
  /** Stretches of tape both were sounding, ascending and disjoint. */
  readonly intervals: readonly Interval[];
  /** Total simultaneous time. Measured; not a thing to show. */
  readonly totalMs: number;
  /**
   * `interjection` when the second turn is a short backchannel said inside the
   * first; `competing` when both were holding the floor.
   */
  readonly kind: "interjection" | "competing";
}

/** Turns in reading order, and the stretches where two of them collide. */
export interface TurnModel<B> {
  /** Top-level turns, earliest first. Interjections hang off their host. */
  readonly turns: readonly Turn<B>[];
  /** Every turn including the nested ones, keyed for lookup. */
  readonly turnsByKey: ReadonlyMap<string, Turn<B>>;
  /** Which turn each block belongs to, so a caller can go from a block to its turn. */
  readonly turnKeyByBlockId: ReadonlyMap<string, string>;
  /** Credible simultaneity, largest first. */
  readonly simultaneous: readonly SimultaneousStretch[];
}

/**
 * The reading structure of a transcript: who held the floor, what was said
 * inside somebody else's turn, and where two people genuinely spoke at once.
 *
 * WHAT THIS IS FOR. The producer interleaves every speaker's words by start
 * time and flushes a segment on each speaker change, so the moment two people
 * hold the floor at once BOTH of their sentences are shredded into one- to
 * three-word fragments that alternate. Measured on the overlap-and-pause
 * fixture, whose ground truth is known: Cara speaks one sentence 41.0-49.0 s and
 * Ben a different, competing sentence 43.2-49.0 s, and the producer emitted 31
 * alternating fragments — "f the" / "final" / "sign" / "off". A published
 * meeting showed the same at scale: 262 segments, 34% of them three words or
 * fewer, sentences cut mid-clause. This model is what a renderer needs to stop
 * showing that: the turns as they were spoken, with the fragments still intact
 * underneath.
 *
 * It deliberately makes no presentation decisions — no ordering beyond time, no
 * nesting depth beyond one, no copy, no durations to display. The transcript
 * pane still runs on groupInterruptedTurns, which sees only the small case;
 * this is what the rebuilt pane will read, and it is shaped so that any of the
 * treatments under consideration can be built on it without the judgement being
 * redone.
 */
export function buildTurnModel<B extends OverlapBlock>(
  blocks: readonly B[],
  options: { readonly minOverlapMs?: number } = {},
): TurnModel<B> {
  const ordered = sortBlocksInReadingOrder(blocks);
  const spansOf = audibleSpanCache();
  const runs = buildTurnRuns(ordered, spansOf);
  const nested = nestTurnRuns(runs, spansOf);

  const childrenByHost = new Map<TurnRun<B>, Array<TurnRun<B>>>();
  for (const [run, context] of nested) {
    const siblings = childrenByHost.get(context.host);
    if (siblings) {
      siblings.push(run);
    } else {
      childrenByHost.set(context.host, [run]);
    }
  }

  const turnsByKey = new Map<string, Turn<B>>();
  const turnKeyByBlockId = new Map<string, string>();
  const turnByRun = new Map<TurnRun<B>, Turn<B>>();
  const register = (run: TurnRun<B>, turn: Turn<B>): Turn<B> => {
    turnsByKey.set(turn.key, turn);
    turnByRun.set(run, turn);
    for (const block of run.blocks) {
      turnKeyByBlockId.set(block.id, turn.key);
    }
    return turn;
  };

  const turns: Array<Turn<B>> = [];
  for (const run of runs) {
    if (nested.has(run)) {
      continue;
    }
    const children = (childrenByHost.get(run) ?? []).sort(
      (left, right) => left.startMs - right.startMs,
    );
    const interjections = children.map((child) => {
      const context = nested.get(child)!;
      return register(child, {
        ...turnOf(child, spansOf),
        interjections: [],
        interjectionOf: run.blocks[0]!.id,
        interjectionSeam: { beforeId: context.beforeId, afterId: context.afterId },
      });
    });
    turns.push(register(run, { ...turnOf(run, spansOf), interjections }));
  }

  return {
    turns,
    turnsByKey,
    turnKeyByBlockId,
    simultaneous: measureSimultaneity(
      runs,
      turnByRun,
      nested,
      spansOf,
      options.minOverlapMs ?? MIN_CREDIBLE_OVERLAP_MS,
    ),
  };
}

function turnOf<B extends OverlapBlock>(
  run: TurnRun<B>,
  spansOf: (block: OverlapBlock) => Interval[],
): Omit<Turn<B>, "interjections"> {
  return {
    key: run.blocks[0]!.id,
    speaker: run.speakerKey,
    speakerLabel: run.speakerLabel,
    startMs: run.startMs,
    endMs: run.endMs,
    audibleMs: runAudibleMs(run, spansOf),
    blocks: run.blocks,
    rejoined: run.blocks.length > 1,
  };
}

/**
 * Credible simultaneity between whole turns, largest first.
 *
 * MEASURED BETWEEN TURNS, NOT BETWEEN FRAGMENTS, and that is not a refinement.
 * Of the 31 fragment pairs the producer emits across the double-talk stretch,
 * most intersect for less than the 150 ms credibility floor and would be thrown
 * away one at a time; summing what survives quotes a fraction of the truth.
 * Between the re-joined turns the same stretch measures 5.1 s.
 *
 * The same sweep the per-block analysis uses, over stand-in blocks whose
 * "words" are each turn's own audible intervals — so the credibility floor, the
 * union arithmetic and the never-simultaneous-with-yourself rule are the ones
 * already measured and tested. A turn with no defensible audible time carries
 * the rejection forward and so cannot fall back to its extent and fabricate the
 * overlap the rejection removed.
 */
function measureSimultaneity<B extends OverlapBlock>(
  runs: readonly TurnRun<B>[],
  turnByRun: ReadonlyMap<TurnRun<B>, Turn<B>>,
  nested: ReadonlyMap<TurnRun<B>, NestedTurn<B>>,
  spansOf: (block: OverlapBlock) => Interval[],
  minOverlapMs: number,
): SimultaneousStretch[] {
  const intervalsByRun = new Map<TurnRun<B>, Interval[]>();
  for (const run of runs) {
    intervalsByRun.set(run, mergeIntervals(run.blocks.flatMap((block) => spansOf(block))));
  }

  const stretches: SimultaneousStretch[] = [];
  for (let left = 0; left < runs.length; left += 1) {
    for (let right = left + 1; right < runs.length; right += 1) {
      const earlier = runs[left]!;
      const later = runs[right]!;
      if (earlier.speakerKey === later.speakerKey) {
        continue;
      }
      const intervals = intersectIntervals(
        intervalsByRun.get(earlier) ?? [],
        intervalsByRun.get(later) ?? [],
      );
      const simultaneousMs = totalMs(intervals);
      if (simultaneousMs < minOverlapMs) {
        continue;
      }
      const earlierTurn = turnByRun.get(earlier)!;
      const laterTurn = turnByRun.get(later)!;
      stretches.push({
        turnKeys: [earlierTurn.key, laterTurn.key],
        speakerLabels: [earlierTurn.speakerLabel, laterTurn.speakerLabel],
        intervals,
        totalMs: simultaneousMs,
        kind:
          nested.get(later)?.host === earlier || nested.get(earlier)?.host === later
            ? "interjection"
            : "competing",
      });
    }
  }
  return stretches.sort(
    (left, right) =>
      right.totalMs - left.totalMs || left.turnKeys[0].localeCompare(right.turnKeys[0]),
  );
}

function runAudibleMs<B extends OverlapBlock>(
  run: TurnRun<B>,
  spansOf: (block: OverlapBlock) => Interval[],
): number {
  return totalMs(mergeIntervals(run.blocks.flatMap((block) => spansOf(block))));
}

// ─────────────────────────────────────────────────────────────────────────────
// Reading rows
// ─────────────────────────────────────────────────────────────────────────────

/**
 * One thing a row lays out inline: a stretch of the speaker's own speech, or
 * somebody else's short remark dropped into it.
 *
 * A `speech` member is one of the producer's blocks, untouched — its own id,
 * its own words, its own seek anchors. Several of them in a row are the
 * fragments one turn was shredded into, and they are meant to be rendered as
 * continuous prose with nothing between them but a space: the row is the
 * paragraph, not the member.
 *
 * An `interjection` member is a whole nested turn (usually one block, sometimes
 * a re-joined pair), sitting at the seam where it was actually said.
 */
export type TranscriptRowMember<B> =
  | { readonly kind: "speech"; readonly key: string; readonly block: B }
  | {
      readonly kind: "interjection";
      readonly key: string;
      readonly speakerLabel: string;
      readonly blocks: readonly B[];
    };

/**
 * One turn, laid out for reading: who spoke, when they started, everything they
 * said in one piece, and what was said inside or across it.
 */
export interface TranscriptRow<B> {
  /** Stable key: the id of the turn's first block. */
  readonly key: string;
  readonly speaker: string;
  readonly speakerLabel: string;
  /** First and last audible moment of the turn, interjections excluded. */
  readonly startMs: number;
  readonly endMs: number;
  /** The turn's own blocks in time order, with interjections at their seams. */
  readonly members: readonly TranscriptRowMember<B>[];
  /**
   * The other speakers this turn was genuinely audible at the same time as,
   * largest collision first and each named once. A backchannel is NOT in here:
   * it is already on the page as a chip inside this row.
   *
   * Labels, not durations. "0.2 s with Chris" is a precision nobody acts on;
   * "over Chris" is the whole of what a reader can do something with.
   */
  readonly over: readonly string[];
}

/**
 * The transcript pane's rows: one row per turn, in the order they were spoken.
 *
 * This is the whole of the presentation decision, and it is deliberately small,
 * because buildTurnModel has already made every judgement: which fragments were
 * one turn, which short runs were said inside somebody else's, and where two
 * people genuinely collided. All this adds is where each of those things goes
 * on the page.
 *
 * WHAT A ROW IS. One turn — so a speaker's sixteen shredded fragments are one
 * row and read as one paragraph, and the reader stops being told that one
 * sentence was sixteen turns. Nothing is merged and no text is concatenated:
 * the blocks come through as themselves, in order, and it is the renderer's job
 * to set them as continuous prose. Every block keeps its id, its words and its
 * seek targets.
 *
 * WHAT AN INTERJECTION IS. A backchannel is not a row of its own: it is a
 * member of the row it landed in, placed after the block it followed, so that
 * "(Ben: Right.)" reads where it happened instead of cutting the host's
 * sentence in half. Its blocks are the same untouched blocks — a chip is still
 * seekable, still highlightable, still countable.
 *
 * WHAT `over` IS. Two people holding the floor at once cannot be nested, so the
 * collision is named on both rows and nowhere else. No duration: the model
 * keeps the measurement, the page does not show it.
 *
 * Expects blocks already in reading order (see sortBlocksInReadingOrder);
 * buildTurnModel sorts defensively anyway.
 */
export function buildTranscriptRows<B extends OverlapBlock>(
  blocks: readonly B[],
  options: { readonly minOverlapMs?: number } = {},
): Array<TranscriptRow<B>> {
  const model = buildTurnModel(blocks, options);

  const over = new Map<string, string[]>();
  const nameOver = (turnKey: string, speakerLabel: string): void => {
    const named = over.get(turnKey);
    if (!named) {
      over.set(turnKey, [speakerLabel]);
    } else if (!named.includes(speakerLabel)) {
      named.push(speakerLabel);
    }
  };
  for (const stretch of model.simultaneous) {
    // Interjections are already on the page as chips; marking them again as a
    // collision would put the noise back a different way.
    if (stretch.kind !== "competing") {
      continue;
    }
    nameOver(stretch.turnKeys[0], stretch.speakerLabels[1]);
    nameOver(stretch.turnKeys[1], stretch.speakerLabels[0]);
  }

  // Where the producer's own paragraph breaks fall. A turn is one turn however
  // long it ran, but a turn is not one PARAGRAPH: when a speaker holds the floor
  // for minutes the producer ends a block on its word-count limit and starts the
  // next one immediately, and those breaks are the only paragraphing a long
  // update has. Rejoining them too turns a real standup answer into a wall of
  // text - measured on one meeting, rows of 209 s, 101 s and 97 s against a
  // median of 8.7 s. So the reading unit splits where nothing came between two
  // of the speaker's blocks, and stays whole where somebody else's speech did,
  // which is the shredding this module exists to undo.
  const readingPosition = new Map<string, number>();
  sortBlocksInReadingOrder(blocks).forEach((block, index) => {
    readingPosition.set(block.id, index);
  });
  const producerBrokeHere = (before: B, after: B): boolean => {
    const from = readingPosition.get(before.id);
    const to = readingPosition.get(after.id);
    return from !== undefined && to !== undefined && to === from + 1;
  };

  const rows: Array<TranscriptRow<B>> = [];
  for (const turn of model.turns) {
    const paragraphs: Array<Array<TranscriptRowMember<B>>> = [];
    let current: Array<TranscriptRowMember<B>> = [];
    let previousSpeech: B | null = null;
    for (const member of layOutTurn(turn)) {
      if (
        member.kind === "speech" &&
        previousSpeech &&
        current.at(-1)?.kind === "speech" &&
        producerBrokeHere(previousSpeech, member.block)
      ) {
        paragraphs.push(current);
        current = [];
      }
      current.push(member);
      if (member.kind === "speech") {
        previousSpeech = member.block;
      }
    }
    if (current.length > 0) {
      paragraphs.push(current);
    }
    paragraphs.forEach((members, index) => {
      const blocksHere = members.flatMap((m) => (m.kind === "speech" ? [m.block] : m.blocks));
      rows.push({
        // The turn's own key on the first paragraph, so callers that resolve a
        // turn by key still land on it; later paragraphs key by their own first
        // block, which is also their scroll anchor.
        key: index === 0 ? turn.key : members[0]!.key,
        speaker: turn.speaker,
        speakerLabel: turn.speakerLabel,
        startMs: Math.min(...blocksHere.map((block) => block.startMs)),
        endMs: Math.max(...blocksHere.map((block) => block.endMs)),
        members,
        // Naming the collision once per turn: repeating it on every paragraph
        // of a long answer is the badge noise this rendering removed.
        over: index === 0 ? (over.get(turn.key) ?? []) : [],
      });
    });
  }
  return rows;
}

/**
 * Stable scroll target for the blocks sounding now.
 *
 * A rendered row can contain many producer fragments (and an interjection
 * chip). Following those raw ids makes smooth scrolling bounce inside one
 * paragraph. Resolve them back to the first top-level row that contains any
 * sounding block instead; competing rows still choose the earlier row in
 * reading order.
 */
export function followRowKeyForBlocks<B extends OverlapBlock>(
  rows: readonly TranscriptRow<B>[],
  soundingBlocks: readonly B[],
): string | null {
  const soundingIds = new Set(soundingBlocks.map((block) => block.id));
  if (soundingIds.size === 0) {
    return null;
  }
  for (const row of rows) {
    for (const member of row.members) {
      const blocks = member.kind === "speech" ? [member.block] : member.blocks;
      if (blocks.some((block) => soundingIds.has(block.id))) {
        return row.key;
      }
    }
  }
  return null;
}

/**
 * A turn's blocks in time order, each interjection following the block it was
 * said after.
 *
 * The seam is the model's own (`interjectionSeam.beforeId`) rather than a
 * fresh comparison of timestamps, so the chip lands exactly where the producer
 * cut the turn — which is where the reader heard it. An interjection whose seam
 * names no block of this turn cannot be dropped on the floor: it goes last, so
 * a malformed model loses a chip's position but never its words.
 */
function layOutTurn<B extends OverlapBlock>(turn: Turn<B>): Array<TranscriptRowMember<B>> {
  const ownIds = new Set(turn.blocks.map((block) => block.id));
  const lastId = turn.blocks.at(-1)?.id;
  const atSeam = new Map<string, Array<Turn<B>>>();
  for (const inner of turn.interjections) {
    const seamId = inner.interjectionSeam?.beforeId;
    const anchorId = seamId !== undefined && ownIds.has(seamId) ? seamId : lastId;
    if (anchorId === undefined) {
      continue;
    }
    const waiting = atSeam.get(anchorId);
    if (waiting) {
      waiting.push(inner);
    } else {
      atSeam.set(anchorId, [inner]);
    }
  }

  const members: Array<TranscriptRowMember<B>> = [];
  for (const block of turn.blocks) {
    members.push({ kind: "speech", key: block.id, block });
    for (const inner of atSeam.get(block.id) ?? []) {
      members.push({
        kind: "interjection",
        key: inner.key,
        speakerLabel: inner.speakerLabel,
        blocks: inner.blocks,
      });
    }
  }
  return members;
}
