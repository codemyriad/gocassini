<script lang="ts">
  import { onDestroy, onMount, tick } from "svelte";
  import { fade, fly } from "svelte/transition";
  import { cubicInOut, cubicOut, quintOut } from "svelte/easing";
  import { marked } from "marked";
  import DOMPurify from "dompurify";
  import { Sun, Moon, Play, Pause, Keyboard, Calendar, Clock, Users, ArrowLeft, CassetteTape } from "@lucide/svelte";
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
    switchPortableTranscript,
    type ArtifactMetadata,
    type ArtifactMetadataRow,
    type ArtifactTimingPrecision,
    type LoadedArtifact,
    type PortableMeetingSummary,
  } from "./viewer/loadArtifact";
  import type { PortableTranscriptDescriptor } from "./viewer/portable";
  import {
    loadMeetingCatalog,
    sortMeetingCatalogEntries,
    type MeetingCatalogEntry,
  } from "./viewer/catalog";
  import { buildViewerHash, readViewerHash, viewerUrlWithHash } from "./viewer/hashRouting";

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

  type ThemeMode = "forrest-light" | "forrest-dark";
  const THEME_STORAGE_KEY = "cassini-theme";
  let themeMode: ThemeMode = "forrest-light";
  // theme + theme-switching live on the .cassini-root wrapper (data-theme /
  // class:theme-switching), NOT on document.documentElement — in the embedded
  // build the SPA renders inside a shadow root, so the document's <html> is
  // outside it and daisyUI's [data-theme] rule (scoped to the shadow stylesheet)
  // could never match it. The wrapper is in-tree in both builds.
  let themeSwitching = false;
  let prefersDarkMedia: MediaQueryList | null = null;

  let shortcutsDialog: HTMLDialogElement | null = null;
  function openShortcutsDialog() {
    shortcutsDialog?.showModal();
  }

  // The .cassini-root wrapper; used to resolve element lookups against the
  // component's ROOT NODE — the shadow root in the embedded build, the document
  // in standalone — since document.getElementById can't see shadow-tree nodes.
  let rootEl: HTMLElement | undefined;

  const DESKTOP_MEDIA_QUERY = "(min-width: 768px)";
  let isDesktop = false;
  let viewportMedia: MediaQueryList | null = null;

  let viewportChanging = false;

  async function handleViewportChange(event: MediaQueryListEvent) {
    viewportChanging = true;
    isDesktop = event.matches;
    await tick();
    viewportChanging = false;
  }

  let prefersReducedMotion = false;
  let reducedMotionMedia: MediaQueryList | null = null;
  let mountComplete = false;

  function handleReducedMotionChange(event: MediaQueryListEvent) {
    prefersReducedMotion = event.matches;
  }

  function regionFlyConfig(direction: -1 | 1) {
    if (prefersReducedMotion || isDesktop || !mountComplete || viewportChanging) {
      return { duration: 0 };
    }
    return {
      x: direction * window.innerWidth,
      duration: 600,
      easing: cubicInOut,
      opacity: 1,
    };
  }

  function contentFadeConfig() {
    return prefersReducedMotion ? { duration: 0 } : { duration: 180 };
  }

  function playerFadeConfig() {
    // cubicOut so the card materializes early in the transition rather than
    // snapping into view near the end — pure linear opacity reads as a
    // "click" when the card bg is close to the page bg.
    return prefersReducedMotion ? { duration: 0 } : { duration: 320, easing: cubicOut };
  }

  function contentFlyInConfig() {
    // Load-in only: slide content up from below while fading in (svelte/fly
    // animates opacity by default). quintOut gives a snappy start with a
    // pronounced deceleration so the content settles into place rather than
    // drifting. Load-out keeps a plain fade so the previous content doesn't
    // visibly travel downward when switching.
    return prefersReducedMotion ? { duration: 0 } : { y: 120, duration: 560, easing: quintOut };
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

  function handleBackToList() {
    pushMeetingUrl("");
    resetLoadedArtifact();
    activeMeeting = null;
    errorMessage = "";
  }

  function pushMeetingUrl(meetingId: string) {
    // tx applies to a specific meeting; dropping it (we never carry it forward)
    // means the next meeting opens on its producer-default transcript.
    window.history.pushState({}, "", viewerHref(buildViewerHash({ meeting: meetingId })));
  }

  function seedListHistoryEntry(meetingId: string) {
    // The "list" entry below the current page in the back stack never carries
    // a transcript selection; tx is per-meeting state.
    window.history.replaceState({}, "", viewerHref(buildViewerHash({})));
    window.history.pushState({}, "", viewerHref(buildViewerHash({ meeting: meetingId })));
  }

  function handlePopState() {
    const { meeting: urlMeetingId } = currentViewerHash();
    if (urlMeetingId === selectedMeetingId) {
      return;
    }
    if (urlMeetingId) {
      const meeting = catalogMeetings.find((entry) => entry.id === urlMeetingId);
      if (meeting) {
        pendingSeekMs = parseTimeHash(window.location.hash);
        void loadMeetingArtifact(meeting);
      } else {
        resetLoadedArtifact();
        activeMeeting = null;
        selectedMeetingId = urlMeetingId;
        errorMessage = `Meeting not found in catalog: ${urlMeetingId}`;
      }
    } else {
      resetLoadedArtifact();
      activeMeeting = null;
      errorMessage = "";
    }
  }

  function readStoredTheme(): ThemeMode | null {
    try {
      const value = localStorage.getItem(THEME_STORAGE_KEY);
      return value === "forrest-light" || value === "forrest-dark" ? value : null;
    } catch {
      return null;
    }
  }

  function applyTheme(mode: ThemeMode) {
    // data-theme is bound on the .cassini-root wrapper; updating themeMode is
    // all that's needed (no document.documentElement — it's outside the shadow
    // root in the embedded build).
    themeMode = mode;
  }

  function setTheme(mode: ThemeMode) {
    // Suppress per-element transitions during the theme swap so the
    // attribute change applies in a single paint instead of cascading
    // animations across every transcript token. theme-switching is bound on the
    // .cassini-root wrapper via class:theme-switching.
    themeSwitching = true;
    applyTheme(mode);
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        themeSwitching = false;
      });
    });
    try {
      localStorage.setItem(THEME_STORAGE_KEY, mode);
    } catch {
      // localStorage unavailable; in-memory state still applies
    }
  }

  function toggleTheme() {
    setTheme(themeMode === "forrest-dark" ? "forrest-light" : "forrest-dark");
  }

  function handlePrefersColorSchemeChange(event: MediaQueryListEvent) {
    if (readStoredTheme() === null) {
      applyTheme(event.matches ? "forrest-dark" : "forrest-light");
    }
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
  $: playbackProgress = clampedDurationMs > 0 ? (clampedCurrentTimeMs / clampedDurationMs) * 100 : 0;
  $: documentTitle = activeMeeting
    ? `${activeMeeting.title} | Cassini Viewer`
    : catalogMeetings.length > 0
      ? "Cassini Meetings"
      : "Cassini Viewer";
  $: speakerNames = speakers.map((s) => s.label || s.id).filter(Boolean);
  $: mastheadSummary =
    transcriptIndex !== null
      ? [
          activeMeeting ? formatMeetingDate(activeMeeting.dateLabel) : null,
          formatClockTime(clampedDurationMs),
          speakerNames.length > 0 ? speakerNames.join(", ") : null,
        ]
          .filter(Boolean)
          .join(" · ")
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
    window.addEventListener("popstate", handlePopState);
    // Back/forward across hash-only history entries fires popstate; some paths
    // (e.g. manual hash edits, or browsers that only emit hashchange for
    // hash-only nav) surface as hashchange. handlePopState early-returns when
    // the hash meeting already matches, so handling both is idempotent.
    window.addEventListener("hashchange", handlePopState);
    const stored = readStoredTheme();
    if (stored !== null) {
      applyTheme(stored);
    } else if (typeof window.matchMedia === "function") {
      prefersDarkMedia = window.matchMedia("(prefers-color-scheme: dark)");
      applyTheme(prefersDarkMedia.matches ? "forrest-dark" : "forrest-light");
      prefersDarkMedia.addEventListener("change", handlePrefersColorSchemeChange);
    } else {
      applyTheme("forrest-light");
    }
    if (typeof window.matchMedia === "function") {
      viewportMedia = window.matchMedia(DESKTOP_MEDIA_QUERY);
      isDesktop = viewportMedia.matches;
      viewportMedia.addEventListener("change", handleViewportChange);
      reducedMotionMedia = window.matchMedia("(prefers-reduced-motion: reduce)");
      prefersReducedMotion = reducedMotionMedia.matches;
      reducedMotionMedia.addEventListener("change", handleReducedMotionChange);
    }
    pendingSeekMs = parseTimeHash(window.location.hash);
    const initialMeetingId = currentViewerHash().meeting || null;
    const viewerConfig = window as typeof window & {
      __CASSINI_VIEWER_ARTIFACT_MODE__?: string;
    };
    const preferBundledArtifact = viewerConfig.__CASSINI_VIEWER_ARTIFACT_MODE__ === "bundled";
    try {
      if (!preferBundledArtifact) {
        const catalog = await loadMeetingCatalog();
        // A successfully-loaded catalog — even an empty one (fresh install,
        // meetings: []) — means catalog/list mode. Only fall through to the
        // single bundled-artifact path when there is NO catalog at all (null),
        // e.g. a standalone single-meeting export. Treating an empty catalog as
        // "no catalog" would resolve bundled fallback files against the proxy
        // root and surface a load error instead of an empty meeting list.
        if (catalog) {
          catalogMeetings = catalog.meetings;
          void hydrateCatalogMeetingMetadata(catalog.meetings);
          const requested = initialMeetingId
            ? catalog.meetings.find((meeting) => meeting.id === initialMeetingId) ?? null
            : null;
          const selected =
            requested ??
            (initialMeetingId === null && catalog.meetings.length === 1
              ? catalog.meetings[0]
              : null);
          if (selected) {
            const loaded = await loadMeetingArtifact(selected);
            if (loaded) {
              seedListHistoryEntry(loaded.id);
            }
          } else if (initialMeetingId) {
            selectedMeetingId = initialMeetingId;
            errorMessage = `Meeting not found in catalog: ${initialMeetingId}`;
            seedListHistoryEntry(initialMeetingId);
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
      mountComplete = true;
    }
  });

  onDestroy(() => {
    window.removeEventListener("keydown", handleWindowKeydown);
    window.removeEventListener("popstate", handlePopState);
    window.removeEventListener("hashchange", handlePopState);
    prefersDarkMedia?.removeEventListener("change", handlePrefersColorSchemeChange);
    viewportMedia?.removeEventListener("change", handleViewportChange);
    reducedMotionMedia?.removeEventListener("change", handleReducedMotionChange);
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
    selectedMeetingId = "";
  }

  async function handleTranscriptSwitch(targetId: string) {
    if (
      transcriptSwitchPending ||
      !activeMeeting?.audioPath ||
      targetId === currentTranscriptId ||
      !availableTranscripts.some((entry) => entry.id === targetId)
    ) {
      return;
    }
    transcriptSwitchPending = true;
    transcriptSwitchError = "";
    try {
      const next = await switchPortableTranscript(activeMeeting.audioPath, targetId);
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

  async function maybeApplyUrlTranscript(meeting: MeetingCatalogEntry) {
    const requested = currentViewerHash().tx;
    if (!requested || !meeting.audioPath) {
      return;
    }
    if (!availableTranscripts.some((entry) => entry.id === requested)) {
      clearTranscriptUrlParam();
      return;
    }
    if (requested === currentTranscriptId) {
      return;
    }
    transcriptSwitchPending = true;
    try {
      const next = await switchPortableTranscript(meeting.audioPath, requested);
      applySwitchedTranscript(next);
    } catch {
      clearTranscriptUrlParam();
    } finally {
      transcriptSwitchPending = false;
    }
  }

  async function loadMeetingArtifact(
    meeting: MeetingCatalogEntry,
  ): Promise<MeetingCatalogEntry | null> {
    // Drop stale artifact + select the new meeting synchronously so the
    // viewer immediately shows a loading state for *this* meeting rather
    // than the previous meeting's masthead/transcript bleeding through.
    resetLoadedArtifact();
    selectedMeetingId = meeting.id;
    activeMeeting = meeting;
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
      await maybeApplyUrlTranscript(enrichedMeeting);
      return enrichedMeeting;
    } catch (error) {
      resetLoadedArtifact();
      activeMeeting = null;
      selectedMeetingId = "";
      errorMessage = error instanceof Error ? error.message : String(error);
      return null;
    } finally {
      loading = false;
    }
  }

  async function loadCatalogMeeting(meeting: MeetingCatalogEntry) {
    pushMeetingUrl(meeting.id);
    await loadMeetingArtifact(meeting);
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
    const root = rootEl?.getRootNode() as Document | ShadowRoot | undefined;
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

  function metadataSectionStartsOpen(_title: string): boolean {
    return false;
  }

  // Catalog `dateLabel` ships as ISO-ish "YYYY-MM-DD" or "YYYY-MM-DD HH:MM".
  // Render it in the user's locale (e.g. "Fri, Mar 13, 2026, 12:29 PM").
  function formatMeetingDate(dateLabel: string): string {
    const match = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2}))?/.exec(dateLabel);
    if (!match) {
      return dateLabel;
    }
    const [, y, m, d, h, mn] = match;
    const hasTime = h !== undefined && mn !== undefined;
    const date = new Date(
      Number(y),
      Number(m) - 1,
      Number(d),
      hasTime ? Number(h) : 0,
      hasTime ? Number(mn) : 0,
    );
    if (Number.isNaN(date.getTime())) {
      return dateLabel;
    }
    return new Intl.DateTimeFormat(undefined, {
      weekday: "short",
      month: "short",
      day: "numeric",
      year: "numeric",
      ...(hasTime
        ? { hour: "numeric", minute: "2-digit", timeZoneName: "short" }
        : {}),
    }).format(date);
  }

  // Compact variant of formatMeetingDate for places with little space
  // (e.g. catalog cards). "13 Mar 2026" — day, short month, year.
  // Locale-pinned to en-GB so the day-month-year order is consistent
  // regardless of the viewer's browser locale.
  function formatMeetingDateShort(dateLabel: string): string {
    const match = /^(\d{4})-(\d{2})-(\d{2})/.exec(dateLabel);
    if (!match) {
      return dateLabel;
    }
    const [, y, m, d] = match;
    const date = new Date(Number(y), Number(m) - 1, Number(d));
    if (Number.isNaN(date.getTime())) {
      return dateLabel;
    }
    return new Intl.DateTimeFormat("en-GB", {
      day: "numeric",
      month: "short",
      year: "numeric",
    }).format(date);
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

<!-- .cassini-root carries the daisyUI theme (data-theme) and the theme-switching
     transition-suppression for the whole app. It lives here (in-tree), not on
     document.documentElement, so it works inside the embedded build's shadow
     root. The <dialog> below is wrapped too so [data-theme] styles it (showModal
     keeps it in-tree for inheritance even when promoted to the top layer). -->
<div bind:this={rootEl} class="cassini-root" data-theme={themeMode} class:theme-switching={themeSwitching}>
<div class="grid grid-cols-1 md:grid-cols-[400px_1fr] h-full bg-base-200 overflow-x-clip">
  {#if isDesktop || !selectedMeetingId}
    <section
      aria-label="Meeting list"
      class="meeting-list flex flex-col row-start-1 col-start-1 h-full md:sticky md:top-0 bg-base-100 backdrop-blur-xl border-r border-base-300"
      transition:fly={regionFlyConfig(-1)}
    >
      <!-- Scroll container holds the sticky header so cards visibly scroll
           behind its blur. Scrollbar runs the full sidebar height as a
           consequence (the only way to get the scroll-behind effect). -->
      <div class="flex-1 min-h-0 overflow-y-auto scroll-stable">
        <header class="sticky top-0 z-20 flex items-center justify-between gap-3 px-4 py-3 border-b bg-base-100 border-base-300 md:border-none md:bg-base-100/50 backdrop-blur-lg">
          <h2 class="text-base font-bold text-base-content">
            Meetings
          </h2>
          <label class="flex items-center gap-1.5 cursor-pointer">
            <Sun size={16} strokeWidth={2} class="text-base-content/80" aria-hidden="true" />
            <input
              type="checkbox"
              class="toggle toggle-sm rounded-lg text-base-content/80"
              aria-label="Toggle light or dark theme"
              checked={themeMode === "forrest-dark"}
              on:change={toggleTheme}
            />
            <Moon size={16} strokeWidth={2} class="text-base-content/80" aria-hidden="true" />
          </label>
        </header>
        {#if catalogMeetings.length > 0}
          <div class="grid gap-3 p-4">
            {#each catalogMeetings as meeting}
              <button
                on:click={() => loadCatalogMeeting(meeting)}
                type="button"
                aria-current={meeting.id === selectedMeetingId ? "page" : undefined}
                class="
                  grid gap-1 w-full p-3 text-left rounded-lg border cursor-pointer transition-all duration-100
                  border-base-300 bg-base-100/50
                  hover:bg-base-200
                  aria-[current=page]:border-primary
                  aria-[current=page]:bg-primary/25
                "
              >
                <span class="font-bold">{meeting.title}</span>
                <div class="flex flex-wrap items-center gap-4 text-base-content/70 text-sm">
                  <span class="badge gap-1.5 px-0 bg-transparent border-transparent">
                    <Calendar size={14} aria-hidden="true" />
                    {formatMeetingDateShort(meeting.dateLabel)}
                  </span>
                  {#if typeof meeting.digestDurationMs === "number"}
                    <span class="badge gap-1.5 px-0 bg-transparent border-transparent tabular-nums">
                      <Clock size={14} aria-hidden="true" />
                      {formatClockTime(meeting.digestDurationMs)}
                    </span>
                  {/if}
                  {#if typeof meeting.speakerCount === "number"}
                    <span class="badge gap-1.5 px-0 bg-transparent border-transparent">
                      <Users size={14} aria-hidden="true" />
                      {meeting.speakerCount}
                    </span>
                  {/if}
                </div>
              </button>
            {/each}
          </div>
        {/if}
      </div>

      <!-- Sticky footer: load note (only renders when there's an error) -->
      {#if errorMessage && !selectedMeetingId}
        <footer class="flex-none flex flex-col gap-3 px-4 py-4">
          <section class="alert alert-warning items-start">
            <div>
              <h2 class="text-xs font-bold uppercase tracking-widest mb-1">Load note</h2>
              <p>{errorMessage}</p>
            </div>
          </section>
        </footer>
      {/if}
    </section>
  {/if}

  {#if isDesktop || selectedMeetingId}
    <section
      aria-label="Meeting view"
      class="meeting-viewer relative flex flex-col row-start-1 col-start-1 md:col-start-2 h-full"
      transition:fly={regionFlyConfig(1)}
    >
      <!-- Scroll container: holds the sticky header and all main content.
           Bottom padding keeps the last lines of the transcript reachable
           even when the (absolutely positioned) player overlaps the scroll.
           `scrollbar-gutter: stable` reserves the scrollbar gutter persistently
           so content width never shifts as scrollbar appears/disappears. -->
      <div class="flex-1 min-h-0 overflow-y-auto pb-40 md:pb-32 scroll-stable flex flex-col">
        <!-- Sticky header — translucent bg so the transcript scrolls behind it.
             Using base-100 (not base-200) so the header reads distinct from the
             page bg even when there's no transcript content behind it. -->
        <header class="sticky top-0 z-20 flex-none flex items-center gap-3 min-h-12 px-4 py-3 bg-base-200 border-b border-base-300 md:border-none md:bg-base-200/50 backdrop-blur-lg">
        {#if !isDesktop}
          <button
            on:click={handleBackToList}
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

      {#if !transcriptIndex && selectedMeetingId && errorMessage}
        <div class="grid place-items-center flex-1 p-4">
          <div class="card bg-base-100 w-full max-w-md border border-base-300">
            <div class="card-body items-center text-center">
              <h2 class="text-lg font-bold">Meeting not found</h2>
              <p class="text-base-content">{errorMessage}</p>
            </div>
          </div>
        </div>
      {:else if transcriptIndex}
      <div in:fly={contentFlyInConfig()} out:fade={contentFadeConfig()}>
      <header class="m-4 mb-8 md:mx-8 md:mb-0 min-w-0">
        <h1 class="text-3xl font-bold mb-3">
          {activeMeeting
            ? activeMeeting.title
            : "Meeting transcript viewer"}
        </h1>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-2 mt-2 text-base-content/70 text-sm">
          {#if transcriptIndex && activeMeeting}
            <span class="badge badge-ghost gap-1.5 px-0">
              <Calendar size={14} aria-hidden="true" />
              {formatMeetingDate(activeMeeting.dateLabel)}
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

      <main class="flex flex-col gap-3.5 m-4 md:m-8">
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
                          : 'hover:bg-primary'}"
                        on:click={() => seekTo(word.startMs)}
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

      {#if loading && selectedMeetingId}
        <div
          class="absolute inset-0 z-10 grid place-items-center bg-base-200 pointer-events-none"
          transition:fade={contentFadeConfig()}
        >
          <div class="flex flex-col items-center gap-3 text-base-content">
            <span class="cassini-spinner text-primary" aria-hidden="true"></span>
            <p class="text-base">Loading meeting…</p>
          </div>
        </div>
      {:else if !transcriptIndex && !selectedMeetingId && isDesktop}
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
        class="absolute bottom-0 left-0 right-0 md:right-[15px] z-30 p-2 md:px-4 md:pb-4 pointer-events-none [will-change:opacity]"
        transition:fade={playerFadeConfig()}
      >
        <div class="card bg-base-100 shadow-2xl p-2 border border-base-300 pointer-events-auto relative">
          {#if audioSrc}
            {#key selectedMeetingId}
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
  {/if}
</div>

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
</div><!-- /.cassini-root -->

