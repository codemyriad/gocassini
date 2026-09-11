<script lang="ts">
  import { createEventDispatcher, onDestroy, onMount, tick } from "svelte";
  import { fade } from "svelte/transition";
  import { cubicOut } from "svelte/easing";
  import { marked } from "marked";
  import DOMPurify from "dompurify";
  import {
    Play,
    Pause,
    Keyboard,
    Calendar,
    Clock,
    FileText,
    MessageSquare,
    Users,
    ArrowLeft,
    CassetteTape,
    X,
  } from "@lucide/svelte";
  import {
    formatClockTime,
    isLikelyCrosstalkAcrossBlocks,
    filterDisplaySegmentsByQuery,
    judgedDisplaySegments,
    normalizeSpeakerLabel,
    parseTimeHash,
    type JudgedDisplaySegment,
  } from "../core/transcript";
  import TranscriptWords from "./TranscriptWords.svelte";
  import MeetingTags from "./marking/MeetingTags.svelte";
  import TranscriptFrame from "./marking/TranscriptFrame.svelte";
  import { createMarksSession } from "./marking/session";
  import { findStops } from "../core/find";
  import { wordsByTime } from "../core/marking";
  import type {
    AnnotationRequest,
    AnnotationResult,
    MeetingAnnotations,
    VocabularyTag,
  } from "../viewer/annotations";
  import { createWordHighlighter } from "../core/wordHighlight";
  import {
    keyboardEventTargetsControl,
    tokensPreserveText,
    transcriptWordParts,
    type TranscriptWordPart,
  } from "../core/wordInteraction";
  import {
    buildTranscriptRows,
    followRowKeyForBlocks,
    repairTurnFinalWordInflation,
    sortBlocksInReadingOrder,
    type TranscriptRow,
  } from "../core/overlap";
  import {
    buildPlayheadIndex,
    resolvePlayhead,
    type PlayheadIndex,
  } from "../core/playhead";
  import type {
    DisplayTranscriptV1,
    ReadableTranscriptV1,
    TranscriptIndex,
  } from "../core/types";
  import type {
    ArtifactMetadata,
    ArtifactMetadataRow,
    ArtifactTimingPrecision,
    LoadedArtifact,
  } from "../viewer/loadArtifact";
  import { buildDisplayTranscriptFromArtifacts, type PortableTranscriptDescriptor } from "../viewer/portable";
  import { formatMeetingDate, type MeetingCatalogEntry } from "../viewer/catalog";
  import { roomLabelOf } from "../viewer/rooms";
  import {
    formatInsightCreated,
    insightHeadline,
    type InsightRecord,
  } from "../viewer/insights";
  import type { DataProvider } from "../viewer/dataProvider";
  import { buildViewerHash, readViewerHash, viewerUrlWithHash } from "../viewer/hashRouting";

  // The single-meeting reading surface (D-420 V1). It is "smart": given a
  // DataProvider and a meeting entry (or bundled mode) it loads the artifact,
  // owns playback + transcript switching, and owns the per-meeting tx/t hash
  // params. The shell owns which meeting is selected (the `meeting` prop) and
  // the meeting hash param. Extracting this makes it the standalone shareable
  // single-meeting embed (mount it directly with bundled=true).
  export let dataProvider: DataProvider;
  export let meeting: MeetingCatalogEntry | null = null;
  export let bundled = false;
  // inSheet is true when the shell has opened this meeting as a sheet over the
  // browse list (D-654) rather than mounting it as a page of its own. It only
  // decides how leaving is offered: a close control at every width, instead of
  // the narrow-viewport-only back arrow a two-column layout needed.
  export let inSheet = false;
  export let isDesktop = false;
  export let prefersReducedMotion = false;
  // hasCatalog drives the "choose a meeting" empty-state copy (a list exists to
  // choose from) vs the bare viewer-ready copy.
  export let hasCatalog = false;
  // Shell-level "meeting id not in catalog" error (deep-link to a removed
  // meeting). Rendered in the same not-found card as an internal load failure.
  export let notFoundMessage = "";

  // Which insights drew on this meeting, resolved by the shell against the
  // WHOLE catalog and handed down. Empty in every build with no operator to
  // ask — a standalone export has no insights and shows no section, rather
  // than an empty one that implies there could have been some.
  export let linkedInsights: InsightRecord[] = [];
  // How many of each insight's sources this caller can read, again the shell's
  // answer: it is not meetingIds.length, because a source they may not read is
  // absent, and a count is a disclosure that it existed.
  export let insightSourceCounts: ReadonlyMap<string, number> = new Map();

  // Tags and marks (D-746), bound by the shell to the current meeting. Without a
  // loader, as in the standalone export, there is no tagging here at all.
  export let tagVocabulary: VocabularyTag[] = [];
  export let loadAnnotations: (() => Promise<MeetingAnnotations>) | null = null;
  export let applyAnnotations: ((request: AnnotationRequest) => Promise<AnnotationResult>) | null = null;

  const dispatch = createEventDispatcher<{
    back: void;
    enriched: MeetingCatalogEntry;
    openInsight: InsightRecord;
    tagsChanged: AnnotationResult;
  }>();
  const marks = createMarksSession((result) => dispatch("tagsChanged", result));
  let openedMarksFor: string | null = null;
  let headerHeight = 0;
  let scrollHeight = 0;
  let playerHeight = 0;

  type DisplaySegment = JudgedDisplaySegment;

  const CONTINUATION_GAP_MS = 60_000;


  let transcriptIndex: TranscriptIndex | null = null;
  let displayTranscript: DisplayTranscriptV1 | null = null;
  let readableTranscript: ReadableTranscriptV1 | null = null;
  let summaryMarkdown: string | null = null;
  let audioSrc = "";
  let captionsSrc: string | null = null;
  let chaptersSrc: string | null = null;
  let timingPrecision: ArtifactTimingPrecision | null = null;
  let artifactMetadata: ArtifactMetadata | null = null;
  // Whether the PRODUCER already bounded this artifact's word ends by the
  // measured audio (manifest provenance.wordTimings.endsBoundedByAudio). When
  // it did, the legacy display-time repair must stand down: its budget would
  // clip a genuinely long word — one measured at 1.44 s against a 240 ms
  // median — back to 1 s and undo the production fix. Absent on all 197
  // already-published meetings, which is where the repair earns its keep.
  let wordEndsBoundedByAudio = false;
  let availableTranscripts: PortableTranscriptDescriptor[] = [];
  let currentTranscriptId = "";
  let defaultTranscriptId = "";
  let transcriptSwitchPending = false;
  // Inline error for transcript-switch failures (e.g. SHA mismatch on an
  // alternate body). Kept separate from `errorMessage` so it shows next to the
  // switcher instead of taking over the whole "meeting failed to load" state.
  let transcriptSwitchError = "";
  let errorMessage = "";
  // lastBundled tracks the previous value of the `bundled` prop so the reactive
  // block below fires on transitions only. Initialised from the prop because
  // onMount already loads when it starts true; without that, mounting bundled
  // would load twice.
  let lastBundled = bundled;
  let loading = false;

  let currentTimeMs = 0;
  // Which turns are sounding right now, pushed by syncHighlight rather than
  // derived per frame. `activeSegments` holds the very objects
  // `displaySegments` holds, so identity comparisons downstream stay sound.
  // Word controls subscribe separately, so neither a frame nor a row change
  // invalidates every rendered word. Acoustic row evidence stays independent
  // of display alignment, which may contain rewritten or untimed prose.
  let activeSegments: DisplaySegment[] = [];
  let activeSegmentIds = new Set<string>();
  let activeFollowRowKey: string | null = null;
  let playheadIndex: PlayheadIndex<DisplaySegment> = buildPlayheadIndex<DisplaySegment>([]);
  // The stretch `activeSegments` is known to be correct across. Deliberately
  // empty to begin with, so the first call always resolves.
  let highlightValidFromMs = Number.POSITIVE_INFINITY;
  let highlightValidUntilMs = Number.NEGATIVE_INFINITY;
  const wordHighlighter = createWordHighlighter();
  let wordHighlightSource: Map<string, TranscriptWordPart[]> | null = null;
  let durationMs = 0;
  let playing = false;
  let followPlayback = true;
  let manualScrollLock = false;
  let pendingSeekMs: number | null = null;

  let transcriptPane: HTMLElement | null = null;
  let audioEl: HTMLAudioElement | null = null;
  let animationFrameId = 0;
  let lastAutoScrollRowKey = "";

  // The meeting-view section; used to resolve element lookups against the
  // component's ROOT NODE — the shadow root in the embedded build, the document
  // in standalone — since document.getElementById can't see shadow-tree nodes.
  let viewRootEl: HTMLElement | undefined;

  let shortcutsDialog: HTMLDialogElement | null = null;
  function openShortcutsDialog() {
    shortcutsDialog?.showModal();
  }

  // attemptedKey guards the reactive load: it is set to meeting.id BEFORE the
  // async load and is NOT cleared on failure, so a failed load does not retrigger
  // (the meeting object identity churns when the shell enriches the catalog, but
  // the id is stable). Selecting a different meeting changes the id → reload.
  let attemptedKey: string | null = null;

  function contentFadeConfig() {
    return prefersReducedMotion ? { duration: 0 } : { duration: 360 };
  }

  function playerFadeConfig() {
    // cubicOut so the card materializes early in the transition rather than
    // snapping into view near the end — pure linear opacity reads as a
    // "click" when the card bg is close to the page bg.
    return prefersReducedMotion ? { duration: 0 } : { duration: 320, easing: cubicOut };
  }

  function renderSummaryHtml(markdown: string | null): string {
    if (typeof markdown !== "string" || markdown.trim() === "") {
      return "";
    }
    const rawHtml = marked.parse(markdown, { async: false }) as string;
    return DOMPurify.sanitize(rawHtml, { USE_PROFILES: { html: true } });
  }

  // Routing is hash-only (see src/viewer/hashRouting.ts for why and the wire
  // format). These thin wrappers bind the pure helpers to the live location.
  function currentViewerHash() {
    return readViewerHash(window.location.hash);
  }

  function viewerHref(hash: string): string {
    return viewerUrlWithHash(window.location.href, hash);
  }

  function applyArtifact(artifact: LoadedArtifact) {
    stopPlaybackClock();
    audioEl?.pause();
    playing = false;
    currentTimeMs = 0;
    transcriptIndex = artifact.index;
    displayTranscript = artifact.displayTranscript;
    readableTranscript = artifact.readableTranscript;
    summaryMarkdown = artifact.summary;
    audioSrc = artifact.audioSrc;
    captionsSrc = artifact.captionsSrc;
    chaptersSrc = artifact.chaptersSrc;
    timingPrecision = artifact.timingPrecision;
    artifactMetadata = artifact.metadata;
    wordEndsBoundedByAudio = artifact.wordEndsBoundedByAudio;
    availableTranscripts = artifact.availableTranscripts;
    currentTranscriptId = artifact.currentTranscriptId;
    defaultTranscriptId =
      artifact.availableTranscripts.find((entry) => entry.isDefault)?.id ?? artifact.currentTranscriptId;
    durationMs = artifact.index.transcript.media.durationMs;
    errorMessage = "";
    manualScrollLock = false;
    lastAutoScrollRowKey = "";
  }

  function applySwitchedTranscript(artifact: LoadedArtifact) {
    // Audio is not paused during a switch — it keeps playing through the
    // ~100-300ms rebuild. We deliberately do NOT touch audioEl.currentTime
    // here: writing back a value captured before the await would rewind
    // playback by the duration of the switch (audible stutter). The
    // requestAnimationFrame loop already keeps `currentTimeMs` synced with
    // `audioEl.currentTime`, so the transcript highlight tracks correctly
    // as soon as the new index renders.
    transcriptIndex = artifact.index;
    displayTranscript = artifact.displayTranscript;
    readableTranscript = artifact.readableTranscript;
    timingPrecision = artifact.timingPrecision;
    artifactMetadata = artifact.metadata;
    wordEndsBoundedByAudio = artifact.wordEndsBoundedByAudio;
    availableTranscripts = artifact.availableTranscripts;
    currentTranscriptId = artifact.currentTranscriptId;
    lastAutoScrollRowKey = "";
  }

  function resetLoadedArtifact() {
    stopPlaybackClock();
    audioEl?.pause();
    playing = false;
    currentTimeMs = 0;
    transcriptIndex = null;
    displayTranscript = null;
    readableTranscript = null;
    summaryMarkdown = null;
    audioSrc = "";
    captionsSrc = null;
    chaptersSrc = null;
    timingPrecision = null;
    artifactMetadata = null;
    wordEndsBoundedByAudio = false;
    availableTranscripts = [];
    currentTranscriptId = "";
    defaultTranscriptId = "";
    transcriptSwitchPending = false;
    transcriptSwitchError = "";
    durationMs = 0;
    manualScrollLock = false;
    lastAutoScrollRowKey = "";
  }

  function mergeMeetingRuntimeSummary(
    entry: MeetingCatalogEntry,
    artifact: LoadedArtifact,
  ): MeetingCatalogEntry {
    return {
      ...entry,
      speakerCount: artifact.transcript.speakers.length,
      segmentCount: artifact.transcript.segments.length,
      digestDurationMs: artifact.transcript.media.durationMs,
    };
  }

  async function loadForMeeting(entry: MeetingCatalogEntry) {
    // Drop stale artifact synchronously so the view immediately shows a loading
    // state for *this* meeting rather than the previous meeting's transcript
    // bleeding through.
    resetLoadedArtifact();
    loading = true;
    errorMessage = "";
    pendingSeekMs = parseTimeHash(window.location.hash);
    try {
      const artifact = await dataProvider.loadMeetingForEntry(entry);
      applyArtifact(artifact);
      await maybeApplyUrlTranscript(entry);
      // Real speaker/segment/duration counts → shell updates the list card.
      dispatch("enriched", mergeMeetingRuntimeSummary(entry, artifact));
    } catch (error) {
      resetLoadedArtifact();
      errorMessage = error instanceof Error ? error.message : String(error);
    } finally {
      loading = false;
    }
  }

  async function loadBundled() {
    loading = true;
    errorMessage = "";
    pendingSeekMs = parseTimeHash(window.location.hash);
    try {
      const artifact = await dataProvider.loadBundledArtifact();
      applyArtifact(artifact);
    } catch (error) {
      errorMessage = error instanceof Error ? error.message : String(error);
    } finally {
      loading = false;
    }
  }

  async function handleTranscriptSwitch(targetId: string) {
    if (
      transcriptSwitchPending ||
      !meeting?.audioPath ||
      targetId === currentTranscriptId ||
      !availableTranscripts.some((entry) => entry.id === targetId)
    ) {
      return;
    }
    transcriptSwitchPending = true;
    transcriptSwitchError = "";
    try {
      const next = await dataProvider.switchTranscript(meeting, targetId);
      applySwitchedTranscript(next);
      writeTranscriptUrlParam(targetId);
    } catch (error) {
      // Surface inline next to the switcher; previous transcript stays visible.
      transcriptSwitchError = `Couldn't load that transcript: ${
        error instanceof Error ? error.message : String(error)
      }`;
    } finally {
      transcriptSwitchPending = false;
    }
  }

  function dismissTranscriptSwitchError() {
    transcriptSwitchError = "";
  }

  function writeTranscriptUrlParam(targetId: string) {
    const current = currentViewerHash();
    const tx = targetId && targetId !== defaultTranscriptId ? targetId : "";
    window.history.replaceState(
      {},
      "",
      viewerHref(buildViewerHash({ meeting: current.meeting, tx })),
    );
  }

  function clearTranscriptUrlParam() {
    const current = currentViewerHash();
    if (current.tx) {
      window.history.replaceState(
        {},
        "",
        viewerHref(buildViewerHash({ meeting: current.meeting })),
      );
    }
  }

  async function maybeApplyUrlTranscript(entry: MeetingCatalogEntry) {
    const requested = currentViewerHash().tx;
    if (!requested || !entry.audioPath) {
      return;
    }
    if (!availableTranscripts.some((descriptor) => descriptor.id === requested)) {
      clearTranscriptUrlParam();
      return;
    }
    if (requested === currentTranscriptId) {
      return;
    }
    transcriptSwitchPending = true;
    try {
      const next = await dataProvider.switchTranscript(entry, requested);
      applySwitchedTranscript(next);
    } catch {
      clearTranscriptUrlParam();
    } finally {
      transcriptSwitchPending = false;
    }
  }

  /**
   * Read the playhead once. Deliberately does NOT schedule the next frame.
   *
   * It used to. Because it is also called from `handleTimeUpdate` (the media
   * element fires `timeupdate` about four times a second) and from `seekTo`,
   * every one of those calls started ANOTHER self-scheduling animation-frame
   * chain while one was already running, and `animationFrameId` kept only the
   * newest — so `stopPlaybackClock` could cancel exactly one of them and the
   * rest ran until playback stopped. Counted in the browser: 139 callbacks in
   * the first second of playback, 323 by the twelfth, and once the transcript
   * highlight stopped starving the frame budget, 2,644 — forty-four clocks
   * where there should be one, each re-reading `audioEl.currentTime` and
   * re-entering the reactive graph. Scheduling now belongs to the clock alone.
   */
  function syncPlaybackTime() {
    currentTimeMs = asFiniteMilliseconds(Math.round((audioEl?.currentTime ?? 0) * 1000));
  }

  function advancePlaybackClock() {
    syncPlaybackTime();
    if (playing) {
      animationFrameId = window.requestAnimationFrame(advancePlaybackClock);
    }
  }

  function startPlaybackClock() {
    stopPlaybackClock();
    animationFrameId = window.requestAnimationFrame(advancePlaybackClock);
  }

  function stopPlaybackClock() {
    if (animationFrameId) {
      window.cancelAnimationFrame(animationFrameId);
      animationFrameId = 0;
    }
  }

  function handleLoadedMetadata() {
    syncDurationFromMedia();
    if (pendingSeekMs !== null) {
      seekTo(pendingSeekMs);
      pendingSeekMs = null;
    }
  }

  function handleDurationChange() {
    syncDurationFromMedia();
  }

  function handlePlay() {
    playing = true;
    startPlaybackClock();
  }

  function handlePause() {
    playing = false;
    stopPlaybackClock();
    syncPlaybackTime();
  }

  function handleTimeUpdate() {
    syncPlaybackTime();
  }

  function togglePlayback() {
    if (!audioEl) {
      return;
    }
    if (audioEl.paused) {
      void audioEl.play();
      return;
    }
    audioEl.pause();
  }

  function seekTo(ms: number) {
    const nextTimeMs = Math.min(Math.max(0, ms), clampedDurationMs || ms);
    currentTimeMs = nextTimeMs;
    if (!audioEl) {
      pendingSeekMs = nextTimeMs;
      return;
    }
    if (audioEl.readyState === 0) {
      pendingSeekMs = nextTimeMs;
      audioEl.load();
      return;
    }
    audioEl.currentTime = nextTimeMs / 1000;
    syncPlaybackTime();
  }

  function handleTimelineInput(event: Event) {
    const target = event.currentTarget;
    if (!(target instanceof HTMLInputElement)) {
      return;
    }
    seekTo(Number(target.value));
  }

  function syncDurationFromMedia() {
    const reportedDurationMs = asFiniteMilliseconds(Math.round((audioEl?.duration ?? 0) * 1000));
    if (reportedDurationMs > 0) {
      durationMs = Math.max(durationMs, reportedDurationMs);
    }
  }

  function asFiniteMilliseconds(value: number): number {
    return Number.isFinite(value) ? value : 0;
  }

  function segmentDomId(segmentId: string): string {
    return `segment-${segmentId.replace(/[^A-Za-z0-9_-]/g, "_")}`;
  }

  async function scrollSegmentIntoView(segmentId: string, behavior: ScrollBehavior) {
    await tick();
    // In the embedded build the segment <article>s live inside the shadow root,
    // where document.getElementById can't reach them — resolve against the
    // component's root node (ShadowRoot embedded / Document standalone).
    const id = segmentDomId(segmentId);
    const root = viewRootEl?.getRootNode() as Document | ShadowRoot | undefined;
    const element = root?.getElementById?.(id) ?? document.getElementById(id);
    element?.scrollIntoView({ behavior, block: "center" });
  }

  function resumeFollow() {
    followPlayback = true;
    manualScrollLock = false;
    if (activeFollowRowKey) {
      lastAutoScrollRowKey = "";
      void scrollSegmentIntoView(activeFollowRowKey, "smooth");
    }
  }

  function toggleFollowPlayback() {
    if (followPlayback && manualScrollLock) {
      resumeFollow();
      return;
    }
    followPlayback = !followPlayback;
    if (followPlayback) {
      manualScrollLock = false;
    }
  }

  function handleWindowKeydown(event: KeyboardEvent) {
    if (event.code !== "Space" || event.repeat || event.defaultPrevented) {
      return;
    }
    // This is a WINDOW-level handler, so it stays live while the component is
    // merely hidden (display:none) rather than unmounted — e.g. under the app
    // shell's operator surface, where MeetingView keeps its state but is not
    // shown. A hidden section has no offsetParent; bail so Space stays free for
    // whatever surface is actually visible (button activation, scrolling) and we
    // never toggle the hidden player's audio.
    if (!viewRootEl || viewRootEl.offsetParent === null) {
      return;
    }
    if (keyboardEventTargetsControl(event)) {
      return;
    }
    event.preventDefault();
    togglePlayback();
  }

  function buildDisplaySegments(
    index: TranscriptIndex,
    readable: ReadableTranscriptV1 | null,
    display: DisplayTranscriptV1 | null,
  ): DisplaySegment[] {
    if (display) {
      // The whole projection lives in core/transcript.ts: the canonical words a
      // block is judged on and the tokens still allowed to vote on that
      // judgement have to come out of one compatibility pass, and that has to
      // be somewhere a test can reach. See judgedDisplaySegments.
      return judgedDisplaySegments(index, display);
    }

    if (!readable) {
      return index.segments.map((segment) => ({
        id: segment.id,
        speaker: segment.speaker,
        speakerLabel: normalizeSpeakerLabel(segment.speakerLabel),
        startMs: segment.startMs,
        endMs: segment.endMs,
        text: segment.text,
        tokens: [],
        words: segment.words,
        sourceSegmentIds: [segment.id],
      }));
    }

    // Reuse the artifact projection's existing readable-to-source alignment;
    // retain this view's block IDs/extents and canonical acoustic evidence.
    const projected = buildDisplayTranscriptFromArtifacts(index.transcript, readable);
    const canonicalById = new Map(index.segments.map((segment) => [segment.id, segment]));
    return readable.segments.map((segment, segmentIndex) => {
      const sourceSegments = segment.sourceSegmentIds
        .map((segmentId) => canonicalById.get(segmentId))
        .filter((value): value is NonNullable<typeof value> => Boolean(value));
      const words = sourceSegments.flatMap((sourceSegment) => sourceSegment.words);
      const speakerLabel = segment.speaker
        ? normalizeSpeakerLabel(index.speakersById.get(segment.speaker)?.label ?? segment.speaker)
        : normalizeSpeakerLabel(sourceSegments[0]?.speakerLabel ?? "Unknown speaker");
      return {
        id: segment.id,
        speaker: segment.speaker,
        speakerLabel,
        startMs: segment.startMs,
        endMs: segment.endMs,
        text: segment.text,
        tokens: tokensPreserveText(segment.text, projected.blocks[segmentIndex]?.tokens ?? [])
          ? projected.blocks[segmentIndex]!.tokens
          : [],
        words,
        sourceSegmentIds: [...segment.sourceSegmentIds],
      };
    });
  }

  // Tooltip fragment for a low-confidence word. Guarded against non-finite
  // gaps (hosts can mount this published component with unvalidated data):
  // never render "NaN dB" or "Infinity dB" — fall back to the unmeasured
  // wording instead.
  function describeAttributionGap(gapDb: number | undefined): string {
    if (gapDb === undefined || !Number.isFinite(gapDb)) {
      return "much louder";
    }
    return `${gapDb.toFixed(0)} dB louder`;
  }

  // A row whose speaker just held the floor a moment ago does not repeat its
  // own name: the same rule as before, only asked of turns rather than of the
  // fragments a turn arrives in. A monologue the producer split at a two-second
  // pause is two turns, and the second one keeps its timestamp but drops the
  // duplicate header.
  function continuationRowKeys(rows: TranscriptRow<DisplaySegment>[]): Set<string> {
    const keys = new Set<string>();
    for (let index = 1; index < rows.length; index += 1) {
      const previous = rows[index - 1]!;
      const current = rows[index]!;
      if (!current.speaker || previous.speaker !== current.speaker) {
        continue;
      }
      if (current.startMs - previous.endMs <= CONTINUATION_GAP_MS) {
        keys.add(current.key);
      }
    }
    return keys;
  }

  /**
   * Re-derive the highlight, but only when the playhead has actually left the
   * stretch the last answer was good for.
   *
   * `resolvePlayhead` hands back the window its answer holds across, so the
   * common case — a frame landing between two word boundaries — costs one
   * comparison and returns without touching a single reactive source. Measured
   * on a transcript shaped like the largest published meeting, 36 of 480 frames
   * in an eight-second listen cross a boundary at all.
   *
   * The index is rebuilt when `displaySegments` changes, which is exactly when
   * its identity changes: that array is recomputed and never mutated, so the
   * identity test is both sound and complete.
   */
  // Which lines are sounding, recomputed only when the answer can have moved.
  function syncHighlight(segments: DisplaySegment[], timeMs: number): void {
    const rebuilt = playheadIndex.source !== segments;
    if (rebuilt) {
      playheadIndex = buildPlayheadIndex(segments);
    }
    if (
      !rebuilt &&
      timeMs >= highlightValidFromMs &&
      timeMs < highlightValidUntilMs
    ) {
      return;
    }

    const state = resolvePlayhead(playheadIndex, timeMs);
    highlightValidFromMs = state.validFromMs;
    highlightValidUntilMs = state.validUntilMs;

    activeSegments = state.soundingBlocks as DisplaySegment[];
    activeSegmentIds = new Set(activeSegments.map((segment) => segment.id));
  }

  function syncWordHighlight(partsByBlock: Map<string, TranscriptWordPart[]>, timeMs: number, soundingBlockIds: Set<string>): void {
    if (wordHighlightSource !== partsByBlock) {
      wordHighlightSource = partsByBlock;
      wordHighlighter.setWords([...partsByBlock].flatMap(([blockId, parts]) =>
        parts.flatMap((part, order) => part.startMs !== undefined && part.endMs !== undefined
          ? [{ id: part.id, startMs: part.startMs, endMs: part.endMs, blockId, order,
              ambiguous: part.alignment === "interpolated" || part.sourceWordsRejected }]
          : []),
      ));
    }
    // Seeking is allowed for display tokens with rejected source references;
    // playback must still respect D-690's independent acoustic eligibility.
    wordHighlighter.update(timeMs, soundingBlockIds);
  }

  // The blocks a row renders as its own speaker's prose — the turn's own
  // fragments, not the chips nested in it. What rings when the turn is
  // sounding, and what the crosstalk warning is judged on.
  function rowSpeechBlocks(row: TranscriptRow<DisplaySegment>): DisplaySegment[] {
    return row.members
      .filter((member): member is Extract<typeof member, { kind: "speech" }> => member.kind === "speech")
      .map((member) => member.block);
  }

  // The ring says "this speaker is sounding now". It is judged on the turn's
  // OWN blocks: a backchannel inside the turn is sounding on its own account
  // and lights its own chip, and must not claim the host was still talking.
  function isRowSounding(row: TranscriptRow<DisplaySegment>, sounding: Set<string>): boolean {
    return rowSpeechBlocks(row).some((block) => sounding.has(block.id));
  }

  function isRowLikelyCrosstalk(row: TranscriptRow<DisplaySegment>): boolean {
    return isLikelyCrosstalkAcrossBlocks(rowSpeechBlocks(row));
  }

  function likelyCrosstalkTitle(speakerLabel: string): string {
    return `Another participant's microphone was much louder here. This is probably their voice bleeding into ${speakerLabel}'s track, not ${speakerLabel} speaking.`;
  }

  // "over Chris", "over Chris and Dana" - names, never durations. The measured
  // simultaneity stays in the model; a reader can act on who, not on how long.
  function formatSpeakerList(labels: readonly string[]): string {
    if (labels.length <= 1) {
      return labels[0] ?? "";
    }
    return `${labels.slice(0, -1).join(", ")} and ${labels.at(-1)}`;
  }

  function formatArtifactMode(): string {
    if (!transcriptIndex) {
      return hasCatalog ? "Runtime catalog" : "Viewer ready";
    }
    if (displayTranscript) {
      return "Cleaned display transcript";
    }
    if (readableTranscript) {
      return "Readable transcript";
    }
    return "Canonical transcript";
  }

  function describeTranscriptInteraction(): string {
    if (!meeting && hasCatalog) {
      return "Choose a meeting from the library to load its audio, transcript, and timing data.";
    }
    if (!transcriptIndex) {
      return "Load a meeting artifact to inspect its transcript and timing.";
    }
    return "Select a word to seek to it.";
  }

  function formatMetadataLabel(label: string): string {
    return label
      .split(".")
      .map((part) =>
        part
          .replace(/[_-]+/g, " ")
          .replace(/([a-z])([A-Z])/g, "$1 $2")
          .replace(/\s+/g, " ")
          .trim()
          .replace(/\b\w/g, (match) => match.toUpperCase()),
      )
      .join(" / ");
  }

  function metadataSectionStartsOpen(_title: string): boolean {
    return false;
  }

  function metadataRowKey(sectionTitle: string, row: ArtifactMetadataRow): string {
    return `${sectionTitle}:${row.label}`;
  }

  onMount(() => {
    window.addEventListener("keydown", handleWindowKeydown);
    if (bundled) {
      void loadBundled();
    }
  });

  onDestroy(() => {
    window.removeEventListener("keydown", handleWindowKeydown);
    stopPlaybackClock();
    void marks.close();
  });

  // Reactive `bundled`: the shell can flip this at any time — an embedded
  // viewer that fell back to the bundled artifact and then recovered its
  // catalog is exactly that transition. Without a handler here, a failed
  // bundled load left errorMessage set and NEITHER branch below could clear it
  // (the first needs a meeting, the second needs a non-null attemptedKey, and a
  // bundled load sets neither) — so the "Meeting not found" card survived the
  // recovery and stayed on screen until the user clicked something (D-543).
  //
  // This lives in the component rather than only in the shell because
  // MeetingView is a published entry point ("./MeetingView.svelte") — any host
  // that toggles `bundled` reproduces the same stuck card.
  $: if (bundled !== lastBundled) {
    const wasBundled = lastBundled;
    lastBundled = bundled;
    if (bundled) {
      void loadBundled();
    } else if (wasBundled) {
      resetLoadedArtifact();
      errorMessage = "";
    }
  }

  // Reactive load: when the shell selects a different meeting, load it. Guarded
  // by attemptedKey so catalog enrichment (which changes the meeting object
  // identity but not its id) and load failures do not retrigger. Deselecting
  // (meeting → null) clears the loaded artifact.
  $: if (!bundled && meeting && meeting.id !== attemptedKey) {
    attemptedKey = meeting.id;
    void loadForMeeting(meeting);
  } else if (!bundled && !meeting && attemptedKey !== null) {
    attemptedKey = null;
    resetLoadedArtifact();
    errorMessage = "";
  }

  $: marksFor = loadAnnotations ? (meeting?.id ?? "") : null;
  $: if (marksFor !== openedMarksFor) {
    openedMarksFor = marksFor;
    void marks.open(loadAnnotations, applyAnnotations);
  }

  $: summaryHtml = renderSummaryHtml(summaryMarkdown);
  $: speakers = transcriptIndex?.transcript.speakers ?? [];
  // Reading order, then EFFECTIVE timings, then overlap. The order matters:
  // the producer appends wordless segments last so the array cannot be trusted
  // to be sorted, and the overlap analysis has to see repaired spans or it
  // spends most of its time labelling decoder-fabricated silence as crosstalk
  // (D-690). repairTurnFinalWordInflation copies rather than mutates, so the
  // loaded artifact keeps its canonical times and every word keeps its original
  // START — seek targets never move.
  let transcriptQuery = "";
  let onlyMatching = false;

  $: displaySegments = transcriptIndex
    ? sortBlocksInReadingOrder(
        repairTurnFinalWordInflation(
          buildDisplaySegments(transcriptIndex, readableTranscript, displayTranscript),
          { endsBoundedByAudio: wordEndsBoundedByAudio },
        ),
      )
    : [];
  // The seam this was always for. The filter lives in core/transcript.ts
  // because the interesting half is the mapping from matched canonical segments
  // to rendered blocks, and that deserves a test that does not need a DOM.
  // Hiding what does not match is opt-in; find itself only highlights.
  $: visibleSegments = onlyMatching
    ? filterDisplaySegmentsByQuery(transcriptIndex, displaySegments, transcriptQuery)
    : displaySegments;
  // Rows are TURNS, not blocks: the producer flushes a segment at every speaker
  // change, so one sentence spoken over somebody else arrives as a dozen
  // fragments and only the turn they came from is worth reading (D-693).
  $: transcriptRows = buildTranscriptRows(visibleSegments);
  // Keyed by block id and built from EVERY block, not the filtered ones: it is
  // a lookup, so covering blocks that are currently hidden costs a few map
  // entries, while missing one a row still renders would drop its words (D-734).
  $: wordPartsByBlock = new Map(displaySegments.map((block) => [block.id, transcriptWordParts(block)]));
  $: timedWords = wordsByTime([...wordPartsByBlock.values()].flat());
  $: findStopList = findStops(transcriptIndex, transcriptRows, wordPartsByBlock, transcriptQuery);
  $: activeFollowRowKey = followRowKeyForBlocks(transcriptRows, activeSegments);
  $: continuationKeys = continuationRowKeys(transcriptRows);
  // Highlight membership runs on the same effective audible spans the overlap
  // analysis judges on, not on paragraph extents: extents ring both speakers
  // (and follow-scroll the earlier paragraph) through stretches where their
  // words strictly alternate. Wordless blocks keep their extent.
  //
  // PUSHED ON A BOUNDARY, NOT DERIVED ON A FRAME (D-692). These five were `$:`
  // over `currentTimeMs`, so each of the sixty playhead writes a second rebuilt
  // them — and because two of them are a fresh Map and a fresh Set, and legacy
  // reactivity compares objects by identity, every consumer of the playhead was
  // invalidated each time. On the largest published meeting that is 7,318
  // word-button effects marked, scheduled and dirty-checked sixty times a
  // second to discover that one or two of them changed; the profile was
  // accordingly 93% busy, four fifths of it Svelte bookkeeping. `syncHighlight`
  // keeps them assigned only when the answer actually moves — which, since a
  // word lasts a few hundred milliseconds, is a few times a second rather than
  // sixty. The work is not made cheaper; it is not entered.
  $: syncHighlight(displaySegments, currentTimeMs);
  $: syncWordHighlight(wordPartsByBlock, currentTimeMs, activeSegmentIds);
  $: hasPrecomputedDisplay = displayTranscript !== null;
  $: metadataSections = artifactMetadata?.sections ?? [];
  $: safeDurationMs = asFiniteMilliseconds(durationMs);
  $: clampedDurationMs = Math.max(0, safeDurationMs);
  $: clampedCurrentTimeMs = Math.min(Math.max(0, asFiniteMilliseconds(currentTimeMs)), clampedDurationMs || 0);
  $: remainingMs = Math.max(0, clampedDurationMs - clampedCurrentTimeMs);
  $: speakerNames = speakers.map((s) => s.label || s.id).filter(Boolean);
  $: if (
    followPlayback &&
    !manualScrollLock &&
    activeFollowRowKey &&
    activeFollowRowKey !== lastAutoScrollRowKey
  ) {
    lastAutoScrollRowKey = activeFollowRowKey;
    void scrollSegmentIntoView(activeFollowRowKey, "smooth");
  }
