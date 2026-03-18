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
    let winner: DisplaySegment | null = null;
    for (const segment of segments) {
      if (segment.startMs <= timeMs && timeMs <= segment.endMs) {
        winner = segment;
      }
    }
    return winner;
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
    return title === "Artifact" || title === "Meeting" || title === "Provenance" || title === "Transcript";
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

<div class="shell">
  <header class="masthead panel">
    <div class="masthead-copy">
      <p class="eyebrow">Cassini Viewer</p>
      <h1>
        {#if activeMeeting}
          {activeMeeting.title}, {activeMeeting.dateLabel}
        {:else if catalogMeetings.length > 0}
          Cassini meeting library
        {:else}
          Meeting transcript viewer
        {/if}
      </h1>
      <p class="masthead-meta">{mastheadSummary}</p>
    </div>
    <div class="info-strip">
      <span class="info-pill">
        {formatArtifactMode()}
      </span>
      {#if transcriptIndex && timingPrecision}
        <span
          class:info-pill-warning={timingPrecision.level !== "word"}
          class="info-pill info-pill-subtle"
          title={timingPrecision.detail}
        >
          {timingPrecision.label}
        </span>
      {/if}
      <span class="info-pill">
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

  <div class="layout">
    <aside class="sidebar">
      {#if catalogMeetings.length > 0}
        <section class="panel">
          <h2>Meetings</h2>
          <div class="meeting-list">
            {#each catalogMeetings as meeting}
              <button
                class:active-meeting={meeting.id === selectedMeetingId}
                class="meeting-card"
                on:click={() => loadCatalogMeeting(meeting)}
                type="button"
              >
                <span class="meeting-title">{loadMeetingButtonLabel(meeting)}</span>
                <span class="meeting-meta">{formatMeetingMeta(meeting)}</span>
              </button>
            {/each}
          </div>
        </section>
      {/if}

      {#if errorMessage}
        <section class="panel warning">
          <h2>Load note</h2>
          <p>{errorMessage}</p>
        </section>
      {/if}
    </aside>

    <main class="panel transcript-panel">
      <div class="transcript-header">
        <div>
          <p class="eyebrow">Transcript</p>
          <p class="transcript-summary">{describeTranscriptInteraction()}</p>
        </div>
        {#if manualScrollLock}
          <span class="lock-pill">Auto-scroll paused</span>
        {/if}
      </div>

      {#if loading}
        <p class="muted">Loading transcript bootstrap...</p>
      {:else if visibleSegments.length === 0}
        <p class="muted">
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
          class="transcript-list"
          on:touchmove={() => (manualScrollLock = true)}
          on:wheel={() => (manualScrollLock = true)}
          role="log"
        >
          {#each visibleSegments as segment, segmentIndex}
            <article
              aria-current={segment.id === activeSegment?.id ? "true" : undefined}
              class:active={segment.id === activeSegment?.id}
              class:continuation-segment={isSpeakerContinuation(visibleSegments, segmentIndex)}
              class="segment"
              id={segmentDomId(segment.id)}
            >
              <div class="segment-meta">
                {#if !isSpeakerContinuation(visibleSegments, segmentIndex)}
                  <button class="speaker-tag" on:click={() => seekTo(segment.startMs)} type="button">
                    {segment.speakerLabel}
                  </button>
                {/if}
                <button class="time-chip" on:click={() => seekTo(segment.startMs)} type="button">
                  {formatClockTime(segment.startMs)}
                </button>
              </div>

              {#if segment.tokens.length > 0 && hasTimedTokens(segment)}
                <div class="segment-text token-flow">
                  {#each segment.tokens as token}{#if token.startMs !== undefined && token.endMs !== undefined}<button
                        class:active-token={segment.id === activeSegment?.id && token === activeToken}
                        class:interpolated-token={token.alignment === "interpolated"}
                        class="token-button"
                        on:click={() => seekTo(token.startMs ?? segment.startMs)}
                        type="button"
                      >{token.spaceBefore ? ` ${token.text}` : token.text}</button>{:else}<span
                        class:untimed-word={token.kind === "word"}
                        class="token-text"
                      >{token.spaceBefore ? ` ${token.text}` : token.text}</span>{/if}{/each}
                </div>
              {:else}
                <button class="segment-text" on:click={() => seekTo(segment.startMs)} type="button">
                  {segment.text}
                </button>
              {/if}

              {#if !hasPrecomputedDisplay && showExactWords && segment.words.length > 0}
                <div class="word-row">
                  {#each segment.words as word, wordIndex}<button
                      class:active-word={segment.id === activeSegment?.id && word.id === activeWord?.id}
                      class="word"
                      on:click={() => seekTo(word.startMs)}
                      type="button"
                    >{wordIndex > 0 ? ` ${word.text}` : word.text}</button>{/each}
                </div>
              {/if}
            </article>
          {/each}
        </div>

        {#if transcriptIndex && (timingPrecision || artifactMetadata)}
          <section class="meeting-footer">
            <div class="meeting-footer-header">
              <div>
                <p class="eyebrow">Meeting metadata</p>
                <p class="meeting-footer-copy">
                  {timingPrecision?.detail ??
                    "Artifact metadata is shown as provided, so older files can remain usable with reduced timing precision."}
                </p>
              </div>
              {#if timingPrecision}
                <span
                  class:info-pill-warning={timingPrecision.level !== "word"}
                  class="info-pill info-pill-subtle"
                  title={timingPrecision.detail}
                >
                  {timingPrecision.label}
                </span>
              {/if}
            </div>

            {#if artifactMetadata}
              <div class="metadata-sections">
                {#each metadataSections as section}
                  <details class="metadata-section" open={metadataSectionStartsOpen(section.title)}>
                    <summary>{section.title}</summary>
                    <dl class="metadata-grid">
                      {#each section.rows as row (metadataRowKey(section.title, row))}
                        <dt>{formatMetadataLabel(row.label)}</dt>
                        <dd>{row.value}</dd>
                      {/each}
                    </dl>
                  </details>
                {/each}
                <details class="metadata-section">
                  <summary>Raw JSON</summary>
                  <pre class="metadata-raw">{artifactMetadata.rawJson}</pre>
                </details>
              </div>
            {/if}
          </section>
        {/if}
      {/if}
    </main>
  </div>

  <footer class="player-dock">
    <div class="player-card panel">
      <div class="player-meta">
        <p class="eyebrow">Player</p>
        <strong>
          {#if activeMeeting}
            {activeMeeting.title}
          {:else if catalogMeetings.length > 0}
            Meeting library
          {:else}
            Manual artifact
          {/if}
        </strong>
        <p class="player-hint">Space toggles play and pause.</p>
      </div>

      {#if audioSrc}
        <div class="player-shell">
          <audio
            bind:this={audioEl}
            class="engine-audio"
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

          <div class="player-controls">
            <div class="player-buttons">
              <button class="transport-button transport-primary" on:click={togglePlayback} type="button">
                {#if playing}
                  Pause
                {:else}
                  Play
                {/if}
              </button>
            </div>

            <div class="timeline-wrap">
              <div class="timeline-stats">
                <span>{formatClockTime(clampedCurrentTimeMs)} elapsed</span>
                <span>{formatClockTime(clampedDurationMs)} total</span>
                <span>-{formatClockTime(remainingMs)} remaining</span>
              </div>
              <input
                aria-label="Seek within meeting"
                class="timeline-slider"
                max={Math.max(clampedDurationMs, 1)}
                min="0"
                on:input={handleTimelineInput}
                step="250"
                style={`--player-progress: ${playbackProgress}%`}
                type="range"
                value={Math.min(clampedCurrentTimeMs, Math.max(clampedDurationMs, 1))}
              />
            </div>
          </div>
        </div>
      {:else}
        <p class="muted">
          {#if catalogMeetings.length > 0}
            Select a meeting to load its audio source.
          {:else}
            No audio source available for this artifact.
          {/if}
        </p>
      {/if}

      <div class="player-actions">
        <button
          aria-checked={followPlayback && !manualScrollLock}
          class="switch-button"
          on:click={toggleFollowPlayback}
          role="switch"
          type="button"
        >
          <span class="switch-label">
            {#if followPlayback && manualScrollLock}
              Resume auto-scroll
            {:else}
              Auto-scroll
            {/if}
          </span>
          <span class:checked={followPlayback && !manualScrollLock} class="switch-track">
            <span class="switch-thumb"></span>
          </span>
        </button>
        {#if readableTranscript && !hasPrecomputedDisplay}
          <button on:click={() => (showExactWords = !showExactWords)} type="button">
            {showExactWords ? "Exact words: on" : "Exact words: off"}
          </button>
        {/if}
      </div>
    </div>
  </footer>
</div>

<style>
  .shell {
    max-width: 1600px;
    margin: 0 auto;
    padding: 1rem 1rem 9.5rem;
  }

  .eyebrow {
    margin: 0 0 0.35rem;
    text-transform: uppercase;
    letter-spacing: 0.18em;
    font-size: 0.76rem;
    color: #7a6849;
  }

  h1,
  h2 {
    margin: 0;
    font-family: Georgia, "Times New Roman", serif;
    line-height: 1.05;
  }

  h1 {
    font-size: clamp(1.8rem, 3vw, 2.85rem);
    max-width: 26ch;
  }

  h2 {
    font-size: clamp(1.15rem, 1.6vw, 1.5rem);
  }

  .panel {
    border: 1px solid rgba(84, 78, 55, 0.16);
    border-radius: 1.1rem;
    background: rgba(255, 252, 247, 0.78);
    box-shadow: 0 12px 30px rgba(87, 72, 40, 0.08);
    backdrop-filter: blur(8px);
  }

  .panel {
    padding: 0.95rem 1rem;
  }

  .masthead {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem 1.4rem;
    margin-bottom: 1rem;
    padding: 1rem 1.15rem;
  }

  .masthead-copy {
    min-width: 0;
  }

  .masthead-meta {
    margin: 0.5rem 0 0;
    color: #665d51;
    font-size: 0.98rem;
    line-height: 1.5;
  }

  .info-strip {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 0.45rem;
    min-width: min(100%, 23rem);
  }

  .info-pill {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 2.3rem;
    padding: 0.5rem 0.8rem;
    border-radius: 999px;
    border: 1px solid rgba(130, 114, 82, 0.15);
    background: rgba(255, 255, 255, 0.72);
    color: #544c40;
    font-size: 0.9rem;
    font-weight: 600;
  }

  .info-pill-subtle {
    color: #64584a;
    background: rgba(252, 249, 243, 0.8);
    font-weight: 500;
  }

  .info-pill-warning {
    border-color: rgba(180, 112, 58, 0.22);
    color: #7a5b2e;
  }

  .layout {
    display: grid;
    align-items: start;
    grid-template-columns: minmax(240px, 290px) minmax(0, 1fr);
    gap: 1rem;
  }

  .sidebar {
    display: grid;
    gap: 0.85rem;
    align-content: start;
    position: sticky;
    top: 1rem;
  }

  .sidebar .panel h2 {
    font-family: ui-sans-serif, system-ui, sans-serif;
    font-size: 0.76rem;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.16em;
    color: #7a6849;
    margin-bottom: 0.75rem;
  }

  .warning {
    border-color: rgba(180, 96, 60, 0.35);
  }

  .meeting-list {
    display: grid;
    gap: 0.6rem;
  }

  .meeting-card {
    display: grid;
    gap: 0.34rem;
    width: 100%;
    padding: 0.8rem 0.85rem;
    text-align: left;
    border: 1px solid rgba(93, 82, 66, 0.16);
    border-radius: 1rem;
    background:
      linear-gradient(120deg, rgba(255, 255, 255, 0.94), rgba(246, 239, 229, 0.8)),
      rgba(255, 255, 255, 0.92);
    color: inherit;
  }

  .meeting-card:hover {
    border-color: rgba(130, 96, 42, 0.34);
    transform: translateY(-1px);
  }

  .meeting-card.active-meeting {
    border-color: rgba(180, 112, 58, 0.45);
    box-shadow: inset 0 0 0 1px rgba(180, 112, 58, 0.2);
  }

  .meeting-title {
    font-weight: 700;
  }

  .meeting-meta {
    color: #665d51;
    font-size: 0.89rem;
    line-height: 1.45;
  }

  .player-actions button,
  .speaker-tag,
  .time-chip,
  .token-button,
  .word {
    border: 1px solid rgba(93, 82, 66, 0.16);
    border-radius: 0.8rem;
    background: rgba(255, 255, 255, 0.92);
    color: inherit;
    transition:
      transform 120ms ease,
      border-color 120ms ease,
      background 120ms ease;
  }

  .player-actions button:hover,
  .speaker-tag:hover,
  .time-chip:hover,
  .token-button:hover,
  .word:hover {
    transform: translateY(-1px);
    border-color: rgba(130, 96, 42, 0.4);
  }

  .player-actions button {
    padding: 0.68rem 0.82rem;
  }

  .switch-button {
    display: inline-flex;
    align-items: center;
    gap: 0.7rem;
    padding: 0.58rem 0.78rem;
    border: 1px solid rgba(93, 82, 66, 0.16);
    border-radius: 999px;
    background: rgba(255, 255, 255, 0.92);
    color: inherit;
    font-weight: 600;
    transition:
      transform 120ms ease,
      border-color 120ms ease,
      background 120ms ease;
  }

  .switch-button:hover {
    transform: translateY(-1px);
    border-color: rgba(130, 96, 42, 0.4);
  }

  .switch-button:focus-visible {
    outline: 2px solid rgba(180, 112, 58, 0.38);
    outline-offset: 3px;
  }

  .switch-label {
    white-space: nowrap;
  }

  .switch-track {
    position: relative;
    width: 2.6rem;
    height: 1.45rem;
    border-radius: 999px;
    background: rgba(126, 114, 90, 0.22);
    box-shadow: inset 0 0 0 1px rgba(93, 82, 66, 0.12);
    transition: background 120ms ease;
    flex: 0 0 auto;
  }

  .switch-track.checked {
    background: rgba(194, 126, 59, 0.92);
    box-shadow: inset 0 0 0 1px rgba(157, 92, 39, 0.35);
  }

  .switch-thumb {
    position: absolute;
    top: 0.14rem;
    left: 0.16rem;
    width: 1.15rem;
    height: 1.15rem;
    border-radius: 50%;
    background: #fffaf4;
    box-shadow: 0 1px 4px rgba(87, 72, 40, 0.2);
    transition: transform 120ms ease;
  }

  .switch-track.checked .switch-thumb {
    transform: translateX(1.14rem);
  }

  .muted {
    margin: 0;
    color: #665d51;
    line-height: 1.5;
    font-size: 0.93rem;
  }

  .transcript-panel {
    display: grid;
    gap: 0.9rem;
    align-content: start;
    padding: 1.1rem 1.2rem 1.2rem;
  }

  .transcript-header {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    align-items: flex-start;
    padding-bottom: 0.4rem;
    border-bottom: 1px solid rgba(84, 78, 55, 0.1);
  }

  .transcript-summary {
    margin: 0;
    color: #665d51;
    line-height: 1.5;
    font-size: 0.98rem;
  }

  .lock-pill {
    padding: 0.45rem 0.72rem;
    border-radius: 999px;
    background: rgba(201, 170, 113, 0.24);
    color: #6c5128;
    font-size: 0.9rem;
  }

  .transcript-list {
    display: grid;
    gap: 0.6rem;
  }

  .meeting-footer {
    display: grid;
    gap: 0.8rem;
    padding-top: 0.15rem;
    border-top: 1px solid rgba(84, 78, 55, 0.1);
  }

  .meeting-footer-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 0.8rem;
  }

  .meeting-footer-copy {
    margin: 0;
    color: #665d51;
    line-height: 1.5;
    font-size: 0.93rem;
    max-width: 72ch;
  }

  .metadata-sections {
    display: grid;
    gap: 0.65rem;
  }

  .metadata-section {
    border: 1px solid rgba(84, 78, 55, 0.1);
    border-radius: 0.9rem;
    background: rgba(255, 255, 255, 0.58);
    overflow: hidden;
  }

  .metadata-section summary {
    cursor: pointer;
    padding: 0.8rem 0.9rem;
    font-weight: 700;
    color: #483f34;
    list-style: none;
  }

  .metadata-section summary::-webkit-details-marker {
    display: none;
  }

  .metadata-grid {
    display: grid;
    grid-template-columns: minmax(10rem, 16rem) minmax(0, 1fr);
    gap: 0.55rem 0.9rem;
    padding: 0 0.9rem 0.9rem;
    margin: 0;
  }

  .metadata-grid dt {
    margin: 0;
    color: #665d51;
    font-size: 0.88rem;
    line-height: 1.4;
  }

  .metadata-grid dd {
    margin: 0;
    color: #2f2d27;
    font-size: 0.9rem;
    line-height: 1.5;
    overflow-wrap: anywhere;
  }

  .metadata-raw {
    margin: 0;
    padding: 0 0.9rem 0.9rem;
    color: #3c362d;
    font-size: 0.84rem;
    line-height: 1.55;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  .segment {
    padding: 0.95rem 1.05rem 1rem;
    border-radius: 1rem;
    border: 1px solid rgba(84, 78, 55, 0.12);
    background: rgba(255, 255, 255, 0.7);
    box-shadow: 0 4px 12px rgba(87, 72, 40, 0.04);
  }

  .segment.active {
    border-color: rgba(180, 112, 58, 0.42);
    background:
      linear-gradient(90deg, rgba(220, 188, 136, 0.18), transparent 32%),
      rgba(255, 255, 255, 0.92);
    box-shadow: 0 8px 18px rgba(155, 115, 62, 0.08);
  }

  .segment.continuation-segment {
    padding-top: 0.72rem;
  }

  .segment-meta {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.65rem;
    margin-bottom: 0.45rem;
  }

  .continuation-segment .segment-meta {
    justify-content: flex-end;
  }

  .speaker-tag,
  .time-chip {
    display: inline-flex;
    align-items: center;
    min-height: 2rem;
    padding: 0.42rem 0.7rem;
    border-radius: 999px;
    font: inherit;
    line-height: 1;
  }

  .speaker-tag {
    font-size: 0.84rem;
    font-weight: 700;
    background: rgba(243, 236, 227, 0.96);
  }

  .time-chip {
    font-size: 0.83rem;
    font-weight: 600;
    color: #5b5347;
  }

  .segment-text {
    width: 100%;
    padding: 0;
    border: none;
    background: none;
    text-align: left;
    color: #2f2d27;
    font: inherit;
    font-size: 1.06rem;
    line-height: 1.72;
  }

  .segment-text:hover {
    transform: none;
    border-color: transparent;
    background: none;
  }

  .segment-text:focus-visible {
    outline: 2px solid rgba(180, 112, 58, 0.38);
    outline-offset: 4px;
    border-radius: 0.4rem;
  }

  .token-flow {
    display: block;
  }

  .token-button,
  .token-text {
    display: inline;
    padding: 0;
    border: none;
    background: none;
    border-radius: 0.4rem;
    font: inherit;
    font-size: 1.06rem;
    line-height: 1.72;
    white-space: pre-wrap;
    color: #2f2d27;
  }

  .token-button {
    cursor: pointer;
  }

  .token-button:hover {
    background: rgba(236, 208, 171, 0.3);
  }

  .token-button.active-token {
    background: rgba(236, 208, 171, 0.88);
    box-shadow: inset 0 0 0 1px rgba(186, 96, 44, 0.34);
    font-weight: 700;
    text-decoration: underline;
    text-underline-offset: 0.16em;
  }

  .token-button.interpolated-token {
    border-bottom: 1px dashed rgba(186, 96, 44, 0.45);
  }

  .token-text.untimed-word {
    color: #544c40;
  }

  .word-row {
    display: block;
    margin-top: 0.8rem;
    padding-top: 0.75rem;
    border-top: 1px solid rgba(84, 78, 55, 0.1);
    line-height: 1.72;
  }

  .word {
    padding: 0.1rem 0.18rem;
    line-height: inherit;
    font-size: 0.92rem;
    white-space: pre-wrap;
  }

  .word.active-word {
    border-color: rgba(186, 96, 44, 0.5);
    background: rgba(236, 208, 171, 0.88);
    font-weight: 700;
    text-decoration: underline;
    text-underline-offset: 0.18em;
  }

  .player-dock {
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 30;
    padding: 1rem 1rem 1.1rem;
    background:
      linear-gradient(180deg, rgba(221, 215, 196, 0) 0%, rgba(221, 215, 196, 0.78) 35%, rgba(221, 215, 196, 0.96) 100%);
    pointer-events: none;
  }

  .player-card {
    max-width: 1600px;
    margin: 0 auto;
    padding: 0.85rem 1rem;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    gap: 0.9rem;
    align-items: center;
    pointer-events: auto;
  }

  .player-meta {
    display: grid;
    gap: 0.2rem;
    min-width: 11rem;
  }

  .player-meta strong {
    font-size: 0.96rem;
    color: #383228;
  }

  .player-hint {
    margin: 0;
    color: #665d51;
    font-size: 0.88rem;
    line-height: 1.35;
  }

  .player-shell {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 0.7rem;
    min-width: 0;
  }

  .engine-audio {
    position: absolute;
    width: 1px;
    height: 1px;
    opacity: 0;
    pointer-events: none;
  }

  .player-controls {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: 0.85rem;
    align-items: center;
    min-width: 0;
  }

  .player-buttons {
    display: flex;
    flex-wrap: wrap;
    gap: 0.45rem;
  }

  .transport-button {
    padding: 0.68rem 0.82rem;
    border: 1px solid rgba(93, 82, 66, 0.16);
    border-radius: 0.8rem;
    background: rgba(255, 255, 255, 0.92);
    color: inherit;
    font-weight: 700;
    transition:
      transform 120ms ease,
      border-color 120ms ease,
      background 120ms ease;
  }

  .transport-button:hover {
    transform: translateY(-1px);
    border-color: rgba(130, 96, 42, 0.4);
  }

  .transport-primary {
    background: linear-gradient(135deg, rgba(198, 134, 73, 0.96), rgba(173, 98, 43, 0.96));
    border-color: rgba(138, 82, 32, 0.55);
    color: #fffaf4;
  }

  .timeline-wrap {
    display: grid;
    gap: 0.38rem;
    min-width: 0;
  }

  .timeline-stats {
    display: flex;
    justify-content: space-between;
    gap: 0.8rem;
    color: #665d51;
    font-size: 0.86rem;
    font-variant-numeric: tabular-nums;
  }

  .timeline-slider {
    -webkit-appearance: none;
    appearance: none;
    width: 100%;
    height: 0.8rem;
    border-radius: 999px;
    background:
      linear-gradient(
        90deg,
        rgba(194, 126, 59, 0.95) 0%,
        rgba(194, 126, 59, 0.95) var(--player-progress, 0%),
        rgba(126, 114, 90, 0.18) var(--player-progress, 0%),
        rgba(126, 114, 90, 0.18) 100%
      );
    outline: none;
    cursor: pointer;
  }

  .timeline-slider::-webkit-slider-thumb {
    -webkit-appearance: none;
    appearance: none;
    width: 1rem;
    height: 1rem;
    border-radius: 50%;
    border: 2px solid rgba(157, 92, 39, 0.92);
    background: #fffaf4;
    box-shadow: 0 2px 8px rgba(87, 72, 40, 0.18);
  }

  .timeline-slider::-moz-range-thumb {
    width: 1rem;
    height: 1rem;
    border-radius: 50%;
    border: 2px solid rgba(157, 92, 39, 0.92);
    background: #fffaf4;
    box-shadow: 0 2px 8px rgba(87, 72, 40, 0.18);
  }

  .timeline-slider::-moz-range-track {
    height: 0.8rem;
    border-radius: 999px;
    background: transparent;
  }

  .player-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 0.55rem;
  }

  @media (max-width: 980px) {
    .masthead,
    .layout {
      grid-template-columns: 1fr;
    }

    .masthead {
      flex-direction: column;
    }

    .sidebar {
      position: static;
    }

    .transcript-panel {
      padding: 1rem;
    }

    .meeting-footer-header,
    .metadata-grid {
      grid-template-columns: 1fr;
    }

    .segment {
      padding: 0.85rem 0.9rem 0.92rem;
    }

    .segment-text {
      font-size: 1rem;
    }

    .shell {
      padding-bottom: 11.5rem;
    }

    .player-card {
      grid-template-columns: 1fr;
      gap: 0.75rem;
    }

    .player-controls {
      grid-template-columns: 1fr;
    }

    .player-actions {
      justify-content: flex-start;
    }

    .timeline-stats {
      flex-direction: column;
      gap: 0.2rem;
    }
  }
</style>
