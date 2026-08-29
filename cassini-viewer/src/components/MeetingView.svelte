<script lang="ts">
  import { createEventDispatcher, onDestroy, onMount, tick } from "svelte";
  import { fade, fly } from "svelte/transition";
  import { cubicOut } from "svelte/easing";
  import { marked } from "marked";
  import DOMPurify from "dompurify";
  import { Play, Pause, Keyboard, Calendar, Clock, Users, ArrowLeft, CassetteTape } from "@lucide/svelte";
  import { formatClockTime, parseTimeHash } from "../core/transcript";
  import {
    getActiveTimedRange,
    getLatestStartingActiveTimedRange,
  } from "../core/timing";
  import type {
    DisplayTranscriptToken,
    DisplayTranscriptV1,
    IndexedWord,
    ReadableTranscriptV1,
    TranscriptIndex,
  } from "../core/types";
  import type {
    ArtifactMetadata,
    ArtifactMetadataRow,
    ArtifactTimingPrecision,
    LoadedArtifact,
  } from "../viewer/loadArtifact";
  import type { PortableTranscriptDescriptor } from "../viewer/portable";
  import { formatMeetingDate, type MeetingCatalogEntry } from "../viewer/catalog";
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
  export let isDesktop = false;
  export let prefersReducedMotion = false;
  // The region fly params are computed by the shell (they depend on
  // reduced-motion / viewport state the shell owns) and passed through.
  export let flyParams: Parameters<typeof fly>[1] = { duration: 0 };
  // hasCatalog drives the "choose a meeting" empty-state copy (a list exists to
  // choose from) vs the bare viewer-ready copy.
  export let hasCatalog = false;
  // Shell-level "meeting id not in catalog" error (deep-link to a removed
  // meeting). Rendered in the same not-found card as an internal load failure.
  export let notFoundMessage = "";

  const dispatch = createEventDispatcher<{
    back: void;
    enriched: MeetingCatalogEntry;
  }>();

  interface DisplaySegment {
    id: string;
    speaker?: string;
    speakerLabel: string;
    startMs: number;
    endMs: number;
    text: string;
    tokens: DisplayTranscriptToken[];
    words: IndexedWord[];
    sourceSegmentIds: string[];
  }

  const CONTINUATION_GAP_MS = 60_000;

  /**
   * A turn whose words the producer flagged as probable crosstalk. Cassini
   * records, per word, how far the loudest other microphone sat above its own
   * noise floor compared with this speaker's; a large gap means somebody else
   * was talking and this track only picked them up. Such a turn is usually not
   * a real interjection at all, so it is marked rather than hidden — the reader
   * can still hear the audio and decide.
   */
  function lowConfidenceWordCount(segment: DisplaySegment): number {
    return segment.words.reduce(
      (count, word) => (word.lowConfidenceSpeaker ? count + 1 : count),
      0,
    );
  }

  function isLikelyCrosstalkTurn(segment: DisplaySegment): boolean {
    const flagged = lowConfidenceWordCount(segment);
    // Every word, and there is a word: a turn built entirely from audio the
    // evidence attributes to somebody else.
    return flagged > 0 && flagged === segment.words.length;
  }

  let transcriptIndex: TranscriptIndex | null = null;
  let displayTranscript: DisplayTranscriptV1 | null = null;
  let readableTranscript: ReadableTranscriptV1 | null = null;
  let summaryMarkdown: string | null = null;
  let audioSrc = "";
  let captionsSrc: string | null = null;
  let chaptersSrc: string | null = null;
  let timingPrecision: ArtifactTimingPrecision | null = null;
  let artifactMetadata: ArtifactMetadata | null = null;
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
  let durationMs = 0;
  let playing = false;
  let followPlayback = true;
  let manualScrollLock = false;
  let pendingSeekMs: number | null = null;

  let transcriptPane: HTMLElement | null = null;
  let audioEl: HTMLAudioElement | null = null;
  let animationFrameId = 0;
  let lastAutoScrollSegmentId = "";
  let showExactWords = false;

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
    availableTranscripts = artifact.availableTranscripts;
    currentTranscriptId = artifact.currentTranscriptId;
    defaultTranscriptId =
      artifact.availableTranscripts.find((entry) => entry.isDefault)?.id ?? artifact.currentTranscriptId;
    durationMs = artifact.index.transcript.media.durationMs;
    errorMessage = "";
    showExactWords = false;
    manualScrollLock = false;
    lastAutoScrollSegmentId = "";
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
    availableTranscripts = artifact.availableTranscripts;
    currentTranscriptId = artifact.currentTranscriptId;
    lastAutoScrollSegmentId = "";
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
    availableTranscripts = [];
    currentTranscriptId = "";
    defaultTranscriptId = "";
    transcriptSwitchPending = false;
    transcriptSwitchError = "";
    durationMs = 0;
    showExactWords = false;
    manualScrollLock = false;
    lastAutoScrollSegmentId = "";
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

  function syncPlaybackTime() {
    currentTimeMs = asFiniteMilliseconds(Math.round((audioEl?.currentTime ?? 0) * 1000));
    if (playing) {
      animationFrameId = window.requestAnimationFrame(syncPlaybackTime);
    }
  }

  function startPlaybackClock() {
    stopPlaybackClock();
    animationFrameId = window.requestAnimationFrame(syncPlaybackTime);
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
    if (activeSegment) {
      lastAutoScrollSegmentId = "";
      void scrollSegmentIntoView(activeSegment.id, "smooth");
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
    if (event.code !== "Space" || event.repeat) {
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
    const target = event.target;
    if (
      target instanceof HTMLElement &&
      (target.isContentEditable ||
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.tagName === "SELECT")
    ) {
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
      return display.blocks.map((block) => ({
        id: block.id,
        speaker: block.speaker,
        speakerLabel: normalizeSpeakerLabel(block.speakerLabel),
        startMs: block.startMs,
        endMs: block.endMs,
        text: block.text,
        tokens: block.tokens,
        words: [],
        sourceSegmentIds: [...block.sourceSegmentIds],
      }));
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

    const canonicalById = new Map(index.segments.map((segment) => [segment.id, segment]));
    return readable.segments.map((segment) => {
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
        tokens: [],
        words,
        sourceSegmentIds: [...segment.sourceSegmentIds],
      };
    });
  }

  function normalizeSpeakerLabel(label: string): string {
    return label.replace(/\s+(audio|video)\s*$/i, "").trim() || label;
  }

  function getActiveDisplaySegment(
    segments: DisplaySegment[],
    timeMs: number,
  ): DisplaySegment | null {
    return getLatestStartingActiveTimedRange(segments, timeMs);
  }

  function getActiveDisplayToken(
    segment: DisplaySegment | null,
    timeMs: number,
  ): DisplayTranscriptToken | null {
    if (!segment || segment.tokens.length === 0) {
      return null;
    }
    return getActiveTimedRange(
      segment.tokens.filter(
        (token): token is DisplayTranscriptToken & { startMs: number; endMs: number } =>
          token.startMs !== undefined && token.endMs !== undefined,
      ),
      timeMs,
    );
  }

  function getActiveDisplayWord(
    segment: DisplaySegment | null,
    timeMs: number,
  ): IndexedWord | null {
    if (!segment || !showExactWords || segment.tokens.length > 0) {
      return null;
    }
    return getActiveTimedRange(segment.words, timeMs);
  }

  function isSpeakerContinuation(segments: DisplaySegment[], index: number): boolean {
    if (index === 0) {
      return false;
    }
    const previous = segments[index - 1];
    const current = segments[index];
    if (!previous || !current) {
      return false;
    }
    if (!current.speaker || previous.speaker !== current.speaker) {
      return false;
    }
    return current.startMs - previous.endMs <= CONTINUATION_GAP_MS;
  }

  function hasTimedTokens(segment: DisplaySegment): boolean {
    return segment.tokens.some((token) => token.startMs !== undefined && token.endMs !== undefined);
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
    if (displayTranscript) {
      if (timingPrecision?.level === "word") {
        return "Cleaned transcript with word-level playback highlighting. Click a word or time stamp to seek.";
      }
      if (timingPrecision?.level === "mixed") {
        return "Cleaned transcript with mixed timing precision. Timed words stay clickable; rewritten text falls back to passage timing.";
      }
      return "Cleaned transcript with passage timing. Click a passage or time stamp to seek without fake word precision.";
    }
    if (readableTranscript) {
      return "Readable transcript first. Click any passage to seek. Press space to play or pause.";
    }
    return "Canonical timed transcript. Click any passage to seek the audio. Press space to play or pause.";
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

  $: summaryHtml = renderSummaryHtml(summaryMarkdown);
  $: speakers = transcriptIndex?.transcript.speakers ?? [];
  $: displaySegments = transcriptIndex
    ? buildDisplaySegments(transcriptIndex, readableTranscript, displayTranscript)
    : [];
  $: visibleSegments = displaySegments;
  $: activeSegment = getActiveDisplaySegment(displaySegments, currentTimeMs);
  $: activeToken = getActiveDisplayToken(activeSegment, currentTimeMs);
  $: activeWord = getActiveDisplayWord(activeSegment, currentTimeMs);
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
    activeSegment?.id &&
    activeSegment.id !== lastAutoScrollSegmentId
  ) {
    lastAutoScrollSegmentId = activeSegment.id;
    void scrollSegmentIntoView(activeSegment.id, "smooth");
  }
</script>

<section
  bind:this={viewRootEl}
  aria-label="Meeting view"
  class="meeting-viewer relative flex flex-col row-start-1 col-start-1 min-[981px]:col-start-2 h-full min-h-0 min-w-0"
  transition:fly={flyParams}
>
  <!-- Scroll container: holds the sticky header and all main content.
       Bottom padding keeps the last lines of the transcript reachable
       even when the (absolutely positioned) player overlaps the scroll.
       `scrollbar-gutter: stable` reserves the scrollbar gutter persistently
       so content width never shifts as scrollbar appears/disappears. -->
  <div class="flex-1 min-h-0 overflow-y-auto overflow-x-hidden overscroll-contain pb-40 min-[981px]:pb-32 scroll-stable flex flex-col">
    <!-- Sticky header — translucent bg so the transcript scrolls behind it.
         Using base-100 (not base-200) so the header reads distinct from the
         page bg even when there's no transcript content behind it. -->
    <header class="sticky top-0 z-20 flex-none flex items-center gap-3 min-h-12 px-4 py-3 bg-base-200 border-b border-base-300 min-[981px]:border-none min-[981px]:bg-base-200/50 backdrop-blur-lg">
    {#if !isDesktop}
      <button
        on:click={() => dispatch("back")}
        class="btn btn-square btn-neutral btn-xs"
        type="button"
        aria-label="Back to meeting list"
      >
        <ArrowLeft size={18} aria-hidden="true" />
      </button>
    {/if}

    <!-- Status info: artifact mode, transcript switcher, timing precision. -->
    <div class="ml-auto flex items-center gap-1 text-base-content/70">
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
      {#if transcriptIndex && timingPrecision}
        <span
          class:badge-warning={timingPrecision.level !== "word"}
          class="badge badge-xs badge-outline px-1"
          title={timingPrecision.detail}
        >
          {timingPrecision.label}
        </span>
      {/if}
    </div>

    <button
      type="button"
      class="btn btn-ghost btn-xs btn-square"
      on:click={openShortcutsDialog}
      aria-label="Keyboard shortcuts"
      title="Keyboard shortcuts"
    >
      <Keyboard size={14} aria-hidden="true" />
    </button>
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
  <header class="m-4 mb-8 min-[981px]:mx-8 min-[981px]:mb-0 min-w-0">
    <h1 class="text-3xl font-bold mb-3">
      {meeting
        ? meeting.title
        : "Meeting transcript viewer"}
    </h1>
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2 mt-2 text-base-content/70 text-sm">
      {#if transcriptIndex && meeting}
        <span class="badge badge-ghost gap-1.5 px-0">
          <Calendar size={14} aria-hidden="true" />
          {formatMeetingDate(meeting.dateLabel)}
        </span>
        <span class="badge badge-ghost gap-1.5 tabular-nums px-0">
          <Clock size={14} aria-hidden="true" />
          {formatClockTime(clampedDurationMs)}
        </span>
        {#if speakerNames.length > 0}
          <div class="flex flex-wrap items-center gap-1.5">
            <Users size={14} class="text-base-content" aria-hidden="true" />
            {#each speakerNames as name}
              <span class="badge px-1">{name}</span>
            {/each}
          </div>
        {/if}
      {/if}
    </div>
  </header>

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

    <div class="flex justify-between items-start gap-4 pb-1.5 border-b border-base-300">
      <div>
        <p class="text-xl font-semibold text-base-content">Transcript</p>
        <p class="text-xs text-base-content/70 leading-normal">{describeTranscriptInteraction()}</p>
      </div>
    </div>

    {#if visibleSegments.length === 0}
      <p class="text-base-content/70 text-sm leading-normal">No transcript loaded yet.</p>
    {:else}
      <div
        bind:this={transcriptPane}
        aria-label="Transcript"
        class="grid gap-3"
        on:touchmove={() => (manualScrollLock = true)}
        on:wheel={() => (manualScrollLock = true)}
        role="log"
      >
        {#each visibleSegments as segment, segmentIndex}
          <article
            aria-current={segment.id === activeSegment?.id ? "true" : undefined}
            class="transition-all {isSpeakerContinuation(visibleSegments, segmentIndex)
              ? '-mt-1'
              : ''} {segment.id === activeSegment?.id
              ? 'ring-2 ring-primary ring-offset-6 ring-offset-base-100 bg-base-100 rounded-sm'
              : ''}"
            id={segmentDomId(segment.id)}
          >
            <!-- Header row: speaker name + timestamp, both aligned left. -->
            <div class="flex items-center gap-1 mb-1">
              {#if !isSpeakerContinuation(visibleSegments, segmentIndex)}
                <span class="badge badge-md badge-info text-sm px-1 font-bold">{segment.speakerLabel}</span>
              {/if}
              {#if isLikelyCrosstalkTurn(segment)}
                <span
                  class="badge badge-md badge-warning badge-outline text-sm px-1"
                  title="Another participant's microphone was much louder here. This turn is probably their voice bleeding into {segment.speakerLabel}'s track, not {segment.speakerLabel} speaking."
                >probably crosstalk</span>
              {/if}
              <button
                class="badge badge-md text-sm bg-base-200 px-1 text-base-content/60 hover:bg-primary/60 hover:text-base-content cursor-pointer tabular-nums"
                on:click={() => seekTo(segment.startMs)}
                type="button"
              >
                {formatClockTime(segment.startMs)}
              </button>
            </div>

            {#if segment.tokens.length > 0 && hasTimedTokens(segment)}
              <div class="text-base leading-normal px-1.5">
                {#each segment.tokens as token}{#if token.spaceBefore}{' '}{/if}{#if token.startMs !== undefined && token.endMs !== undefined}<button
                      class="inline p-0 border-0 rounded text-base leading-normal cursor-pointer transition duration-150 {segment.id ===
                        activeSegment?.id && token === activeToken
                        ? 'bg-primary ring-1 ring-primary'
                        : 'bg-transparent hover:bg-primary/60'} {token.alignment === 'interpolated'
                        ? 'border-b border-dashed border-warning/60'
                        : ''}"
                      on:click={() => seekTo(token.startMs ?? segment.startMs)}
                      type="button"
                    >{token.text}</button>{:else}<span
                      class="inline rounded text-[1.06rem] leading-[1.72] {token.kind ===
                      'word'
                        ? 'text-base-content/70'
                        : 'text-base-content'}"
                    >{token.text}</span>{/if}{/each}
              </div>
            {:else}
              <button
                class="block w-full p-0 border-0 bg-transparent text-left text-base-content text-[1.06rem] leading-[1.72] rounded"
                on:click={() => seekTo(segment.startMs)}
                type="button"
              >
                {segment.text}
              </button>
            {/if}

            {#if !hasPrecomputedDisplay && showExactWords && segment.words.length > 0}
              <div class="block mt-3 pt-3 border-t border-base-300 text-base leading-normal">
                {#each segment.words as word, wordIndex}{#if wordIndex > 0}{' '}{/if}<button
                    class="inline p-0 border-0 rounded text-base leading-normal cursor-pointer transition duration-150 {segment.id ===
                      activeSegment?.id && word.id === activeWord?.id
                      ? 'bg-primary ring-1 ring-primary'
                      : 'hover:bg-primary'} {word.lowConfidenceSpeaker
                      ? 'text-base-content/55 border-b border-dashed border-warning/60'
                      : ''}"
                    on:click={() => seekTo(word.startMs)}
                    title={word.lowConfidenceSpeaker
                      ? `Another microphone was ${word.attributionGapDb?.toFixed(0) ?? "much"} dB louder here — probably not ${segment.speakerLabel} speaking`
                      : undefined}
                    type="button"
                  >{word.text}</button>{/each}
              </div>
            {/if}
          </article>
        {/each}
      </div>

      {#if transcriptIndex && (timingPrecision || artifactMetadata)}
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
            {#if readableTranscript && !hasPrecomputedDisplay}
              <button
                class="btn btn-ghost btn-xs rounded-full min-[981px]:btn-sm"
                on:click={() => (showExactWords = !showExactWords)}
                type="button"
              >
                {showExactWords ? "Exact words: on" : "Exact words: off"}
              </button>
            {/if}
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
