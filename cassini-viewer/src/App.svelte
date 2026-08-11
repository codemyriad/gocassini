<script lang="ts">
  import { onDestroy, onMount, tick } from "svelte";
  import { cubicOut } from "svelte/easing";
  import type { PortableMeetingSummary } from "./viewer/loadArtifact";
  import {
    sortMeetingCatalogEntries,
    type MeetingCatalogEntry,
  } from "./viewer/catalog";
  import { StaticCatalogProvider, type DataProvider } from "./viewer/dataProvider";
  import { isEmbeddedViewer } from "./viewer/appBase";
  import { resolveCatalogSelection } from "./viewer/catalogSelection";
  import { buildViewerHash, readViewerHash, viewerUrlWithHash } from "./viewer/hashRouting";
  import MeetingList from "./components/MeetingList.svelte";
  import MeetingView from "./components/MeetingView.svelte";

  // The shell (D-420): owns the catalog/list, which meeting is selected, the
  // `meeting` hash param, theme, and responsive state. The two surfaces —
  // MeetingList (left) and MeetingView (right) — are fed from here. MeetingView
  // owns artifact loading, playback, transcript switching, and the tx/t params.

  // ncMode is true when embedded in Nextcloud and OCA.Theming was detected by
  // embedded.ts. Theme toggle is hidden and localStorage/prefers-color-scheme
  // init is skipped — NC colour prefs are applied via CSS in app.css.
  export let ncMode: boolean = false;

  // dataProvider is the seam between the app and its data source (D-415). It
  // defaults to a StaticCatalogProvider so existing mounts/tests that don't pass
  // one keep working unchanged; main.ts / embedded.ts construct and pass one
  // explicitly. It is threaded down to MeetingView for artifact loads.
  export let dataProvider: DataProvider = new StaticCatalogProvider();

  let catalogMeetings: MeetingCatalogEntry[] = [];
  let selectedMeetingId = "";
  let bundledMode = false;
  // Shell-level errors: listError = catalog load failure (shown in the list
  // footer); notFoundMessage = a selected meeting id that isn't in the catalog
  // (shown in the meeting-view not-found card).
  let listError = "";
  let notFoundMessage = "";
  let catalogHydrationGeneration = 0;
  // pendingMeetingId holds a deep-linked meeting id that arrived before any
  // catalog did. It is NOT selectedMeetingId: selecting an id whose entry does
  // not exist yet mounts MeetingView on mobile with nothing to show, which is
  // how the stale "Meeting not found" card got on screen in the first place
  // (D-543). It is consumed once, by whichever load first observes a catalog.
  let pendingMeetingId: string | null = null;
  // destroyed guards every post-await mutation. Both awaits below can resolve
  // after unmount — the shell recreates the viewer whenever its admin probe
  // settles — and the mount path in particular writes browser history, which a
  // destroyed component has no business doing (D-543).
  let destroyed = false;
  // embedded is read once: whether this app runs inside the ExApp shell. See
  // isEmbeddedViewer — this is deliberately not ncMode, which only reports
  // whether Nextcloud Theming was detected.
  const embedded = isEmbeddedViewer();
  const CATALOG_REFRESH_INTERVAL_MS = 15_000;
  let catalogMode = false;
  let catalogRefreshRunning = false;
  let catalogRefreshTimer: number | undefined;

  type ThemeMode = "saturn-light" | "saturn-dark";
  const THEME_STORAGE_KEY = "cassini-theme";
  let themeMode: ThemeMode = "saturn-light";
  // theme + theme-switching live on the .cassini-root wrapper (data-theme /
  // class:theme-switching), NOT on document.documentElement — in the embedded
  // build the SPA renders inside a shadow root, so the document's <html> is
  // outside it and daisyUI's [data-theme] rule (scoped to the shadow stylesheet)
  // could never match it. The wrapper is in-tree in both builds.
  let themeSwitching = false;
  let prefersDarkMedia: MediaQueryList | null = null;

  // The .cassini-root wrapper carries the daisyUI theme and hosts both surfaces.
  let rootEl: HTMLElement | undefined;

  const DESKTOP_MEDIA_QUERY = "(min-width: 981px)";
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
      duration: 280,
      easing: cubicOut,
      opacity: 0,
    };
  }

  // Routing is hash-only (see src/viewer/hashRouting.ts for why and the wire
  // format). The shell owns ONLY the `meeting` param; MeetingView owns tx/t.
  // These thin wrappers bind the pure helpers to the live location.
  function currentViewerHash() {
    return readViewerHash(window.location.hash);
  }

  function viewerHref(hash: string): string {
    return viewerUrlWithHash(window.location.href, hash);
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

  function handleBackToList() {
    pushMeetingUrl("");
    selectedMeetingId = "";
    notFoundMessage = "";
  }

  function handlePopState() {
    const { meeting: urlMeetingId } = currentViewerHash();
    if (urlMeetingId === selectedMeetingId) {
      return;
    }
    notFoundMessage = "";
    if (urlMeetingId) {
      const found = catalogMeetings.find((entry) => entry.id === urlMeetingId);
      selectedMeetingId = urlMeetingId;
      if (!found) {
        notFoundMessage = `Meeting not found in catalog: ${urlMeetingId}`;
      }
    } else {
      selectedMeetingId = "";
    }
  }

  function loadCatalogMeeting(meeting: MeetingCatalogEntry) {
    notFoundMessage = "";
    pushMeetingUrl(meeting.id);
    selectedMeetingId = meeting.id;
  }

  // MeetingView reports the real speaker/segment/duration counts once a meeting
  // is loaded; fold them into the catalog so the list card shows actuals. The
  // derived `selectedMeeting` then carries the enriched entry back into
  // MeetingView (same id → guarded against a reload).
  function handleEnriched(event: CustomEvent<MeetingCatalogEntry>) {
    const enriched = event.detail;
    catalogMeetings = catalogMeetings.map((entry) =>
      entry.id === enriched.id ? enriched : entry,
    );
  }

  function readStoredTheme(): ThemeMode | null {
    try {
      const value = localStorage.getItem(THEME_STORAGE_KEY);
      return value === "saturn-light" || value === "saturn-dark" ? value : null;
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
    setTheme(themeMode === "saturn-dark" ? "saturn-light" : "saturn-dark");
  }

  function handlePrefersColorSchemeChange(event: MediaQueryListEvent) {
    if (readStoredTheme() === null) {
      applyTheme(event.matches ? "saturn-dark" : "saturn-light");
    }
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
        const summary = await dataProvider.loadMeetingSummary(meeting);
        if (generation !== catalogHydrationGeneration) {
          return;
        }
        if (summary) {
          applyMeetingSummary(meeting.id, summary);
        }
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
  }

  async function refreshCatalog() {
    // Embedded mode keeps probing even when no catalog existed at mount time:
    // a fresh install can stay open while its first recording is published.
    if ((!catalogMode && !embedded) || catalogRefreshRunning) {
      return;
    }
    catalogRefreshRunning = true;
    try {
      const catalog = await dataProvider.loadCatalog();
      if (destroyed || !catalog) {
        return;
      }
      catalogMode = true;
      bundledMode = false;
      catalogMeetings = catalog.meetings;
      listError = "";
      void hydrateCatalogMeetingMetadata(catalog.meetings);
      // A deep link that could not be satisfied at mount is satisfied here, the
      // first time a catalog actually arrives.
      if (!selectedMeetingId && pendingMeetingId !== null) {
        applyCatalogSelection(catalog.meetings, pendingMeetingId);
      } else if (selectedMeetingId) {
        notFoundMessage = catalog.meetings.some((entry) => entry.id === selectedMeetingId)
          ? ""
          : `Meeting not found in catalog: ${selectedMeetingId}`;
      }
      pendingMeetingId = null;
    } catch (error) {
      if (destroyed) {
        return;
      }
      // Preserve the last known-good list while surfacing the refresh failure.
      listError = error instanceof Error ? error.message : String(error);
    } finally {
      catalogRefreshRunning = false;
    }
  }

  // applyCatalogSelection commits what resolveCatalogSelection decided. Mount
  // and refresh share it so they cannot drift — before this, only mount knew
  // how to auto-open a single-meeting catalog.
  function applyCatalogSelection(
    meetings: MeetingCatalogEntry[],
    requestedMeetingId: string | null,
  ) {
    const selection = resolveCatalogSelection(meetings, requestedMeetingId);
    if (!selection.selectedMeetingId) {
      return;
    }
    selectedMeetingId = selection.selectedMeetingId;
    notFoundMessage = selection.notFoundMessage;
    if (selection.seedHistory) {
      seedListHistoryEntry(selection.selectedMeetingId);
    }
  }

  function refreshCatalogWhenVisible() {
    if (document.visibilityState === "visible") {
      void refreshCatalog();
    }
  }

  $: selectedMeeting = selectedMeetingId
    ? catalogMeetings.find((entry) => entry.id === selectedMeetingId) ?? null
    : null;
  $: documentTitle = selectedMeeting
    ? `${selectedMeeting.title} | Cassini Viewer`
    : catalogMeetings.length > 0
      ? "Cassini Meetings"
      : "Cassini Viewer";

  onMount(async () => {
    window.addEventListener("popstate", handlePopState);
    // Back/forward across hash-only history entries fires popstate; some paths
    // (e.g. manual hash edits, or browsers that only emit hashchange for
    // hash-only nav) surface as hashchange. handlePopState early-returns when
    // the hash meeting already matches, so handling both is idempotent.
    window.addEventListener("hashchange", handlePopState);
    if (!ncMode) {
      const stored = readStoredTheme();
      if (stored !== null) {
        applyTheme(stored);
      } else if (typeof window.matchMedia === "function") {
        prefersDarkMedia = window.matchMedia("(prefers-color-scheme: dark)");
        applyTheme(prefersDarkMedia.matches ? "saturn-dark" : "saturn-light");
        prefersDarkMedia.addEventListener("change", handlePrefersColorSchemeChange);
      } else {
        applyTheme("saturn-light");
      }
    }
    if (embedded) {
      // The embedded app can remain mounted while a Talk recording publishes
      // in another tab. Refresh on return and periodically so the new catalog
      // entry appears without a hard browser reload. Gated on `embedded`, not
      // ncMode: an ExApp with Theming off still needs this, and it is the only
      // thing that recovers a catalog that was absent at mount (D-543).
      window.addEventListener("focus", refreshCatalogWhenVisible);
      document.addEventListener("visibilitychange", refreshCatalogWhenVisible);
      catalogRefreshTimer = window.setInterval(
        refreshCatalogWhenVisible,
        CATALOG_REFRESH_INTERVAL_MS,
      );
    }
    if (typeof window.matchMedia === "function") {
      viewportMedia = window.matchMedia(DESKTOP_MEDIA_QUERY);
      isDesktop = viewportMedia.matches;
      viewportMedia.addEventListener("change", handleViewportChange);
      reducedMotionMedia = window.matchMedia("(prefers-reduced-motion: reduce)");
      prefersReducedMotion = reducedMotionMedia.matches;
      reducedMotionMedia.addEventListener("change", handleReducedMotionChange);
    }
    const initialMeetingId = currentViewerHash().meeting || null;
    const viewerConfig = window as typeof window & {
      __CASSINI_VIEWER_ARTIFACT_MODE__?: string;
    };
    const preferBundledArtifact = viewerConfig.__CASSINI_VIEWER_ARTIFACT_MODE__ === "bundled";
    try {
      if (!preferBundledArtifact) {
        const catalog = await dataProvider.loadCatalog();
        if (destroyed) {
          return;
        }
        // A successfully-loaded catalog — even an empty one (fresh install,
        // meetings: []) — means catalog/list mode. Only fall through to the
        // single bundled-artifact path when there is NO catalog at all (null),
        // e.g. a standalone single-meeting export. Treating an empty catalog as
        // "no catalog" would resolve bundled fallback files against the proxy
        // root and surface a load error instead of an empty meeting list.
        if (catalog) {
          catalogMode = true;
          catalogMeetings = catalog.meetings;
          void hydrateCatalogMeetingMetadata(catalog.meetings);
          applyCatalogSelection(catalog.meetings, initialMeetingId);
          return;
        }
        // No catalog, and we are embedded: there is no bundled artifact in an
        // ExApp build to fall back to, so stay in list mode and let the refresh
        // above recover. A null catalog here is a normal transient — a fresh
        // install, or any Nextcloud hiccup on the mount fetch — not a signal to
        // switch modes. Remember the deep link so the recovery can honour it
        // (D-543).
        if (embedded) {
          pendingMeetingId = initialMeetingId;
          return;
        }
      }
      bundledMode = true;
    } catch (error) {
      if (destroyed) {
        return;
      }
      listError = error instanceof Error ? error.message : String(error);
    } finally {
      if (!destroyed) {
        mountComplete = true;
      }
    }
  });

  onDestroy(() => {
    // Set before anything else: in-flight awaits check this to decide whether
    // they may still touch component state or browser history.
    destroyed = true;
    window.removeEventListener("popstate", handlePopState);
    window.removeEventListener("hashchange", handlePopState);
    window.removeEventListener("focus", refreshCatalogWhenVisible);
    document.removeEventListener("visibilitychange", refreshCatalogWhenVisible);
    if (catalogRefreshTimer !== undefined) {
      window.clearInterval(catalogRefreshTimer);
    }
    catalogHydrationGeneration += 1;
    prefersDarkMedia?.removeEventListener("change", handlePrefersColorSchemeChange);
    viewportMedia?.removeEventListener("change", handleViewportChange);
    reducedMotionMedia?.removeEventListener("change", handleReducedMotionChange);
  });
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
     root. MeetingView's shortcuts <dialog> is inside this wrapper too so
     [data-theme] styles it. -->
<div bind:this={rootEl} class="cassini-root" data-theme={themeMode} class:theme-switching={themeSwitching}>
<div class="grid grid-cols-1 min-[981px]:grid-cols-[400px_1fr] grid-rows-1 h-full bg-base-200 overflow-x-clip">
  {#if isDesktop || !selectedMeetingId}
    <MeetingList
      meetings={catalogMeetings}
      {selectedMeetingId}
      {ncMode}
      {themeMode}
      errorMessage={listError}
      flyParams={regionFlyConfig(-1)}
      on:select={(event) => loadCatalogMeeting(event.detail)}
      on:toggleTheme={toggleTheme}
    />
  {/if}

  <!-- bundledMode is in the condition because a standalone single-meeting
       export has neither a desktop viewport nor a selected id, and without it
       a phone-sized window renders an empty list and no meeting at all. -->
  {#if isDesktop || selectedMeetingId || bundledMode}
    <MeetingView
      {dataProvider}
      meeting={selectedMeeting}
      bundled={bundledMode}
      {isDesktop}
      {prefersReducedMotion}
      flyParams={regionFlyConfig(1)}
      hasCatalog={catalogMeetings.length > 0}
      {notFoundMessage}
      on:back={handleBackToList}
      on:enriched={handleEnriched}
    />
  {/if}
</div>
</div><!-- /.cassini-root -->