</script>

<section
  bind:this={viewRootEl}
  aria-label="Meeting view"
  class="meeting-viewer relative flex flex-col h-full min-h-0 min-w-0"
>
  <!-- Scroll container: holds the sticky header and all main content.
       Bottom padding keeps the last lines of the transcript reachable
       even when the (absolutely positioned) player overlaps the scroll.
       `scrollbar-gutter: stable` reserves the scrollbar gutter persistently
       so content width never shifts as scrollbar appears/disappears. -->
  <div bind:clientHeight={scrollHeight} class="flex-1 min-h-0 overflow-y-auto overflow-x-hidden overscroll-contain pb-40 min-[981px]:pb-32 scroll-stable flex flex-col">
    <!-- Sticky header — the meeting's identity, and the transcript flows under
         it. It used to be a strip of status badges with the title in a second,
         SCROLLING header below, so the one thing that says which meeting you
         are reading left the screen as soon as you started reading it. Title
         and the facts that identify a meeting are here now; the badges and the
         transcript switcher keep their place at the right, where they were.
         Opaque rather than translucent: this panel sits over the browse list,
         and a blurred header with a meeting list showing through it reads as
         two pages at once. -->
    <header class="sticky top-0 z-20 flex-none min-h-12 px-4 py-3 min-[981px]:px-8 bg-base-200 border-b border-base-300" bind:offsetHeight={headerHeight}>
    <div class="flex items-center gap-2 min-w-0">
    {#if !isDesktop && !inSheet}
      <button
        on:click={() => dispatch("back")}
        class="btn btn-square btn-neutral btn-xs flex-none"
        type="button"
        aria-label="Back to meeting list"
      >
        <ArrowLeft size={18} aria-hidden="true" />
      </button>
    {/if}

    <h1 class="flex-1 min-w-0 truncate text-lg font-bold min-[981px]:text-xl">
      {meeting ? meeting.title : "Meeting transcript viewer"}
    </h1>

    <!-- Status info: artifact mode, transcript switcher, timing precision. -->
    <div class="flex flex-none items-center gap-1 text-base-content/70">
      <span class="badge badge-xs badge-outline px-1">
        {formatArtifactMode()}
      </span>
      {#if transcriptSwitchError}
        <button
          type="button"
          class="badge badge-xs badge-error gap-1 px-1"
          title={transcriptSwitchError}
          aria-label={`Dismiss switch error: ${transcriptSwitchError}`}
          on:click={dismissTranscriptSwitchError}
        >
          <span>Switch failed</span>
          <span aria-hidden="true">×</span>
        </button>
      {/if}
      {#if transcriptIndex && availableTranscripts.length > 1}
        <div
          class="join"
          role="group"
          aria-label="Choose transcript"
        >
          {#each availableTranscripts as descriptor (descriptor.id)}
            <button
              type="button"
              class="join-item btn btn-xs"
              class:btn-primary={descriptor.id === currentTranscriptId}
              class:btn-ghost={descriptor.id !== currentTranscriptId}
              disabled={transcriptSwitchPending && descriptor.id !== currentTranscriptId}
              aria-pressed={descriptor.id === currentTranscriptId}
              title={[
                descriptor.label,
                descriptor.description,
                descriptor.isDefault ? "(producer default)" : "",
              ].filter(Boolean).join(" — ")}
              on:click={() => void handleTranscriptSwitch(descriptor.id)}
            >
              {descriptor.label}
            </button>
          {/each}
        </div>
      {/if}
    </div>

    <button
      type="button"
      class="btn btn-ghost btn-xs btn-square flex-none"
      on:click={openShortcutsDialog}
      aria-label="Keyboard shortcuts"
      title="Keyboard shortcuts"
    >
      <Keyboard size={14} aria-hidden="true" />
    </button>

    {#if inSheet}
      <button
        type="button"
        class="btn btn-ghost btn-xs btn-square flex-none"
        on:click={() => dispatch("back")}
        aria-label="Close the meeting"
        title="Close (Esc)"
      >
        <X size={16} aria-hidden="true" />
      </button>
    {/if}
    </div>

    <!-- The facts that identify a meeting, on one line under its name. Each is
         rendered only where it is known: the room and the date come from the
         catalog and are there before anything loads, the duration and the
         speakers come out of the artifact and arrive with it. -->
    {#if meeting}
      <div class="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-base-content/70">
        <span class="inline-flex items-center gap-1.5">
          <MessageSquare size={14} aria-hidden="true" />
          {roomLabelOf(meeting)}
        </span>
        <span class="inline-flex items-center gap-1.5">
          <Calendar size={14} aria-hidden="true" />
          {formatMeetingDate(meeting.dateLabel)}
        </span>
        {#if transcriptIndex && clampedDurationMs > 0}
          <span class="inline-flex items-center gap-1.5 tabular-nums">
            <Clock size={14} aria-hidden="true" />
            {formatClockTime(clampedDurationMs)}
          </span>
        {/if}
        {#if speakerNames.length > 0}
          <span class="inline-flex flex-wrap items-center gap-1.5">
            <Users size={14} aria-hidden="true" />
            {#each speakerNames as name}
              <span class="badge badge-sm px-1">{name}</span>
            {/each}
          </span>
        {/if}
      </div>
      <MeetingTags session={marks} vocabulary={tagVocabulary} />
    {/if}
  </header>

  {#if !transcriptIndex && (errorMessage || notFoundMessage)}
    <div class="grid place-items-center flex-1 p-4">
      <div class="card bg-base-100 w-full max-w-md border border-base-300">
        <div class="card-body items-center text-center">
          <h2 class="text-lg font-bold">Meeting not found</h2>
          <p class="text-base-content">{errorMessage || notFoundMessage}</p>
        </div>
      </div>
    </div>
  {:else if transcriptIndex}
  <div out:fade={contentFadeConfig()}>
  <main class="flex flex-col gap-3.5 m-4 min-[981px]:m-8">
    {#if summaryHtml}
      <section class="flex flex-col gap-3.5 mb-4">
        <div class="pb-1">
          <p class="text-xl text-base-content font-semibold flex gap-2 items-center">Summary</p>
        </div>
        <!-- Markdown rendered via {@html} can't receive Svelte-scoped
             styles, so per-tag styling is expressed through Tailwind's
             arbitrary descendant selectors on the wrapper. -->
        <div
          class="text-base leading-relaxed text-base-content p-4 mb-8 bg-base-100/50 border-l-4 border-primary
            [&>*+*]:mt-3.5
            [&>h1:first-child]:mt-0 [&>h2:first-child]:mt-0 [&>h3:first-child]:mt-0 [&>h4:first-child]:mt-0
            [&_h1]:text-xl [&_h1]:font-semibold [&_h1]:mt-6 [&_h1]:leading-tight
            [&_h2]:text-xl [&_h2]:font-semibold [&_h2]:mt-5 [&_h2]:leading-tight
            [&_h3]:text-lg [&_h3]:font-semibold [&_h3]:mt-4 [&_h3]:leading-tight
            [&_h4]:text-base [&_h4]:font-semibold [&_h4]:mt-4
            [&_strong]:font-semibold [&_em]:italic
            [&_a]:text-primary [&_a]:underline [&_a]:underline-offset-2 [&_a:hover]:decoration-2
            [&_ul]:pl-6 [&_ul]:grid [&_ul]:gap-1.5 [&_ul]:list-disc
            [&_ol]:pl-6 [&_ol]:grid [&_ol]:gap-1.5 [&_ol]:list-decimal
            [&_li]:marker:text-base-content/55
            [&_code]:font-mono [&_code]:text-[0.875em] [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:rounded [&_code]:bg-base-300
            [&_pre]:font-mono [&_pre]:text-sm [&_pre]:p-3 [&_pre]:rounded-lg [&_pre]:bg-base-300 [&_pre]:overflow-x-auto
            [&_pre_code]:p-0 [&_pre_code]:bg-transparent [&_pre_code]:text-[1em]
            [&_blockquote]:border-l-[3px] [&_blockquote]:border-primary/60 [&_blockquote]:pl-3.5 [&_blockquote]:py-0.5 [&_blockquote]:text-base-content/80
            [&_hr]:border-0 [&_hr]:border-t [&_hr]:border-base-300"
        >{@html summaryHtml}</div>
      </section>
    {/if}

    <!-- Which insights read this meeting (D-721). Here, under the summary and
         above the transcript, because it is a fact about the meeting of the
         same kind as its summary — what came OUT of this conversation — and a
         reader who has to scroll past the whole transcript to find it will
         never find it. Rendered beside the recording rather than inside it:
         what a meeting was used for is not part of what was recorded, which is
         why the record comes from the shell rather than the artifact. -->
    {#if linkedInsights.length > 0}
      <!-- Titled inside like the summary above it, and in the secondary — this
           theme's amber, the colour every insight surface uses — so the two
           model-written blocks are visibly different kinds of thing. -->
      <section
        class="mb-4 flex flex-col gap-1 rounded-box border border-secondary/25 bg-secondary/10 p-3"
      >
        <p class="text-[10px] font-semibold tracking-[0.1em] uppercase text-secondary">
          Insights
        </p>
        {#each linkedInsights as record (record.id)}
          <button
            type="button"
            class="flex w-full items-baseline gap-2 rounded-field px-2 py-1.5 text-left cursor-pointer hover:bg-base-100/60"
            on:click={() => dispatch("openInsight", record)}
          >
            <FileText size={14} class="shrink-0 self-center" aria-hidden="true" />
            <span class="min-w-0 flex-1 truncate text-sm font-medium">
              {insightHeadline(record)}
            </span>
            <span class="shrink-0 text-xs tabular-nums text-base-content/60">
              {formatInsightCreated(record)}
              {#if (insightSourceCounts.get(record.id) ?? 0) > 0}
                &middot; Context from {insightSourceCounts.get(record.id)}
                {insightSourceCounts.get(record.id) === 1 ? "meeting" : "meetings"}
              {/if}
            </span>
          </button>
        {/each}
      </section>
    {/if}

    <div class="flex justify-between items-start gap-4 pb-1.5 border-b border-base-300">
      <div>
        <p class="text-xl font-semibold text-base-content">Transcript</p>
        <p class="text-xs text-base-content/70 leading-normal">{describeTranscriptInteraction()}</p>
      </div>
    </div>

    {#if displaySegments.length === 0}
      <p class="text-base-content/70 text-sm leading-normal">No transcript loaded yet.</p>
    {:else}
      <TranscriptFrame
        session={marks}
        vocabulary={tagVocabulary}
        words={timedWords}
        rows={transcriptRows}
        stops={findStopList}
        bind:query={transcriptQuery}
        bind:onlyMatching
        durationMs={clampedDurationMs}
        playheadMs={clampedCurrentTimeMs}
        seek={seekTo}
        stickTop={headerHeight}
        viewHeight={scrollHeight - headerHeight - playerHeight}
      >
      {#if visibleSegments.length === 0}
      <!-- Distinct from the line above on purpose: "no transcript" and "nothing
           matched what you typed" are different facts, and the first one read as
           an answer to a search would say the meeting has no words in it. -->
      <p class="text-base-content/70 text-sm leading-normal">
        Nothing in this transcript matches “{transcriptQuery.trim()}”.
      </p>
      {:else}
      <div
        bind:this={transcriptPane}
        aria-label="Transcript"
        class="grid gap-3"
        on:touchmove={() => (manualScrollLock = true)}
        on:wheel={() => (manualScrollLock = true)}
        role="log"
      >
        <!-- One row per TURN, and a turn is ONE PARAGRAPH. The producer
             flushes a segment at every speaker change, so a sentence spoken
             over somebody else arrives here as a dozen one- to three-word
             fragments; buildTranscriptRows puts them back into the turn they
             were and this loop sets them as continuous prose. NOTHING IS
             MERGED: every block keeps its own id, its own words, its own seek
             anchors and its own scroll target - only the paragraph breaks and
             the repeated headers between them are gone. A short remark said
             inside a turn is a chip where it happened rather than a row of its
             own, and two people who genuinely held the floor at once are named
             on each other's rows ("over Chris"). No durations anywhere: the
             model keeps the measurement, the page shows who. -->
        {#snippet blockProse(block: DisplaySegment)}<TranscriptWords
              parts={wordPartsByBlock.get(block.id) ?? []}
              speakerLabel={block.speakerLabel}
              highlighter={wordHighlighter}
              seek={seekTo}
            />{/snippet}
        {#each transcriptRows as row (row.key)}
          <article
            aria-current={isRowSounding(row, activeSegmentIds) ? "true" : undefined}
            class="transition-all {continuationKeys.has(row.key) ? '-mt-1' : ''} {isRowSounding(
              row,
              activeSegmentIds,
            )
              ? 'ring-2 ring-primary ring-offset-6 ring-offset-base-100 bg-base-100 rounded-sm'
              : ''}"
          >
            <!-- Header row: speaker name + timestamp, both aligned left.
                 flex-wrap so the simultaneity marker drops to its own line on a
                 narrow screen instead of squeezing the speaker name. -->
            <div class="flex flex-wrap items-center gap-1 mb-1">
              {#if !continuationKeys.has(row.key)}
                <span class="badge badge-md badge-info text-sm px-1 font-bold">{row.speakerLabel}</span>
              {/if}
              {#if isRowLikelyCrosstalk(row)}
                <span
                  class="badge badge-md badge-warning badge-outline text-sm px-1"
                  title={likelyCrosstalkTitle(row.speakerLabel)}
                >probably crosstalk</span>
              {/if}
              <button
                class="badge badge-md text-sm bg-base-200 px-1 text-base-content/60 hover:bg-primary/60 hover:text-base-content cursor-pointer tabular-nums"
                on:click={() => seekTo(row.startMs)}
                type="button"
              >
                {formatClockTime(row.startMs)}
              </button>
              <!-- Simultaneity, quietly: WHO this turn ran over, and nothing
                   else. Static per transcript (it never depends on the
                   playhead), so it does not churn the role="log" live region. -->
              {#if row.over.length > 0}
                <span
                  class="text-sm text-base-content/55 min-w-0"
                  title="Simultaneous speech: {formatSpeakerList(row.over)} {row.over.length > 1
                    ? 'were'
                    : 'was'} speaking at the same time."
                ><span class="sr-only">Simultaneous speech: </span>over {formatSpeakerList(row.over)}</span>
              {/if}
            </div>

            <!-- The turn, as one paragraph. Members are laid end to end with a
                 single space between them: the fragments the producer cut are
                 sentences again, and an interjection sits inline at the seam it
                 was said in. A chip's copy text reads "(Ben: Right.)" - the
                 parens and the name are real text nodes, the screen-reader
                 prefix is `select-none` so it never lands in the clipboard. -->
            <p class="px-1.5 text-[1.06rem] leading-[1.72] text-base-content break-words">{#each row.members as member, memberIndex (member.key)}{#if memberIndex > 0}{' '}{/if}{#if member.kind === 'speech'}<span
                  id={segmentDomId(member.block.id)}
                >{@render blockProse(member.block)}</span>{:else}<span
                   class="box-decoration-clone rounded-md border bg-base-200/60 px-1.5 py-0.5 text-[0.94rem] text-base-content/60 {isLikelyCrosstalkAcrossBlocks(
                     member.blocks,
                   )
                     ? 'border-warning'
                     : 'border-base-300'}"
                   title={isLikelyCrosstalkAcrossBlocks(member.blocks)
                     ? likelyCrosstalkTitle(member.speakerLabel)
                     : undefined}
                 >{#if isLikelyCrosstalkAcrossBlocks(member.blocks)}<span class="sr-only select-none">Probably crosstalk. </span>{/if}<span class="sr-only select-none">Interjection by </span><span aria-hidden="true">(</span><span
                    class="font-semibold">{member.speakerLabel}</span><span aria-hidden="true">:</span>{#each member.blocks as chipBlock (chipBlock.id)}{' '}<span
                      class="rounded {activeSegmentIds.has(chipBlock.id) ? 'bg-primary/25' : ''}"
                      id={segmentDomId(chipBlock.id)}
                    >{@render blockProse(chipBlock)}</span>{/each}<span aria-hidden="true">)</span></span>{/if}{/each}</p>
          </article>
        {/each}
      </div>
      {/if}
      </TranscriptFrame>

      {#if visibleSegments.length > 0 && transcriptIndex && (timingPrecision || artifactMetadata)}
        <section class="grid gap-3 mt-8">
          <div class="border-b border-base-300 pb-3">
            <p class="text-lg font-medium text-base-content">
              Meeting metadata
            </p>
            <p class="text-base-content/70 leading-normal text-xs">
              Artifact metadata is shown as provided, so older files can remain usable with reduced timing precision.
            </p>
          </div>

          {#if artifactMetadata}
            <div class="grid gap-2.5">
              {#each metadataSections as section}
                <details
                  class="collapse collapse-arrow bg-base-100 border border-base-300"
                  open={metadataSectionStartsOpen(section.title)}
                >
                  <summary class="collapse-title text-base font-medium">{section.title}</summary>
                  <div class="collapse-content">
                    <dl
                      class="grid grid-cols-[minmax(10rem,16rem)_minmax(0,1fr)] gap-y-2 gap-x-3.5 m-0 max-[980px]:grid-cols-1"
                    >
                      {#each section.rows as row (metadataRowKey(section.title, row))}
                        <dt class="m-0 text-base-content/70 text-sm leading-snug">
                          {formatMetadataLabel(row.label)}
                        </dt>
                        <dd class="m-0 text-base-content text-sm leading-normal break-words">
                          {#if row.values && row.values.length > 0}
                            <div class="flex flex-wrap gap-1.5">
                              {#each row.values as value}
                                <span class="badge badge-outline">{value}</span>
                              {/each}
                            </div>
                          {:else if row.tone === "code"}
                            <code
                              class="inline-block px-1.5 py-0.5 rounded bg-base-300 text-base-content text-xs font-mono"
                              >{row.value}</code
                            >
                          {:else}
                            {row.value}
                          {/if}
                        </dd>
                      {/each}
                      {#if section.title === "Meeting" && timingPrecision}
                        <dt class="m-0 text-base-content/70 text-sm leading-snug">
                          Timing precision
                        </dt>
                        <dd class="m-0 text-base-content text-sm leading-normal break-words">
                          <span
                            class:text-warning={timingPrecision.level !== "word"}
                            title={timingPrecision.detail}
                          >
                            {timingPrecision.label}
                          </span>
                          <p class="text-base-content/70 text-xs mt-1.5 leading-snug">
                            {timingPrecision.detail}
                          </p>
                        </dd>
                      {/if}
                    </dl>
                  </div>
                </details>
              {/each}
              <details class="collapse collapse-arrow bg-base-100 border border-base-300">
                <summary class="collapse-title text-base font-medium">Raw JSON</summary>
                <div class="collapse-content">
                  <pre
                    class="m-0 text-base-content text-xs leading-relaxed whitespace-pre-wrap break-words font-mono">{artifactMetadata.rawJson}</pre>
                </div>
              </details>
            </div>
          {/if}
        </section>
      {/if}
    {/if}
  </main>
  </div>
  {/if}
  </div>

  {#if loading && meeting}
    <div
      class="absolute inset-0 z-10 grid place-items-center bg-base-200 pointer-events-none"
      out:fade={contentFadeConfig()}
    >
      <div class="flex flex-col items-center gap-3 text-base-content">
        <span class="cassini-spinner text-primary" aria-hidden="true"></span>
        <p class="text-base">Loading meeting…</p>
      </div>
    </div>
  {:else if !transcriptIndex && !meeting && !bundled && !notFoundMessage && isDesktop}
    <div class="absolute inset-0 z-10 grid place-items-center pointer-events-none">
      <div class="flex flex-col items-center gap-2 text-base-content">
        <CassetteTape size={28} strokeWidth={1.5} aria-hidden="true" />
        <p class="text-base">Select a meeting to view</p>
      </div>
    </div>
  {/if}

  {#if transcriptIndex && audioSrc}
  <!-- right-[15px] matches the scrollbar gutter on the sibling scroll
       container so the player aligns with transcript content's right edge. -->
  <footer
    bind:offsetHeight={playerHeight}
    class="absolute bottom-0 left-0 right-0 min-[981px]:right-[15px] z-30 p-2 min-[981px]:px-4 min-[981px]:pb-4 pointer-events-none [will-change:opacity]"
    transition:fade={playerFadeConfig()}
  >
    <div class="card bg-base-100 shadow-2xl p-2 border border-base-300 pointer-events-auto relative">
      {#if audioSrc}
        {#key audioSrc}
          <audio
            bind:this={audioEl}
            class="sr-only"
            preload="metadata"
            src={audioSrc}
            on:durationchange={handleDurationChange}
            on:ended={handlePause}
            on:loadedmetadata={handleLoadedMetadata}
            on:pause={handlePause}
            on:play={handlePlay}
            on:timeupdate={handleTimeUpdate}
          >
            {#if captionsSrc}
              <track kind="captions" src={captionsSrc} label="Captions" default />
            {/if}
            {#if chaptersSrc}
              <track kind="chapters" src={chaptersSrc} label="Chapters" />
            {/if}
          </audio>
        {/key}

        <!--
          Mobile (<981px): 2-col 2-row grid.
            Row 1: [================ scrub + labels ================]
            Row 2: [play]                                 [auto-scroll + exact-words]
          Desktop (≥981px): 3-col 1-row grid.
            [play] [scrub + labels] [auto-scroll + exact-words]
        -->
        <div
          class="grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-2 gap-y-2 min-[981px]:grid-cols-[auto_minmax(0,1fr)_auto] min-[981px]:gap-x-3.5 min-[981px]:gap-y-0"
        >
          <button
            class="row-start-2 col-start-1 min-[981px]:row-start-1 btn btn-primary btn-sm btn-square min-[981px]:btn-md"
            on:click={togglePlayback}
            type="button"
            aria-label={playing ? "Pause" : "Play"}
          >
            {#if playing}
              <Pause size={20} strokeWidth={0} fill="currentColor" aria-hidden="true" />
            {:else}
              <Play size={20} strokeWidth={0} fill="currentColor" aria-hidden="true" />
            {/if}
          </button>

          <div
            class="row-start-2 col-start-2 min-[981px]:row-start-1 min-[981px]:col-start-3 flex items-center justify-end flex-wrap gap-1.5"
          >
            <label class="flex items-center gap-1.5 h-9 px-2 bg-primary/20 border border-primary/50 rounded-lg cursor-pointer min-[981px]:h-10">
              <span class="whitespace-nowrap text-xs min-[981px]:text-sm">Auto-scroll</span>
              <input
                type="checkbox"
                class="toggle toggle-primary toggle-xs min-[981px]:toggle-sm"
                aria-label="Toggle transcript auto-scroll"
                checked={followPlayback && !manualScrollLock}
                on:change={toggleFollowPlayback}
              />
            </label>
          </div>

          <div
            class="row-start-1 col-span-2 min-[981px]:col-start-2 min-[981px]:col-span-1 grid gap-0.5 min-w-0"
          >
            <input
              aria-label="Seek within meeting"
              class="range range-primary range-sm w-full"
              max={Math.max(clampedDurationMs, 1)}
              min="0"
              on:input={handleTimelineInput}
              step="250"
              type="range"
              value={Math.min(clampedCurrentTimeMs, Math.max(clampedDurationMs, 1))}
            />
            <div class="flex justify-between gap-3 text-base-content/70 text-xs tabular-nums">
              <span>{formatClockTime(clampedCurrentTimeMs)} elapsed</span>
              <span>{formatClockTime(clampedDurationMs)} total</span>
              <span>-{formatClockTime(remainingMs)} remaining</span>
            </div>
          </div>
        </div>
      {/if}
    </div>
  </footer>
  {/if}

  {#if followPlayback && manualScrollLock}
    <button
      type="button"
      class="absolute top-16 right-4 z-30 badge badge-neutral gap-1 px-2 pb-1 cursor-pointer shadow-md rounded-md"
      on:click={toggleFollowPlayback}
      aria-label="Resume auto-scroll"
    >
    Auto-scroll paused
    </button>
  {/if}
</section>

<dialog bind:this={shortcutsDialog} class="modal">
  <div class="modal-box p-4">
    <h3 class="font-bold text-lg mb-3">Keyboard shortcuts</h3>
    <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 items-baseline">
      <dt><kbd class="kbd kbd-sm">Space</kbd></dt>
      <dd class="text-base-content/80">Play / pause audio</dd>
      <dt><kbd class="kbd kbd-sm">Ctrl</kbd> <kbd class="kbd kbd-sm">F</kbd></dt>
      <dd class="text-base-content/80">Find in this meeting; Enter and Shift+Enter step through</dd>
      {#if $marks.status === "ready"}
        <dt><kbd class="kbd kbd-sm">Enter</kbd> / <kbd class="kbd kbd-sm">Esc</kbd></dt>
        <dd class="text-base-content/80">Tag the selected stretch / clear it</dd>
        <dt><kbd class="kbd kbd-sm">←</kbd> <kbd class="kbd kbd-sm">→</kbd></dt>
        <dd class="text-base-content/80">Move a stretch's marker a word; with Shift, to the next pause</dd>
      {/if}
    </dl>
    <div class="modal-action">
      <form method="dialog">
        <button class="btn btn-sm" type="submit">Close</button>
      </form>
    </div>
  </div>
  <!-- Click backdrop to close -->
  <form method="dialog" class="modal-backdrop">
    <button type="submit">close</button>
  </form>
</dialog>
