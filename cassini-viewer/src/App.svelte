<script lang="ts">
  import { onDestroy, onMount, tick } from "svelte";
  import {
    formatClockTime,
    parseTimeHash,
  } from "./core/transcript";
  import { getActiveTimedRange } from "./core/timing";
  import type {
    DisplayTranscriptToken,
    DisplayTranscriptV1,
    IndexedWord,
    ReadableTranscriptV1,
    TranscriptIndex,
  } from "./core/types";
  import {
    loadArtifactFromDirectory,
    loadBundledArtifact,
    loadPortableArtifactFromAudioPath,
    loadPortableMeetingSummary,
    type ArtifactMetadata,
    type ArtifactMetadataRow,
    type ArtifactTimingPrecision,
    type LoadedArtifact,
    type PortableMeetingSummary,
  } from "./viewer/loadArtifact";
  import {
    loadMeetingCatalog,
    sortMeetingCatalogEntries,
    type MeetingCatalogEntry,
  } from "./viewer/catalog";

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

  let transcriptIndex: TranscriptIndex | null = null;
  let displayTranscript: DisplayTranscriptV1 | null = null;
  let readableTranscript: ReadableTranscriptV1 | null = null;
  let audioSrc = "";
  let captionsSrc: string | null = null;
  let chaptersSrc: string | null = null;
  let timingPrecision: ArtifactTimingPrecision | null = null;
  let artifactMetadata: ArtifactMetadata | null = null;
  let errorMessage = "";
  let loading = true;
  let catalogMeetings: MeetingCatalogEntry[] = [];

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
  let selectedMeetingId = "";
  let activeMeeting: MeetingCatalogEntry | null = null;
  let showExactWords = false;
  let catalogHydrationGeneration = 0;
  const CONTINUATION_GAP_MS = 60_000;

  type ThemeMode = "light" | "dark";
  const THEME_STORAGE_KEY = "cassini-theme";
  let themeMode: ThemeMode = "light";
  let prefersDarkMedia: MediaQueryList | null = null;

  function readStoredTheme(): ThemeMode | null {
    try {
      const value = localStorage.getItem(THEME_STORAGE_KEY);
      return value === "light" || value === "dark" ? value : null;
    } catch {
      return null;
    }
  }

  function applyTheme(mode: ThemeMode) {
    themeMode = mode;
    document.documentElement.setAttribute("data-theme", mode);
  }

  function setTheme(mode: ThemeMode) {
    applyTheme(mode);
    try {
      localStorage.setItem(THEME_STORAGE_KEY, mode);
    } catch {
      // localStorage unavailable; in-memory state still applies
    }
  }

  function toggleTheme() {
    setTheme(themeMode === "dark" ? "light" : "dark");
  }

  function handlePrefersColorSchemeChange(event: MediaQueryListEvent) {
    if (readStoredTheme() === null) {
      applyTheme(event.matches ? "dark" : "light");
    }
  }

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
  $: playbackProgress = clampedDurationMs > 0 ? (clampedCurrentTimeMs / clampedDurationMs) * 100 : 0;
  $: documentTitle = activeMeeting
    ? `${activeMeeting.title} | Cassini Viewer`
    : catalogMeetings.length > 0
      ? "Cassini Meetings"
      : "Cassini Viewer";
  $: mastheadSummary =
    transcriptIndex !== null
      ? `${speakers.length} speaker${speakers.length === 1 ? "" : "s"} · ${displaySegments.length} passage${displaySegments.length === 1 ? "" : "s"} · ${formatClockTime(durationMs)}`
      : catalogMeetings.length > 0
        ? `${catalogMeetings.length} meeting${catalogMeetings.length === 1 ? "" : "s"} available`
        : "No transcript loaded";
  $: if (
    followPlayback &&
    !manualScrollLock &&
    activeSegment?.id &&
    activeSegment.id !== lastAutoScrollSegmentId
  ) {
    lastAutoScrollSegmentId = activeSegment.id;
    void scrollSegmentIntoView(activeSegment.id, "smooth");
  }

  onMount(async () => {
    window.addEventListener("keydown", handleWindowKeydown);
    const stored = readStoredTheme();
    if (stored !== null) {
      applyTheme(stored);
    } else if (typeof window.matchMedia === "function") {
      prefersDarkMedia = window.matchMedia("(prefers-color-scheme: dark)");
      applyTheme(prefersDarkMedia.matches ? "dark" : "light");
      prefersDarkMedia.addEventListener("change", handlePrefersColorSchemeChange);
    } else {
      applyTheme("light");
    }
    pendingSeekMs = parseTimeHash(window.location.hash);
    const meetingId = new URL(window.location.href).searchParams.get("meeting");
    const viewerConfig = window as typeof window & {
      __CASSINI_VIEWER_ARTIFACT_MODE__?: string;
    };
    const preferBundledArtifact = viewerConfig.__CASSINI_VIEWER_ARTIFACT_MODE__ === "bundled";
    try {
      if (!preferBundledArtifact) {
        const catalog = await loadMeetingCatalog();
        if (catalog?.meetings.length) {
          catalogMeetings = catalog.meetings;
          void hydrateCatalogMeetingMetadata(catalog.meetings);
          selectedMeetingId = meetingId ?? "";
          const selected =
            catalog.meetings.find((meeting) => meeting.id === meetingId) ??
            (catalog.meetings.length === 1 ? catalog.meetings[0] : null);
          if (selected) {
            await loadCatalogMeeting(selected);
          } else if (meetingId) {
            errorMessage = `Meeting not found in catalog: ${meetingId}`;
          }
          loading = false;
          return;
        }
      }
      const artifact = await loadBundledArtifact();
      selectedMeetingId = "";
      activeMeeting = null;
      applyArtifact(artifact);
    } catch (error) {
      errorMessage = error instanceof Error ? error.message : String(error);
    } finally {
      loading = false;
    }
  });

  onDestroy(() => {
    window.removeEventListener("keydown", handleWindowKeydown);
    prefersDarkMedia?.removeEventListener("change", handlePrefersColorSchemeChange);
    stopPlaybackClock();
  });

  function applyArtifact(artifact: LoadedArtifact) {
    stopPlaybackClock();
    audioEl?.pause();
    playing = false;
    currentTimeMs = 0;
    transcriptIndex = artifact.index;
    displayTranscript = artifact.displayTranscript;
    readableTranscript = artifact.readableTranscript;
    audioSrc = artifact.audioSrc;
    captionsSrc = artifact.captionsSrc;
    chaptersSrc = artifact.chaptersSrc;
    timingPrecision = artifact.timingPrecision;
    artifactMetadata = artifact.metadata;
    durationMs = artifact.index.transcript.media.durationMs;
    errorMessage = "";
    showExactWords = false;
    manualScrollLock = false;
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
    audioSrc = "";
    captionsSrc = null;
    chaptersSrc = null;
    timingPrecision = null;
    artifactMetadata = null;
    durationMs = 0;
    showExactWords = false;
    manualScrollLock = false;
    lastAutoScrollSegmentId = "";
    selectedMeetingId = "";
  }

  async function loadCatalogMeeting(meeting: MeetingCatalogEntry) {
    loading = true;
    errorMessage = "";
    try {
      const artifact = meeting.artifactPath
        ? await loadArtifactFromDirectory(meeting.artifactPath)
        : meeting.audioPath
          ? await loadPortableArtifactFromAudioPath(meeting.audioPath)
          : (() => {
              throw new Error(`Meeting ${meeting.id} is missing artifactPath and audioPath`);
            })();
      const enrichedMeeting = mergeMeetingRuntimeSummary(meeting, artifact);
      catalogMeetings = catalogMeetings.map((entry) =>
        entry.id === enrichedMeeting.id ? enrichedMeeting : entry,
      );
      selectedMeetingId = enrichedMeeting.id;
      activeMeeting = enrichedMeeting;
      applyArtifact(artifact);
      const url = new URL(window.location.href);
      url.searchParams.set("meeting", enrichedMeeting.id);
      window.history.replaceState({}, "", url);
    } catch (error) {
      resetLoadedArtifact();
      activeMeeting = null;
      selectedMeetingId = "";
      errorMessage = error instanceof Error ? error.message : String(error);
    } finally {
      loading = false;
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
    const element = document.getElementById(segmentDomId(segmentId));
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

  function loadMeetingButtonLabel(meeting: MeetingCatalogEntry): string {
    return `${meeting.title} - ${meeting.dateLabel}`;
  }

  function formatMeetingMeta(meeting: MeetingCatalogEntry): string {
    if (
      typeof meeting.speakerCount !== "number" ||
      typeof meeting.segmentCount !== "number" ||
      typeof meeting.digestDurationMs !== "number"
    ) {
      return "Loading...";
    }
    return `${meeting.speakerCount} speakers, ${meeting.segmentCount} segments, ${formatClockTime(
      meeting.digestDurationMs,
    )}`;
  }

  function mergeMeetingRuntimeSummary(
    meeting: MeetingCatalogEntry,
    artifact: LoadedArtifact,
  ): MeetingCatalogEntry {
    return {
      ...meeting,
      speakerCount: artifact.transcript.speakers.length,
      segmentCount: artifact.transcript.segments.length,
      digestDurationMs: artifact.transcript.media.durationMs,
    };
  }

  async function hydrateCatalogMeetingMetadata(meetings: MeetingCatalogEntry[]) {
    const generation = ++catalogHydrationGeneration;
    for (const meeting of meetings) {
      if (generation !== catalogHydrationGeneration) {
        return;
      }
      if (
        !meeting.audioPath ||
        (typeof meeting.speakerCount === "number" &&
          typeof meeting.segmentCount === "number" &&
          typeof meeting.digestDurationMs === "number")
      ) {
        continue;
      }
      try {
        const summary = await loadPortableMeetingSummary(meeting.audioPath);
        if (generation !== catalogHydrationGeneration) {
          return;
        }
        applyMeetingSummary(meeting.id, summary);
      } catch {
        // Keep the entry in loading state if background hydration fails.
      }
    }
  }

  function applyMeetingSummary(meetingId: string, summary: PortableMeetingSummary) {
    catalogMeetings = sortMeetingCatalogEntries(
      catalogMeetings.map((entry) =>
        entry.id === meetingId
          ? {
              ...entry,
              ...summary,
            }
          : entry,
      ),
    );
    if (activeMeeting?.id === meetingId) {
      activeMeeting = {
        ...activeMeeting,
        ...summary,
      };
    }
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
    return getActiveTimedRange(segments, timeMs);
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
      return catalogMeetings.length > 0 ? "Runtime catalog" : "Viewer ready";
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
    if (!activeMeeting && catalogMeetings.length > 0) {
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

  function metadataSectionStartsOpen(title: string): boolean {
    return title === "Meeting" || title === "Processing";
  }

  function metadataRowKey(sectionTitle: string, row: ArtifactMetadataRow): string {
    return `${sectionTitle}:${row.label}`;
  }
</script>

<svelte:head>
  <title>{documentTitle}</title>
  <meta
    name="description"
    content="Static audio and transcript viewer for transcript.words.v1 meeting artifacts."
  />
</svelte:head>

<div class="max-w-[1600px] mx-auto px-4 pb-40 max-[980px]:pb-48">
  <header
    class="card bg-base-100 shadow flex flex-row flex-wrap items-start justify-between gap-x-6 gap-y-4 p-4 sm:p-5 mb-4"
  >
    <div class="min-w-0">
      <p class="text-xs uppercase tracking-widest text-base-content/60 mb-1.5">Cassini Viewer</p>
      <h1 class="text-3xl sm:text-4xl font-bold leading-tight max-w-[26ch]">
        {#if activeMeeting}
          {activeMeeting.title}, {activeMeeting.dateLabel}
        {:else if catalogMeetings.length > 0}
          Cassini meeting library
        {:else}
          Meeting transcript viewer
        {/if}
      </h1>
      <p class="text-base-content/70 mt-2">{mastheadSummary}</p>
    </div>
    <div class="flex flex-wrap justify-end gap-2 min-w-[min(100%,23rem)]">
      <span class="badge badge-outline badge-lg">
        {formatArtifactMode()}
      </span>
      {#if transcriptIndex && timingPrecision}
        <span
          class:badge-warning={timingPrecision.level !== "word"}
          class:badge-ghost={timingPrecision.level === "word"}
          class:badge-outline={timingPrecision.level !== "word"}
          class="badge badge-lg"
          title={timingPrecision.detail}
        >
          {timingPrecision.label}
        </span>
      {/if}
      <span class="badge badge-outline badge-lg">
        {#if transcriptIndex}
          {formatClockTime(currentTimeMs)} / {formatClockTime(durationMs)}
        {:else if catalogMeetings.length > 0}
          Choose a meeting
        {:else}
          Waiting for artifact
        {/if}
      </span>
    </div>
  </header>

  <div
    class="grid items-start gap-4 grid-cols-1 min-[980px]:grid-cols-[minmax(240px,290px)_minmax(0,1fr)]"
  >
    <aside
      class="flex flex-col gap-3 content-start min-[980px]:sticky min-[980px]:top-4 min-[980px]:min-h-[calc(100vh-2rem)]"
    >
      {#if catalogMeetings.length > 0}
        <section class="card bg-base-100 shadow p-4">
          <h2 class="text-xs font-bold uppercase tracking-widest text-base-content/60 mb-3">
            Meetings
          </h2>
          <div class="grid gap-2.5">
            {#each catalogMeetings as meeting}
              <button
                on:click={() => loadCatalogMeeting(meeting)}
                type="button"
                class="grid gap-1 w-full p-3 text-left rounded-2xl border bg-base-100 hover:border-primary/40 hover:-translate-y-px transition-[transform,border-color,background-color] {meeting.id ===
                selectedMeetingId
                  ? 'border-primary bg-primary/5'
                  : 'border-base-300'}"
              >
                <span class="font-bold">{loadMeetingButtonLabel(meeting)}</span>
                <span class="text-base-content/70 text-sm leading-snug">
                  {formatMeetingMeta(meeting)}
                </span>
              </button>
            {/each}
          </div>
        </section>
      {/if}

      {#if errorMessage}
        <section class="alert alert-warning items-start">
          <div>
            <h2 class="text-xs font-bold uppercase tracking-widest mb-1">Load note</h2>
            <p>{errorMessage}</p>
          </div>
        </section>
      {/if}

      <button
        class="btn btn-ghost btn-sm mt-auto self-start"
        on:click={toggleTheme}
        aria-label="Toggle light or dark theme"
        type="button"
      >
        {themeMode === "dark" ? "☀ Light" : "🌙 Dark"}
      </button>
    </aside>

    <main class="card bg-base-100 shadow flex flex-col gap-3.5 p-4 sm:p-5">
      <div class="flex justify-between items-start gap-4 pb-1.5 border-b border-base-300">
        <div>
          <p class="text-xs uppercase tracking-widest text-base-content/60 mb-1.5">Transcript</p>
          <p class="text-base-content/70 leading-normal">{describeTranscriptInteraction()}</p>
        </div>
        {#if manualScrollLock}
          <span class="badge badge-neutral">Auto-scroll paused</span>
        {/if}
      </div>

      {#if loading}
        <p class="text-base-content/70 text-sm leading-normal">Loading transcript bootstrap...</p>
      {:else if visibleSegments.length === 0}
        <p class="text-base-content/70 text-sm leading-normal">
          {#if catalogMeetings.length > 0}
            Select a meeting to load its audio and transcript.
          {:else}
            No transcript loaded yet.
          {/if}
        </p>
      {:else}
        <div
          bind:this={transcriptPane}
          aria-label="Transcript"
          class="grid gap-2.5"
          on:touchmove={() => (manualScrollLock = true)}
          on:wheel={() => (manualScrollLock = true)}
          role="log"
        >
          {#each visibleSegments as segment, segmentIndex}
            <article
              aria-current={segment.id === activeSegment?.id ? "true" : undefined}
              class="p-4 rounded-2xl border shadow-sm transition-shadow {segment.id ===
              activeSegment?.id
                ? 'border-warning bg-warning/10 shadow-md'
                : 'border-base-300 bg-base-100'} {isSpeakerContinuation(
                visibleSegments,
                segmentIndex,
              )
                ? 'pt-3'
                : ''}"
              id={segmentDomId(segment.id)}
            >
              <div
                class="flex items-center gap-2.5 mb-1.5 {isSpeakerContinuation(
                  visibleSegments,
                  segmentIndex,
                )
                  ? 'justify-end'
                  : 'justify-between'}"
              >
                {#if !isSpeakerContinuation(visibleSegments, segmentIndex)}
                  <span class="badge badge-lg font-bold">{segment.speakerLabel}</span>
                {/if}
                <button
                  class="btn btn-ghost btn-sm rounded-full font-semibold text-base-content/70"
                  on:click={() => seekTo(segment.startMs)}
                  type="button"
                >
                  {formatClockTime(segment.startMs)}
                </button>
              </div>

              {#if segment.tokens.length > 0 && hasTimedTokens(segment)}
                <div class="text-[1.06rem] leading-[1.72]">
                  {#each segment.tokens as token}{#if token.startMs !== undefined && token.endMs !== undefined}<button
                        class="inline p-0 border-0 bg-transparent rounded text-[1.06rem] leading-[1.72] whitespace-pre-wrap cursor-pointer hover:bg-warning/20 {segment.id ===
                          activeSegment?.id && token === activeToken
                          ? 'bg-warning/40 ring-1 ring-warning font-bold underline underline-offset-2'
                          : ''} {token.alignment === 'interpolated'
                          ? 'border-b border-dashed border-warning/60'
                          : ''}"
                        on:click={() => seekTo(token.startMs ?? segment.startMs)}
                        type="button"
                      >{token.spaceBefore ? ` ${token.text}` : token.text}</button>{:else}<span
                        class="inline rounded text-[1.06rem] leading-[1.72] whitespace-pre-wrap {token.kind ===
                        'word'
                          ? 'text-base-content/70'
                          : 'text-base-content'}"
                      >{token.spaceBefore ? ` ${token.text}` : token.text}</span>{/if}{/each}
                </div>
              {:else}
                <button
                  class="block w-full p-0 border-0 bg-transparent text-left text-base-content text-[1.06rem] leading-[1.72] focus-visible:outline-2 focus-visible:outline-warning/60 focus-visible:outline-offset-4 focus-visible:rounded"
                  on:click={() => seekTo(segment.startMs)}
                  type="button"
                >
                  {segment.text}
                </button>
              {/if}

              {#if !hasPrecomputedDisplay && showExactWords && segment.words.length > 0}
                <div class="block mt-3 pt-3 border-t border-base-300 leading-[1.72]">
                  {#each segment.words as word, wordIndex}<button
                      class="inline-block px-1 py-0.5 rounded text-[0.92rem] border hover:border-warning/60 hover:bg-warning/10 hover:-translate-y-px transition whitespace-pre-wrap {segment.id ===
                        activeSegment?.id && word.id === activeWord?.id
                        ? 'border-warning bg-warning/40 font-bold underline underline-offset-2'
                        : 'border-base-300 bg-base-100'}"
                      on:click={() => seekTo(word.startMs)}
                      type="button"
                    >{wordIndex > 0 ? ` ${word.text}` : word.text}</button>{/each}
                </div>
              {/if}
            </article>
          {/each}
        </div>

        {#if transcriptIndex && (timingPrecision || artifactMetadata)}
          <section class="grid gap-3 pt-1 border-t border-base-300">
            <div
              class="flex justify-between items-start gap-3 max-[980px]:grid max-[980px]:grid-cols-1"
            >
              <div>
                <p class="text-xs uppercase tracking-widest text-base-content/60 mb-1.5">
                  Meeting metadata
                </p>
                <p class="text-base-content/70 leading-normal text-sm max-w-[72ch]">
                  {timingPrecision?.detail ??
                    "Artifact metadata is shown as provided, so older files can remain usable with reduced timing precision."}
                </p>
              </div>
              {#if timingPrecision}
                <span
                  class:badge-warning={timingPrecision.level !== "word"}
                  class:badge-ghost={timingPrecision.level === "word"}
                  class:badge-outline={timingPrecision.level !== "word"}
                  class="badge badge-lg"
                  title={timingPrecision.detail}
                >
                  {timingPrecision.label}
                </span>
              {/if}
            </div>

            {#if artifactMetadata}
              <div class="grid gap-2.5">
                {#each metadataSections as section}
                  <details
                    class="collapse collapse-arrow bg-base-200"
                    open={metadataSectionStartsOpen(section.title)}
                  >
                    <summary class="collapse-title text-sm font-bold">{section.title}</summary>
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
                      </dl>
                    </div>
                  </details>
                {/each}
                <details class="collapse collapse-arrow bg-base-200">
                  <summary class="collapse-title text-sm font-bold">Raw JSON</summary>
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

  <footer
    class="fixed inset-x-0 bottom-0 z-30 px-4 pt-4 pb-4 bg-gradient-to-b from-base-200/0 via-base-200/80 to-base-200 pointer-events-none"
  >
    <div
      class="card bg-base-100 shadow max-w-[1600px] mx-auto px-4 py-3 grid grid-cols-[auto_minmax(0,1fr)_auto] gap-3.5 items-center pointer-events-auto max-[980px]:grid-cols-1 max-[980px]:gap-3"
    >
      <div class="grid gap-1 min-w-[11rem]">
        <p class="text-xs uppercase tracking-widest text-base-content/60 mb-1.5">Player</p>
        <strong class="text-base-content text-[0.96rem]">
          {#if activeMeeting}
            {activeMeeting.title}
          {:else if catalogMeetings.length > 0}
            Meeting library
          {:else}
            Manual artifact
          {/if}
        </strong>
        <p class="m-0 text-base-content/70 text-sm leading-snug">Space toggles play and pause.</p>
      </div>

      {#if audioSrc}
        <div class="grid gap-2.5 min-w-0">
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

          <div
            class="grid grid-cols-[auto_minmax(0,1fr)] gap-3.5 items-center min-w-0 max-[980px]:grid-cols-1"
          >
            <div class="flex flex-wrap gap-1.5">
              <button
                class="btn btn-warning btn-md font-bold"
                on:click={togglePlayback}
                type="button"
              >
                {#if playing}
                  Pause
                {:else}
                  Play
                {/if}
              </button>
            </div>

            <div class="grid gap-1.5 min-w-0">
              <div
                class="flex justify-between gap-3 text-base-content/70 text-sm tabular-nums max-[980px]:flex-col max-[980px]:gap-0.5"
              >
                <span>{formatClockTime(clampedCurrentTimeMs)} elapsed</span>
                <span>{formatClockTime(clampedDurationMs)} total</span>
                <span>-{formatClockTime(remainingMs)} remaining</span>
              </div>
              <input
                aria-label="Seek within meeting"
                class="range range-warning range-sm w-full"
                max={Math.max(clampedDurationMs, 1)}
                min="0"
                on:input={handleTimelineInput}
                step="250"
                type="range"
                value={Math.min(clampedCurrentTimeMs, Math.max(clampedDurationMs, 1))}
              />
            </div>
          </div>
        </div>
      {:else}
        <p class="text-base-content/70 text-sm leading-normal">
          {#if catalogMeetings.length > 0}
            Select a meeting to load its audio source.
          {:else}
            No audio source available for this artifact.
          {/if}
        </p>
      {/if}

      <div class="flex flex-wrap justify-end gap-2 max-[980px]:justify-start">
        <button
          aria-checked={followPlayback && !manualScrollLock}
          class="btn btn-ghost btn-sm rounded-full inline-flex items-center gap-2 normal-case"
          on:click={toggleFollowPlayback}
          role="switch"
          type="button"
        >
          <span class="whitespace-nowrap">
            {#if followPlayback && manualScrollLock}
              Resume auto-scroll
            {:else}
              Auto-scroll
            {/if}
          </span>
          <span
            class="relative inline-block w-10 h-5 rounded-full transition-colors flex-none {followPlayback &&
            !manualScrollLock
              ? 'bg-warning'
              : 'bg-base-300'}"
          >
            <span
              class="absolute top-[2px] left-[2px] w-4 h-4 rounded-full bg-base-100 shadow transition-transform {followPlayback &&
              !manualScrollLock
                ? 'translate-x-[20px]'
                : ''}"
            ></span>
          </span>
        </button>
        {#if readableTranscript && !hasPrecomputedDisplay}
          <button
            class="btn btn-ghost btn-sm rounded-full"
            on:click={() => (showExactWords = !showExactWords)}
            type="button"
          >
            {showExactWords ? "Exact words: on" : "Exact words: off"}
          </button>
        {/if}
      </div>
    </div>
  </footer>
</div>

