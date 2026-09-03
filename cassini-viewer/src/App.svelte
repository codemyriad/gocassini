<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { cubicOut } from "svelte/easing";
  import { fade } from "svelte/transition";
  import type { PortableMeetingSummary } from "./viewer/loadArtifact";
  import {
    sortMeetingCatalogEntries,
    type MeetingCatalogEntry,
  } from "./viewer/catalog";
  import { StaticCatalogProvider, type DataProvider } from "./viewer/dataProvider";
  import { isEmbeddedViewer } from "./viewer/appBase";
  import { resolveCatalogSelection } from "./viewer/catalogSelection";
  import { buildViewerHash, readViewerHash, viewerUrlWithHash } from "./viewer/hashRouting";
  import {
    buildRoomBuckets,
    filterMeetingsByRoom,
    type RoomBucket,
  } from "./viewer/rooms";
  import {
    EMPTY_SELECTION,
    acknowledgeDropped,
    clearSelection,
    countHiddenByView,
    describeSelectionGaps,
    reconcileSelection,
    selectedEntries,
    shouldShowSelectionBar,
    summarizeSelection,
    toggleSelected,
    type MeetingSelection,
  } from "./viewer/selectionModel";
  import {
    filterInsightsByRoom,
    insightHeadline,
    insightsForMeeting,
    resolveInsightSources,
    type InsightRecord,
  } from "./viewer/insights";
  import InsightDocument from "./components/InsightDocument.svelte";
  import MeetingList from "./components/MeetingList.svelte";
  import MeetingView from "./components/MeetingView.svelte";
  import PreparePanel from "./components/PreparePanel.svelte";
  import RoomsRail from "./components/RoomsRail.svelte";
  import SelectionBar from "./components/SelectionBar.svelte";

  // The shell (D-420, re-laid-out in D-654): owns the catalog/list, which room
  // is selected, which meeting is open, the `meeting` hash param, theme, and
  // responsive state. Three surfaces are fed from here — RoomsRail (left),
  // MeetingList (centre) and MeetingView, which now opens as a sheet over the
  // list rather than as a second column. MeetingView still owns artifact
  // loading, playback, transcript switching, and the tx/t params.
  //
  // The sheet is positioned against THIS component's shell, not the viewport:
  // in the embedded build the viewer renders inside a Nextcloud page, and a
  // position:fixed sheet would cover Nextcloud's own chrome and the app shell's
  // Browse/Operator nav along with the list it means to cover.

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

  // Which room the list is narrowed to (D-654). null is "all meetings" — not
  // the same as rooms.NO_ROOM_KEY, which selects the meetings that have no room
  // at all. List-local like the text filter: it is a way of looking at the
  // archive, not a location, so it stays out of the hash and out of history.
  let selectedRoomKey: string | null = null;
  // Narrow viewports only: whether the rail is slid in over the list.
  let railOpen = false;

  // Which meetings are PICKED for a context bundle, and whether the Prepare
  // panel is open over the list (D-626). Transient UI state, deliberately not
  // routed: the hash router rebuilds the fragment from {meeting, tx, timeMs} on
  // every viewer navigation, so anything else put there is destroyed by the
  // next click — and a selection is a thing you are doing, not a place you are.
  let selection: MeetingSelection = EMPTY_SELECTION;
  let prepareOpen = false;
  // What the list is actually showing, reported by MeetingList: its text filter
  // is list-local, so this is the only way the shell can say how many picked
  // meetings the current narrowing hides.
  let visibleMeetings: MeetingCatalogEntry[] = [];

  // The caller's own insight runs (D-721), and which one the sheet is holding.
  //
  // Three states, kept apart on purpose: `insightsLoaded` says a listing has
  // come back at least once, `insightsError` says the last one did not, and
  // neither of them is "there are no insights". The list is told all three
  // because "we could not ask" and "there are none" look identical otherwise.
  let insights: InsightRecord[] = [];
  let insightsLoaded = false;
  let insightsError = "";
  let insightsRefreshRunning = false;
  // Deliberately NOT routed, for the reason the Prepare panel is not:
  // buildViewerHash rebuilds the fragment from {meeting, tx, timeMs} on every
  // viewer navigation, so an `insight=` param would be destroyed by the next
  // click. Addressing an insight means teaching hashRouting.ts the param —
  // there, not around it — which is a change of its own.
  let selectedInsightId = "";
  // The insight a meeting was opened OUT OF, so Back returns to the document
  // that named it rather than to the list. Empty for a meeting opened from the
  // list, which is most of them.
  let insightReturnId = "";
  // The open insight's document, and which run+attempt it belongs to: a retry
  // is the same id with a later updatedAt, and the old answer must not be left
  // on screen under a new one.
  let insightDocument = "";
  let insightDocumentKey = "";
  let insightDocumentError = "";
  let insightDocumentLoading = false;

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

  function handleViewportChange(event: MediaQueryListEvent) {
    isDesktop = event.matches;
  }

  // The breakpoint below which the rooms rail becomes a drawer and the meeting
  // sheet becomes a bottom sheet. Separate from DESKTOP_MEDIA_QUERY, which asks
  // a different question (how much room MeetingView's own internals have).
  const NARROW_MEDIA_QUERY = "(max-width: 720px)";
  let isNarrow = false;
  let narrowMedia: MediaQueryList | null = null;

  function handleNarrowChange(event: MediaQueryListEvent) {
    isNarrow = event.matches;
    if (!isNarrow) {
      // The drawer only exists below the breakpoint; leaving it "open" would
      // strand a scrim over a rail that is now permanently in view.
      railOpen = false;
    }
  }

  let prefersReducedMotion = false;
  let reducedMotionMedia: MediaQueryList | null = null;

  function handleReducedMotionChange(event: MediaQueryListEvent) {
    prefersReducedMotion = event.matches;
  }

  // The sheet slides in from the edge it is anchored to — the right on a wide
  // viewport, the bottom on a phone, where a side drawer would leave the list
  // it covers unreachable and read as a page rather than a layer. A percentage
  // transform (rather than svelte/transition's fly, which needs pixels) travels
  // exactly the sheet's own width at whatever size it resolved to.
  function sheetSlide(_node: Element, { duration = 320 }: { duration?: number }) {
    if (prefersReducedMotion) {
      return { duration: 0 };
    }
    const axis = isNarrow ? "Y" : "X";
    return {
      duration,
      easing: cubicOut,
      css: (_t: number, u: number) => `transform: translate${axis}(${u * 100}%)`,
    };
  }

  function scrimFade() {
    return prefersReducedMotion ? { duration: 0 } : { duration: 200 };
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
    if (insightReturnId) {
      const returning = insightReturnId;
      insightReturnId = "";
      // Only if it is still listed: a refresh can drop a run out from under an
      // armed Back, and reopening a sheet on a record we no longer have would
      // be a worse answer than landing on the list.
      if (insights.some((record) => record.id === returning)) {
        selectedInsightId = returning;
      }
    }
  }

  // One sheet, one thing. An insight opened from the list replaces whatever the
  // sheet was holding, and the meeting's history entry goes with it — otherwise
  // Back would return to a meeting the reader had already left.
  function openInsight(record: InsightRecord) {
    if (selectedMeetingId) {
      pushMeetingUrl("");
      selectedMeetingId = "";
      notFoundMessage = "";
    }
    insightReturnId = "";
    selectedInsightId = record.id;
  }

  // A source named by the document opens as itself, in the same sheet, with the
  // way back to the document it came from armed — the reader went from the
  // insight to the meeting, so the insight is where "back" means.
  function openInsightSource(event: CustomEvent<MeetingCatalogEntry>) {
    const from = selectedInsightId;
    selectedInsightId = "";
    loadCatalogMeeting(event.detail);
    insightReturnId = from;
  }

  function closeSheet() {
    if (selectedInsightId) {
      selectedInsightId = "";
      return;
    }
    handleBackToList();
  }

  function handleRoomSelect(event: CustomEvent<string | null>) {
    selectedRoomKey = event.detail;
  }

  // Picking is not opening: a picked meeting stays picked while another one is
  // open, while the room chip changes, and while the search narrows past it.
  function handlePick(event: CustomEvent<MeetingCatalogEntry>) {
    selection = toggleSelected(selection, event.detail.id);
    if (selection.ids.length === 0) {
      // Nothing left to prepare; the panel would be describing an empty set.
      prepareOpen = false;
    }
  }

  function handleClearSelection() {
    selection = clearSelection();
    prepareOpen = false;
  }

  // syncSelectionToCatalog is called from a reactive statement rather than
  // being one: reconcileSelection reads and writes `selection`, and a `$:` that
  // did both would re-run on its own assignment. It returns the same object
  // when nothing changed, so the common path invalidates nothing.
  function syncSelectionToCatalog(meetings: MeetingCatalogEntry[]) {
    const next = reconcileSelection(selection, meetings);
    if (next !== selection) {
      selection = next;
      if (selection.ids.length === 0) {
        prepareOpen = false;
      }
    }
  }

  // The bundle is assembled by whoever is behind the provider — the operator,
  // through one implementation shared with the CLI. The viewer never assembles
  // one itself, so a provider without the capability offers no Prepare at all
  // rather than a lookalike (see dataProvider.ts).
  function loadSelectedBundle(): Promise<string> {
    const provider = dataProvider;
    if (!provider.loadContextBundle) {
      return Promise.reject(new Error("This build cannot assemble a context bundle."));
    }
    return provider.loadContextBundle(pickedMeetings);
  }

  // Escape closes the topmost layer, in the order they stack: the rooms drawer,
  // then Prepare, then the meeting sheet. It deliberately does NOT fire while
  // MeetingView's
  // shortcuts <dialog> is open — a native modal already answers Escape, and
  // closing the meeting out from under it would be a second, unasked-for action.
  function handleShellKeydown(event: KeyboardEvent) {
    if (event.key !== "Escape" || event.defaultPrevented) {
      return;
    }
    if (rootEl?.querySelector("dialog[open]")) {
      return;
    }
    if (railOpen) {
      railOpen = false;
      return;
    }
    if (prepareOpen) {
      prepareOpen = false;
      return;
    }
    if (selectedInsightId || selectedMeetingId) {
      closeSheet();
    }
  }

  function handlePopState() {
    const { meeting: urlMeetingId } = currentViewerHash();
    if (urlMeetingId === selectedMeetingId) {
      return;
    }
    notFoundMessage = "";
    // Browser history is the meeting's story; the insight is not in it. Moving
    // through it therefore leaves the insight behind rather than leaving a
    // document open over the meeting the URL now names.
    insightReturnId = "";
    selectedInsightId = "";
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

  // refreshInsights re-reads the caller's runs. It rides the catalog's own
  // cadence rather than a timer of its own because it answers a question the
  // catalog cannot: a run created a minute ago is `queued`, and the card that
  // says so has to become the card that opens an answer without a reload.
  //
  // Kept independent of refreshCatalog: either listing can fail on its own, and
  // a catalog error must not blank the insights or the other way round.
  async function refreshInsights() {
    const provider = dataProvider;
    if (!provider.listInsights || insightsRefreshRunning) {
      return;
    }
    insightsRefreshRunning = true;
    try {
      const listed = await provider.listInsights();
      if (destroyed) {
        return;
      }
      insights = listed;
      insightsLoaded = true;
      insightsError = "";
    } catch (error) {
      if (destroyed) {
        return;
      }
      // The last known-good list stays on screen under the failure, exactly as
      // the catalog's does — and the list says the listing failed rather than
      // showing a count that would read as "none".
      insightsError = error instanceof Error ? error.message : String(error);
    } finally {
      insightsRefreshRunning = false;
    }
  }

  // ensureInsightDocument fetches the open insight's answer once per attempt.
  // Only a succeeded run has one: a queued, running or failed run has nothing
  // to fetch, and asking for it would turn "not finished" into an error.
  function ensureInsightDocument(record: InsightRecord | null) {
    if (!record || record.status !== "succeeded") {
      return;
    }
    const provider = dataProvider;
    if (!provider.loadInsightDocument) {
      return;
    }
    // updatedAt moves with every attempt, so a retry re-fetches rather than
    // leaving the previous attempt's answer under the new record.
    const key = `${record.id}@${record.updatedAt}`;
    if (key === insightDocumentKey) {
      return;
    }
    insightDocumentKey = key;
    insightDocument = "";
    insightDocumentError = "";
    insightDocumentLoading = true;
    void provider
      .loadInsightDocument(record.id)
      .then((markdown) => {
        if (destroyed || insightDocumentKey !== key) {
          return;
        }
        insightDocument = markdown;
        insightDocumentLoading = false;
      })
      .catch((error: unknown) => {
        if (destroyed || insightDocumentKey !== key) {
          return;
        }
        insightDocumentError = error instanceof Error ? error.message : String(error);
        insightDocumentLoading = false;
      });
  }

  function refreshCatalogWhenVisible() {
    if (document.visibilityState === "visible") {
      void refreshCatalog();
      void refreshInsights();
    }
  }

  $: roomBuckets = buildRoomBuckets(catalogMeetings);
  // A room the catalog no longer describes (its last meeting was removed, or a
  // refresh re-tagged it) must not leave the list narrowed to nothing with no
  // chip that explains why.
  $: if (
    selectedRoomKey !== null &&
    !roomBuckets.some((bucket: RoomBucket) => bucket.key === selectedRoomKey)
  ) {
    selectedRoomKey = null;
  }
  $: selectedRoomName =
    roomBuckets.find((bucket: RoomBucket) => bucket.key === selectedRoomKey)?.name ??
    null;
  $: roomMeetings = filterMeetingsByRoom(catalogMeetings, selectedRoomKey);

  // A picked meeting can leave the archive under a 15-second refresh; run this
  // against every catalog the shell observes.
  $: syncSelectionToCatalog(catalogMeetings);
  $: pickedIds = new Set(selection.ids);
  $: pickedMeetings = selectedEntries(selection, catalogMeetings);
  $: selectionTotals = summarizeSelection(pickedMeetings);
  $: selectionGaps = describeSelectionGaps(selectionTotals);
  $: hiddenSelectedCount = countHiddenByView(selection, visibleMeetings);
  // Not `selection.ids.length > 0`: the bar is the only surface that reports a
  // meeting having left the archive, so it has to survive a loss that took the
  // last pick with it (selectionModel.shouldShowSelectionBar).
  $: selectionBarUp = shouldShowSelectionBar(selection);
  // Prepare exists only where something can produce the bundle. The standalone
  // export's provider says it cannot by not implementing the method, and the
  // whole affordance — checkbox, bar and panel — goes with it.
  $: canPrepare = typeof dataProvider.loadContextBundle === "function";

  // Insights exist here only if something can list them. A standalone export's
  // provider cannot, and then there is no type filter, no card and no document
  // — the honest reading of a build with no operator behind it.
  $: insightsOffered = typeof dataProvider.listInsights === "function";
  $: canLoadInsightDocument = typeof dataProvider.loadInsightDocument === "function";
  // Resolved against the WHOLE catalog, not the room-narrowed list: an insight
  // spanning rooms names sources in each of them, and counting only the ones in
  // the room being looked at would make the same insight claim a different
  // number of meetings depending on where you saw it.
  $: insightSources = resolveInsightSources(insights, catalogMeetings);
  $: insightSourceCounts = new Map(
    [...insightSources].map(([id, sources]) => [id, sources.length]),
  );
  $: roomInsights = filterInsightsByRoom(insights, selectedRoomKey);
  // Called from a reactive statement rather than being one, for the reason
  // syncSelectionToCatalog is: it writes the id that `selectedInsight` is
  // derived from, and a `$:` doing both would be a cycle.
  function dropMissingInsight(records: InsightRecord[]) {
    // A run that leaves the list — deleted elsewhere, or never ours — must not
    // leave a sheet open over a record nobody has any more. Only once a listing
    // has actually come back: an empty list we never loaded is not evidence.
    if (
      selectedInsightId &&
      insightsLoaded &&
      !records.some((record) => record.id === selectedInsightId)
    ) {
      selectedInsightId = "";
    }
  }
  $: dropMissingInsight(insights);
  $: selectedInsight = selectedInsightId
    ? insights.find((record) => record.id === selectedInsightId) ?? null
    : null;
  $: ensureInsightDocument(selectedInsight);
  // The other direction: which insights read the meeting the sheet is holding.
  $: linkedInsights = insightsForMeeting(insights, selectedMeetingId);

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
      narrowMedia = window.matchMedia(NARROW_MEDIA_QUERY);
      isNarrow = narrowMedia.matches;
      narrowMedia.addEventListener("change", handleNarrowChange);
      reducedMotionMedia = window.matchMedia("(prefers-reduced-motion: reduce)");
      prefersReducedMotion = reducedMotionMedia.matches;
      reducedMotionMedia.addEventListener("change", handleReducedMotionChange);
    }
    // Independent of the catalog load below, and started beside it: the two
    // lists come from two places and neither is a precondition for the other.
    void refreshInsights();

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
    narrowMedia?.removeEventListener("change", handleNarrowChange);
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

