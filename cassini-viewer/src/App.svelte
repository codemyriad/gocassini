<script lang="ts">
  import { onDestroy, onMount, tick } from "svelte";
  import {
    formatClockTime,
    parseTimeHash,
  } from "./core/transcript";
  import type {
    IndexedWord,
    ReadableTranscriptV1,
    TranscriptIndex,
  } from "./core/types";
  import {
    loadArtifactFromDirectory,
    loadBundledArtifact,
    type LoadedArtifact,
  } from "./viewer/loadArtifact";
  import { DEMO_MEETINGS, type DemoMeeting } from "./viewer/demoMeetings";

  interface DisplaySegment {
    id: string;
    speaker?: string;
    speakerLabel: string;
    startMs: number;
    endMs: number;
    text: string;
    words: IndexedWord[];
    sourceSegmentIds: string[];
  }

  let transcriptIndex: TranscriptIndex | null = null;
  let readableTranscript: ReadableTranscriptV1 | null = null;
  let audioSrc = "";
  let captionsSrc: string | null = null;
  let chaptersSrc: string | null = null;
  let errorMessage = "";
  let loading = true;

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
  let activeDemoMeeting: DemoMeeting | null = null;
  let showExactWords = false;
  const CONTINUATION_GAP_MS = 60_000;

  $: speakers = transcriptIndex?.transcript.speakers ?? [];
  $: displaySegments = transcriptIndex ? buildDisplaySegments(transcriptIndex, readableTranscript) : [];
  $: visibleSegments = displaySegments;
  $: activeSegment = getActiveDisplaySegment(displaySegments, currentTimeMs);
  $: activeWord = getActiveDisplayWord(activeSegment, currentTimeMs);
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
    if (DEMO_MEETINGS.length > 0) {
      const selected =
        DEMO_MEETINGS.find((meeting) => meeting.id === meetingId) ?? DEMO_MEETINGS[0];
      await loadDemoMeeting(selected);
      return;
    }
    try {
      const artifact = await loadBundledArtifact();
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
    playing = false;
    currentTimeMs = 0;
    transcriptIndex = artifact.index;
    readableTranscript = artifact.readableTranscript;
    audioSrc = artifact.audioSrc;
    captionsSrc = artifact.captionsSrc;
    chaptersSrc = artifact.chaptersSrc;
    durationMs = artifact.index.transcript.media.durationMs;
    errorMessage = "";
    showExactWords = false;
    manualScrollLock = false;
    lastAutoScrollSegmentId = "";
  }

  async function loadDemoMeeting(meeting: DemoMeeting) {
    loading = true;
    errorMessage = "";
    try {
      const artifact = await loadArtifactFromDirectory(meeting.path);
      selectedMeetingId = meeting.id;
      activeDemoMeeting = meeting;
      applyArtifact(artifact);
      const url = new URL(window.location.href);
      url.searchParams.set("meeting", meeting.id);
      window.history.replaceState({}, "", url);
    } catch (error) {
      errorMessage = error instanceof Error ? error.message : String(error);
    } finally {
      loading = false;
    }
  }

  function syncPlaybackTime() {
    currentTimeMs = Math.round((audioEl?.currentTime ?? 0) * 1000);
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
    durationMs = Math.round((audioEl?.duration ?? 0) * 1000) || durationMs;
    if (pendingSeekMs !== null) {
      seekTo(pendingSeekMs);
      pendingSeekMs = null;
    }
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
    if (!audioEl) {
      return;
    }
    audioEl.currentTime = Math.max(0, ms) / 1000;
    syncPlaybackTime();
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

  function loadMeetingButtonLabel(meeting: DemoMeeting): string {
    return `${meeting.title} - ${meeting.dateLabel}`;
  }

  function buildDisplaySegments(
    index: TranscriptIndex,
    readable: ReadableTranscriptV1 | null,
  ): DisplaySegment[] {
    if (!readable) {
      return index.segments.map((segment) => ({
        id: segment.id,
        speaker: segment.speaker,
        speakerLabel: normalizeSpeakerLabel(segment.speakerLabel),
        startMs: segment.startMs,
        endMs: segment.endMs,
        text: segment.text,
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

  function getActiveDisplayWord(
    segment: DisplaySegment | null,
    timeMs: number,
  ): IndexedWord | null {
    if (!segment || !showExactWords) {
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
</script>

<svelte:head>
  <title>Cassini Viewer</title>
  <meta
    name="description"
    content="Static audio and transcript viewer for transcript.words.v1 meeting artifacts."
  />
</svelte:head>

<div class="shell">
  <header class="masthead panel">
    <div class="masthead-copy">
      <p class="eyebrow">Cassini Viewer</p>
      <h1>{activeDemoMeeting ? `${activeDemoMeeting.title}, ${activeDemoMeeting.dateLabel}` : "Meeting transcript viewer"}</h1>
      <p class="masthead-meta">
        {speakers.length} speaker{speakers.length === 1 ? "" : "s"} · {displaySegments.length} passage{displaySegments.length === 1 ? "" : "s"} · {formatClockTime(durationMs)}
      </p>
    </div>
    <div class="info-strip">
      <span class="info-pill">{readableTranscript ? "Readable + exact timing" : "Canonical timing"}</span>
      <span class="info-pill">{formatClockTime(currentTimeMs)} / {formatClockTime(durationMs)}</span>
    </div>
  </header>

  <div class="layout">
    <aside class="sidebar">
      {#if DEMO_MEETINGS.length > 0}
        <section class="panel">
          <h2>Meetings</h2>
          <div class="meeting-list">
            {#each DEMO_MEETINGS as meeting}
              <button
                class:active-meeting={meeting.id === selectedMeetingId}
                class="meeting-card"
                on:click={() => loadDemoMeeting(meeting)}
                type="button"
              >
                <span class="meeting-title">{loadMeetingButtonLabel(meeting)}</span>
                <span class="meeting-meta">
                  {meeting.speakerCount} speakers, {meeting.segmentCount} segments, {formatClockTime(
                    meeting.digestDurationMs,
                  )}
                </span>
                <span class="meeting-meta">{meeting.speakers.join(", ")}</span>
                <span class="meeting-teaser">{meeting.teaser}</span>
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
          <p class="transcript-summary">
            {#if readableTranscript}
              Readable transcript first. Click any passage to seek. Press space to play or pause.
            {:else}
              Canonical timed transcript. Click any passage to seek the audio. Press space to play or pause.
            {/if}
          </p>
        </div>
        {#if manualScrollLock}
          <span class="lock-pill">Auto-scroll paused</span>
        {/if}
      </div>

      {#if loading}
        <p class="muted">Loading transcript bootstrap...</p>
      {:else if visibleSegments.length === 0}
        <p class="muted">No transcript loaded yet.</p>
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

              <button class="segment-text" on:click={() => seekTo(segment.startMs)} type="button">
                {segment.text}
              </button>

              {#if showExactWords && segment.words.length > 0}
                <div class="word-row">
                  {#each segment.words as word}
                    <button
                      class:active-word={segment.id === activeSegment?.id && word.id === activeWord?.id}
                      class="word"
                      on:click={() => seekTo(word.startMs)}
                      type="button"
                    >
                      {word.text}
                    </button>
                  {/each}
                </div>
              {/if}
            </article>
          {/each}
        </div>
      {/if}
    </main>
  </div>

  <footer class="player-dock">
    <div class="player-card panel">
      <div class="player-meta">
        <p class="eyebrow">Player</p>
        <strong>{activeDemoMeeting ? activeDemoMeeting.dateLabel : "Manual artifact"}</strong>
        <p class="player-hint">Space toggles play and pause.</p>
      </div>

      {#if audioSrc}
        <audio
          bind:this={audioEl}
          class="dock-audio"
          controls
          preload="metadata"
          src={audioSrc}
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
      {:else}
        <p class="muted">No audio source available for this artifact.</p>
      {/if}

      <div class="player-actions">
        <button on:click={toggleFollowPlayback} type="button">
          {#if followPlayback && manualScrollLock}
            Resume auto-scroll
          {:else if followPlayback}
            Auto-scroll: on
          {:else}
            Auto-scroll: off
          {/if}
        </button>
        {#if readableTranscript}
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

  .meeting-meta,
  .meeting-teaser {
    color: #665d51;
    font-size: 0.89rem;
    line-height: 1.45;
  }

  .meeting-teaser {
    display: -webkit-box;
    overflow: hidden;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 3;
  }

  .player-actions button,
  .speaker-tag,
  .time-chip,
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
  .word:hover {
    transform: translateY(-1px);
    border-color: rgba(130, 96, 42, 0.4);
  }

  .player-actions button {
    padding: 0.68rem 0.82rem;
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

  .word-row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.42rem;
    margin-top: 0.8rem;
    padding-top: 0.75rem;
    border-top: 1px solid rgba(84, 78, 55, 0.1);
  }

  .word {
    padding: 0.38rem 0.54rem;
    line-height: 1.2;
    font-size: 0.92rem;
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

  .dock-audio {
    width: 100%;
    min-width: 0;
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

    .player-actions {
      justify-content: flex-start;
    }
  }
</style>