<svelte:window on:keydown={handleShellKeydown} />

<!-- .cassini-root carries the daisyUI theme (data-theme) and the theme-switching
     transition-suppression for the whole app. It lives here (in-tree), not on
     document.documentElement, so it works inside the embedded build's shadow
     root. MeetingView's shortcuts <dialog> is inside this wrapper too so
     [data-theme] styles it. -->
<div bind:this={rootEl} class="cassini-root" data-theme={themeMode} class:theme-switching={themeSwitching}>

{#if bundledMode}
  <!-- A standalone single-meeting export has no list to open over: the meeting
       IS the page, so it keeps the full-bleed layout it has always had. -->
  <div class="grid grid-cols-1 grid-rows-1 h-full bg-base-200 overflow-x-clip">
    <MeetingView
      {dataProvider}
      meeting={selectedMeeting}
      bundled={true}
      {isDesktop}
      {prefersReducedMotion}
      hasCatalog={catalogMeetings.length > 0}
      {notFoundMessage}
      on:back={handleBackToList}
      on:enriched={handleEnriched}
    />
  </div>
{:else}
  <div class="browse-shell">
    <RoomsRail
      rooms={roomBuckets}
      {selectedRoomKey}
      totalCount={catalogMeetings.length}
      open={railOpen}
      on:select={handleRoomSelect}
      on:close={() => (railOpen = false)}
    />

    <MeetingList
      meetings={roomMeetings}
      totalCount={catalogMeetings.length}
      insights={roomInsights}
      totalInsightCount={insights.length}
      {insightsOffered}
      {insightsLoaded}
      {insightsError}
      {insightSourceCounts}
      {selectedInsightId}
      {selectedRoomName}
      {selectedMeetingId}
      {pickedIds}
      selectable={canPrepare}
      bottomOverlay={selectionBarUp}
      {ncMode}
      {themeMode}
      errorMessage={listError}
      on:select={(event) => loadCatalogMeeting(event.detail)}
      on:pick={handlePick}
      on:openInsight={(event) => openInsight(event.detail)}
      on:visible={(event) => (visibleMeetings = event.detail)}
      on:clearRoom={() => (selectedRoomKey = null)}
      on:openRooms={() => (railOpen = true)}
      on:toggleTheme={toggleTheme}
    />

    {#if selectionBarUp}
      <div class="selection-dock">
        <SelectionBar
          count={selection.ids.length}
          hiddenCount={hiddenSelectedCount}
          droppedCount={selection.dropped.length}
          on:clear={handleClearSelection}
          on:prepare={() => (prepareOpen = true)}
          on:dismissDropped={() => (selection = acknowledgeDropped(selection))}
        />
      </div>
    {/if}

    {#if railOpen}
      <button
        type="button"
        class="shell-scrim rail-scrim"
        aria-label="Close the room list"
        transition:fade={scrimFade()}
        on:click={() => (railOpen = false)}
      ></button>
    {/if}

    <!-- One sheet, two kinds of thing (D-721). The wrapper's geometry is shared
         deliberately: it is what anchors the panel to this shell rather than to
         the viewport, and an insight that opened in a second, differently
         positioned panel would cover Nextcloud's own chrome in the embedded
         build. Switching kinds swaps the contents rather than the panel, so
         following a source out of a document does not slide the sheet away and
         back. -->
    {#if selectedInsight || selectedMeetingId}
      <button
        type="button"
        class="shell-scrim sheet-scrim"
        aria-label={selectedInsight ? "Close the insight" : "Close the meeting"}
        transition:fade={scrimFade()}
        on:click={closeSheet}
      ></button>
      <aside class="meeting-sheet" transition:sheetSlide={{}}>
        {#if selectedInsight}
          <InsightDocument
            insight={selectedInsight}
            sources={insightSources.get(selectedInsight.id) ?? []}
            documentMarkdown={insightDocument}
            documentError={insightDocumentError}
            documentLoading={insightDocumentLoading}
            canLoadDocument={canLoadInsightDocument}
            on:close={closeSheet}
            on:openSource={openInsightSource}
          />
        {:else}
          <MeetingView
            {dataProvider}
            meeting={selectedMeeting}
            bundled={false}
            inSheet={true}
            {isDesktop}
            {prefersReducedMotion}
            hasCatalog={catalogMeetings.length > 0}
            {notFoundMessage}
            on:back={handleBackToList}
            on:enriched={handleEnriched}
          />
          <!-- The other direction (D-721): a meeting says which insights read
               it. Rendered beside the meeting rather than inside it, because
               what a meeting was used FOR is not part of the recording. -->
          {#if linkedInsights.length > 0}
            <footer class="sheet-linked">
              <span class="sheet-linked-label">
                Used in {linkedInsights.length}
                {linkedInsights.length === 1 ? "insight" : "insights"}
              </span>
              <div class="sheet-linked-chips">
                {#each linkedInsights as record (record.id)}
                  <button
                    type="button"
                    class="sheet-linked-chip"
                    on:click={() => openInsight(record)}
                  >
                    {insightHeadline(record)}
                  </button>
                {/each}
              </div>
            </footer>
          {/if}
        {/if}
      </aside>
    {/if}

    {#if prepareOpen}
      <button
        type="button"
        class="shell-scrim prepare-scrim"
        aria-label="Close Prepare"
        transition:fade={scrimFade()}
        on:click={() => (prepareOpen = false)}
      ></button>
      <aside class="prepare-sheet" transition:sheetSlide={{}}>
        <PreparePanel
          entries={pickedMeetings}
          totals={selectionTotals}
          gaps={selectionGaps}
          loadBundle={loadSelectedBundle}
          on:close={() => (prepareOpen = false)}
        >
          <!-- Forwarded, not decided (D-722). Whether this deployment can be
               asked a question is a fact about the operator behind it, and the
               viewing layer has none: the shell passes the answer through, and
               a build with no shell — the standalone export — passes nothing,
               which is the honest reading of a question nobody could ask. -->
          <slot name="prepare-readiness" slot="readiness" />
          <!-- The same forwarding, with the picked meetings riding down with
               it (D-700): the shell's Generate card asks a question of the set
               this panel is describing, and `let:` is what carries a slot prop
               across the two levels. -->
          <svelte:fragment slot="generate" let:entries>
            <slot name="prepare-generate" {entries} />
          </svelte:fragment>
        </PreparePanel>
      </aside>
    {/if}
  </div>
{/if}
</div><!-- /.cassini-root -->

<style>
  /* The browse shell: rail + list side by side, with the meeting sheet and its
     scrim positioned against this element rather than the viewport (see the
     note in the script). Plain CSS because the sheet's geometry changes shape,
     not just size, below the breakpoint. */
  .browse-shell {
    position: relative;
    display: grid;
    grid-template-columns: 268px minmax(0, 1fr);
    height: 100%;
    min-height: 0;
    overflow: hidden;
    background-color: var(--color-base-100);
  }

  .shell-scrim {
    position: absolute;
    inset: 0;
    display: block;
    padding: 0;
    border: 0;
    cursor: pointer;
    background-color: oklch(0% 0 0 / 0.55);
  }
  .sheet-scrim {
    z-index: 20;
  }
  /* Above the meeting sheet: Prepare opens over whatever is already on screen. */
  .prepare-scrim {
    z-index: 34;
  }
  /* Above the sheet: with both open, the drawer is the layer on top, so its
     scrim has to cover the sheet too. */
  .rail-scrim {
    z-index: 39;
  }

  .meeting-sheet {
    position: absolute;
    top: 0;
    right: 0;
    bottom: 0;
    z-index: 30;
    width: min(680px, 100%);
    display: flex;
    flex-direction: column;
    background-color: var(--color-base-200);
    border-left: 1px solid var(--color-base-300);
    box-shadow: -8px 0 30px oklch(0% 0 0 / 0.22);
  }

  /* Sits under the meeting, inside the same sheet: the strip is about the
     meeting rather than part of it, and MeetingView's own scroll (with the
     player floating in it) has no room for a second thing at its bottom.
     flex: none so the view above shrinks to make space instead of pushing it
     off the sheet. */
  .sheet-linked {
    flex: none;
    display: flex;
    align-items: center;
    gap: 0.625rem;
    min-width: 0;
    padding: 0.5rem 0.75rem;
    background-color: var(--color-base-100);
    border-top: 1px solid var(--color-base-300);
  }
  .sheet-linked-label {
    flex: none;
    font-size: 11px;
    font-weight: 650;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  /* One line that scrolls: a meeting can be read by several insights, and a
     strip that wrapped would take height from the meeting itself. */
  .sheet-linked-chips {
    display: flex;
    flex: 1;
    gap: 0.375rem;
    min-width: 0;
    overflow-x: auto;
  }
  .sheet-linked-chip {
    flex: none;
    max-width: 20rem;
    padding: 3px 10px;
    cursor: pointer;
    background-color: color-mix(in oklch, var(--color-secondary) 15%, transparent);
    border: 1px solid color-mix(in oklch, var(--color-secondary) 40%, transparent);
    border-radius: 20px;
    font-size: 0.75rem;
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--color-base-content);
  }
  .sheet-linked-chip:hover {
    background-color: color-mix(in oklch, var(--color-secondary) 28%, transparent);
  }

  /* The selection bar floats over the list it belongs to — inset past the rail
     track, and absolute against the shell rather than fixed to the viewport,
     for exactly the reason the sheet is (see the note in the script). 288px is
     the rail's 268px plus the bar's own margin. */
  .selection-dock {
    position: absolute;
    left: 288px;
    right: 20px;
    bottom: 14px;
    z-index: 15;
  }

  /* Narrower than the meeting sheet: a review step, not a reading surface. */
  .prepare-sheet {
    position: absolute;
    top: 0;
    right: 0;
    bottom: 0;
    z-index: 35;
    width: min(460px, 100%);
    display: flex;
    flex-direction: column;
    background-color: var(--color-base-100);
    border-left: 1px solid var(--color-base-300);
    box-shadow: -8px 0 30px oklch(0% 0 0 / 0.22);
  }

  @media (max-width: 720px) {
    /* The rail is out of flow down here (it is the drawer), so the list gets
       the whole shell rather than being squeezed beside an empty track. */
    .browse-shell {
      grid-template-columns: minmax(0, 1fr);
    }
    /* No rail track to clear. */
    .selection-dock {
      left: 12px;
      right: 12px;
    }
    .prepare-sheet {
      top: auto;
      left: 0;
      right: 0;
      bottom: 0;
      width: 100%;
      height: 92%;
      border-left: 0;
      border-top: 1px solid var(--color-base-300);
      border-radius: var(--radius-box, 1rem) var(--radius-box, 1rem) 0 0;
      box-shadow: 0 -8px 30px oklch(0% 0 0 / 0.22);
    }
    /* A side drawer on a phone leaves the content it covers unreachable and
       reads as a page; a bottom sheet reads as a layer over the list. */
    .meeting-sheet {
      top: auto;
      left: 0;
      right: 0;
      bottom: 0;
      width: 100%;
      height: 92%;
      border-left: 0;
      border-top: 1px solid var(--color-base-300);
      border-radius: var(--radius-box, 1rem) var(--radius-box, 1rem) 0 0;
      box-shadow: 0 -8px 30px oklch(0% 0 0 / 0.22);
    }
  }
</style>
