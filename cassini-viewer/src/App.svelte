<script lang="ts">
  import { createEventDispatcher, onDestroy, onMount } from "svelte";
  import { cubicOut } from "svelte/easing";
  import { fade } from "svelte/transition";
  import type { PortableMeetingSummary } from "./viewer/loadArtifact";
  import {
    filterMeetingCatalogEntries,
    sortMeetingCatalogEntries,
    type MeetingCatalogEntry,
  } from "./viewer/catalog";
  import { StaticCatalogProvider, type DataProvider } from "./viewer/dataProvider";
  import {
    groupHitsByMeeting,
    isAbortError,
    isMeetingSearchAvailable,
    type MeetingSearchHit,
  } from "./viewer/meetingSearch";
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
    isSelected,
    reconcileSelection,
    selectedEntries,
    shouldShowSelectionBar,
    summarizeSelection,
    toggleSelected,
    type MeetingSelection,
  } from "./viewer/selectionModel";
  import {
    ALL_BROWSE_TYPES,
    filterInsightsByRoom,
    insightsForMeeting,
    missingInsightSources,
    resolveInsightSources,
    toggleBrowseType,
    type BrowseType,
    type BrowseTypeFilter,
    type InsightRecord,
  } from "./viewer/insights";
  import {
    AnnotationError,
    mergeVocabularyTags,
    tagsByMeeting,
    type AnnotationRequest,
    type MeetingAnnotations,
    type TagPick,
    type TagVocabulary,
  } from "./viewer/annotations";
  import {
    createListTagSession,
    createTagLoader,
    createWriteQueue,
    filterByTags,
    wholeTagState,
    withMeetingResult,
    type MeetingTags,
    type TagMatch,
  } from "./viewer/listTags";
  import { createMeetingMarksSessions } from "./components/marking/sessions";
  import ImportPanel from "./components/import/ImportPanel.svelte";
  import InsightDocument from "./components/InsightDocument.svelte";
  import MeetingList from "./components/MeetingList.svelte";
  import MeetingView from "./components/MeetingView.svelte";
  import PreparePanel from "./components/PreparePanel.svelte";
  import TagManager from "./components/tags/TagManager.svelte";
  import RoomNameDialog from "./components/RoomNameDialog.svelte";
  import type { RoomNameState } from "./viewer/roomNames";
  import ConfirmDialog from "./components/ui/ConfirmDialog.svelte";
  import RoomsRail from "./components/RoomsRail.svelte";
  import SelectionBar from "./components/SelectionBar.svelte";
  import { focusLayer } from "./components/ui/focusLayer";

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

  // Who can see the recordings this viewer is listing (D-756). Part of the
  // viewing layer's public surface because the chip that states it belongs
  // beside the meeting count, and passed down rather than resolved here: the
  // fact comes from the deployment's operator, which this layer cannot reach
  // and a standalone export does not have. "" is "nobody said", and the chip
  // renders nothing.
  export let audience: "" | "participants" = "";

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
  const CATALOG_REFRESH_INTERVAL_MS = 60_000;
  let catalogMode = false;
  let catalogRefreshRunning = false;
  let catalogRequestGeneration = 0;
  let editingRoomId: string | null = null;
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
  // The floating bar's real height, so the list can leave exactly that much
  // room under its last row.
  let selectionDockHeight = 0;
  // Said to whoever mounts this shell, each time Prepare opens: the panel's
  // readiness slot is filled from a fact only the shell around it has (whether
  // this deployment has an AI endpoint), and that fact is read once at mount.
  // A reader who was told "no endpoint" and comes back after an administrator
  // configured one is otherwise told it again until they reload (D-749).
  const dispatch = createEventDispatcher<{ prepareOpen: void; overlay: boolean }>();

  // Whether something is open over the browse surface. The shell above this
  // component draws the Browse/Operator tabs, which a scrim positioned inside
  // here cannot reach: told this, it can dim and blur them with everything
  // else, so an overlay covers the app rather than most of it.
  // The rooms drawer is not one of them: it is a part of this surface sliding
  // into view, not a thing over it, and the tabs stay usable while it is open.
  $: overlayOpen = prepareOpen || tagManagerOpen || Boolean(selectedInsight || selectedMeetingId);
  // Which layer is on top, in the order they stack: the meeting sheet, then
  // Prepare, then the rooms drawer (only ever open on a phone), then Manage
  // tags. Everything under it is inert, so neither Tab nor a screen reader
  // wanders into a page the reader cannot see for the drawer over it.
  $: topLayer = deleteConfirm ? 5 : tagManagerOpen ? 4 : railOpen ? 3 : prepareOpen ? 2 : selectedInsight || selectedMeetingId ? 1 : 0;
  $: dispatch("overlay", overlayOpen);
  $: if (prepareOpen) {
    dispatch("prepareOpen");
  }
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
  // Cross-meeting search (D-736). The shell owns it because it is a request,
  // and because the list is presentational.
  let searchQuery = "";
  let searchState: "idle" | "searching" | "ok" | "rateLimited" | "indexUnavailable" | "failed" = "idle";
  let searchMessage = "";
  let transcriptHits: ReadonlyMap<string, readonly MeetingSearchHit[]> = new Map();
  let transcriptOnlyMeetings: MeetingCatalogEntry[] = [];
  let searchCoverage: { visible: number; searched: number } | null = null;
  let lastNarrowingKey = "";
  let searchDebounce: ReturnType<typeof setTimeout> | undefined;
  // Only the newest query may write the results. Without this a slow earlier
  // request can land after a faster later one and repaint the list with answers
  // to a question the user has already moved on from.
  let searchRun = 0;
  let searchAbort: AbortController | undefined;
  // Carried into MeetingView when a moment is opened from a search result, so
  // the meeting lands already filtered to the lines that matched. Cleared on
  // any ordinary open, because a meeting opened from the list is not a search
  // result and should show the whole transcript.
  let transcriptQueryForMeeting = "";

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

  // Tags (D-746). A failed reload keeps the last vocabulary that loaded.
  let tagVocabulary: TagVocabulary | null = null;
  let tagsFailed = false;
  let catalogPending = true;
  let selectedTagIds: string[] = [];
  let tagMatch: TagMatch = "any";
  let tagNotice = "";
  let tagRetryable = false;
  let tagManagerOpen = false;

  // Delete a meeting (D-861). The ids already deleted keep the catalog's
  // refresh poll from flashing a deleted row back before the next listing
  // reflects it; a failure rolls its entry back in and says so.
  const deletedMeetingIds = new Set<string>();
  let deleteConfirm: MeetingCatalogEntry[] | null = null;
  let deleteBusy = false;
  let deleteNotice = "";
  let deleteFailed: MeetingCatalogEntry[] = [];
  $: canDelete = Boolean(dataProvider.deleteMeeting);

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
  // One duration for everything that opens over a surface — the panel, the
  // scrim under it and the tabs the shell dims — so an overlay reads as a
  // single move rather than three that finish at different moments.
  const OVERLAY_MS = 260;

  function sheetSlide(_node: Element, { duration = OVERLAY_MS }: { duration?: number }) {
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
    return prefersReducedMotion ? { duration: 0 } : { duration: OVERLAY_MS, easing: cubicOut };
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
    // Keep the requested transcript, seek, and selection on the meeting entry.
    const hash = currentViewerHash().meeting === meetingId
      ? window.location.hash
      : buildViewerHash({ meeting: meetingId });
    window.history.replaceState({}, "", viewerHref(buildViewerHash({})));
    window.history.pushState({}, "", viewerHref(hash));
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
    listTagSession.clearBulkReport();
    if (selection.ids.length === 0) {
      // Nothing left to prepare; the panel would be describing an empty set.
      prepareOpen = false;
    }
  }

  function handleClearSelection() {
    selection = clearSelection();
    prepareOpen = false;
    listTagSession.clearBulkReport();
  }

  const tagLoader = createTagLoader(
    () => dataProvider.loadTagVocabulary!(),
    (vocabulary) => {
      if (vocabulary) listTagSession.setConfirmed(vocabulary);
      tagsFailed = !vocabulary;
    },
    () => listTagSession.confirmedGeneration(),
  );

  // On open, on return to the tab and after writes; not on the catalog's timer.
  function refreshTags(fresh = false) {
    if (dataProvider.loadTagVocabulary) {
      void tagLoader.reload(fresh);
    }
  }

  const queueTagWrite = createWriteQueue(() => refreshTags(true));

  const meetingMarks = createMeetingMarksSessions(queueTagWrite, reconcileMeetingTags);

  const listTagSession = createListTagSession(
    async (meeting, request) => {
      const result = await dataProvider.applyAnnotationOps!(meeting, request);
      meetingMarks.receive(result);
      return result;
    },
    queueTagWrite,
    async (request) => {
      const result = await dataProvider.applyAnnotationBatch!(request);
      result.results.forEach((meeting) => meetingMarks.receive(meeting));
      return result;
    },
  );

  $: tagVocabulary = $listTagSession.vocabulary;
  $: annotationNotice = $meetingMarks[0];
  $: tagNotice = annotationNotice ? `“${annotationNotice.title}”: ${annotationNotice.message}` : $listTagSession.notice;
  $: tagRetryable = annotationNotice ? annotationNotice.retryable : $listTagSession.retryable;

  // The session applies the click to its optimistic layer before this returns;
  // queued writes reconcile the confirmed layer underneath it.
  function tagMeeting(meeting: MeetingCatalogEntry, pick: TagPick) {
    listTagSession.toggle(meeting, pick);
  }

  function reconcileMeetingTags(result: MeetingAnnotations) {
    if (destroyed) return;
    listTagSession.updateConfirmed((vocabulary) => withMeetingResult(vocabulary, result));
    // The shared queue refreshes the vocabulary once this burst drains.
  }

  function tagSelection(pick: TagPick) {
    if (!dataProvider.applyAnnotationBatch) return;
    listTagSession.toggleBulk(pickedMeetings, pick);
  }

  function toggleTagFilter(tagId: string) {
    selectedTagIds = activeTagIds.includes(tagId)
      ? activeTagIds.filter((id) => id !== tagId)
      : [...activeTagIds, tagId];
  }

  // Bound to an id rather than the entry, so a catalog refresh does not hand the meeting view new functions.
  //
  // Reading marks and writing them are bound separately (D-775). A provider that
  // can only read — a published export, whose tags come out of the recording
  // itself — still opens the session and still draws its marks; it just hands
  // over no `apply`, and the session reports itself as not editable so every
  // control that would change a mark is absent.
  function bindAnnotations(provider: DataProvider, meetingId: string) {
    if (!meetingId || !provider.loadMeetingAnnotations) {
      meetingMarks.activate("", "", null, null);
      return { load: null, apply: null, session: null };
    }
    const entry = async () => {
      const found = catalogMeetings.find((meeting) => meeting.id === meetingId);
      if (!found) {
        throw new Error(`Meeting not found in catalog: ${meetingId}`);
      }
      return found;
    };
    const apply = provider.applyAnnotationOps;
    const load = async () => provider.loadMeetingAnnotations!(await entry());
    const write = apply ? async (request: AnnotationRequest) => apply.call(provider, await entry(), request) : null;
    const title = catalogMeetings.find((meeting) => meeting.id === meetingId)?.title ?? meetingId;
    return { load, apply: write, session: meetingMarks.activate(meetingId, title, load, write) };
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
  export function closeOverlay() {
    if (tagManagerOpen) {
      tagManagerOpen = false;
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

  function handleShellKeydown(event: KeyboardEvent) {
    if (editingRoomId) return;
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

  // SEARCH_DEBOUNCE_MS: long enough that typing a word is one request rather
  // than six, short enough that the list does not feel stuck. Each search costs
  // the operator a whole-archive PROPFIND as the caller, which is also why the
  // endpoint rate-limits: this is the client-side half of not doing that.
  const SEARCH_DEBOUNCE_MS = 220;
  // A meeting contributes at most this many moments to the list. The server
  // caps it so one talkative meeting cannot crowd every other one out of the
  // page; this is the number the rows have room to show.
  const SEARCH_HITS_PER_MEETING = 3;

  // Two independent reasons there may be nothing to ask, and both have to turn
  // the promise in the placeholder off:
  //
  //  1. No operator base at all — a standalone export. Known up front.
  //  2. An operator that does not serve the route (older than D-623, or on the
  //     local sink), which only shows up as a 404 on the first real search.
  //
  // Feature-detecting the provider method answers neither: every provider here
  // defines it, because the module decides at call time.
  let searchRouteServed = true;
  // What of the current narrowing the SERVER can apply. Sending it matters
  // more than it looks: the endpoint bounds its visible set before the
  // statement, so LIMIT applies to the narrowed population. Narrowing only the
  // answer would let a caller with a tight filter be told nothing matched while
  // matches sat just below a page ranked over everything they can read.
  //
  // Only some narrowings are expressible. Room keys are prefixed by roomKeyOf:
  // `id:<roomId>` is a real room the endpoint knows, while `name:` (a room with
  // a name and no id) and `no-room` are viewer-side groupings with nothing to
  // send. The endpoint takes one tag, so several picked tags — or "all" mode —
  // stay client-side too. Those cases still narrow correctly, because the list
  // resolves hits against the narrowed set; they can just under-report on a
  // corpus large enough to fill the page from outside the filter.
  $: searchNarrowing = {
    roomId: selectedRoomKey?.startsWith("id:") ? selectedRoomKey.slice(3) : "",
    tag: activeTagIds.length === 1 ? activeTagIds[0] : "",
  };
  // Re-run when the narrowing changes under a live query. Without this, picking
  // a tag after searching leaves the previous answer on screen — which is what
  // "filtering after a search does nothing" looked like from the outside.
  $: narrowingKey = `${searchNarrowing.roomId}\u0000${searchNarrowing.tag}`;
  $: if (narrowingKey !== lastNarrowingKey) {
    lastNarrowingKey = narrowingKey;
    if (searchQuery.trim() !== "" && searchOffered) {
      void runSearch(searchQuery);
    }
  }

  $: searchOffered =
    typeof dataProvider.searchMeetings === "function" &&
    isMeetingSearchAvailable() &&
    searchRouteServed;

  function resetSearch() {
    clearTimeout(searchDebounce);
    searchAbort?.abort();
    searchAbort = undefined;
    // Invalidate immediately, including the debounce window. A provider may
    // still resolve a request after its signal has been aborted.
    searchRun += 1;
    searchMessage = "";
    transcriptHits = new Map();
    transcriptOnlyMeetings = [];
    searchCoverage = null;
  }

  function handleSearchQuery(query: string) {
    searchQuery = query;
    resetSearch();
    if (query.trim() === "" || !dataProvider.searchMeetings) {
      searchState = "idle";
      return;
    }
    searchState = "searching";
    searchDebounce = setTimeout(() => void runSearch(query), SEARCH_DEBOUNCE_MS);
  }

  async function runSearch(query: string) {
    const search = dataProvider.searchMeetings;
    if (!search) {
      return;
    }
    // Room/tag changes also start a new search, without going through typing.
    resetSearch();
    searchState = "searching";
    const run = searchRun;
    const controller = new AbortController();
    searchAbort = controller;

    let outcome;
    try {
      outcome = await search(query, {
        perMeeting: SEARCH_HITS_PER_MEETING,
        roomId: searchNarrowing.roomId,
        tag: searchNarrowing.tag,
        signal: controller.signal,
      });
    } catch (error) {
      if (isAbortError(error)) {
        // Superseded by a later keystroke. Saying anything here would overwrite
        // the newer request's state with the older one's.
        return;
      }
      if (run === searchRun) {
        searchState = "failed";
        searchMessage = "Could not search the meetings.";
      }
      return;
    }
    if (run !== searchRun) {
      return;
    }

    if (outcome.status === "unsupported") {
      // The route is not served here. Stop claiming it in the placeholder:
      // going on promising a search that answers nothing is worse than never
      // having offered it. The box still narrows names and dates.
      searchRouteServed = false;
      searchState = "idle";
      searchMessage = "";
      transcriptHits = new Map();
      transcriptOnlyMeetings = [];
      return;
    }
    if (outcome.status !== "ok") {
      searchState = outcome.status;
      searchMessage = outcome.message;
      return;
    }

    searchState = "ok";
    searchMessage = "";
    searchCoverage = outcome.coverage;
    const grouped = groupHitsByMeeting(outcome.hits);
    transcriptHits = new Map(grouped.map((match) => [match.meetingId, match.hits]));
    // Meetings the name/date filter will not produce, in the server's rank
    // order. The list concatenates them after its own matches.
    // The NARROWED set, not the whole catalog. Resolving here against every
    // meeting is what let a transcript-only hit render outside the caller's own
    // room or tag filter, so picking a tag after searching appeared to do
    // nothing (D-771). A search result is not a reason to override a narrowing
    // the reader asked for.
    const known = new Map(roomMeetings.map((meeting) => [meeting.id, meeting]));
    const nameMatched = new Set(
      filterMeetingCatalogEntries(roomMeetings, query).map((meeting) => meeting.id),
    );
    transcriptOnlyMeetings = grouped
      .filter((match) => !nameMatched.has(match.meetingId))
      .map((match) => known.get(match.meetingId))
      .filter((meeting): meeting is MeetingCatalogEntry => Boolean(meeting));
  }

  // Open a meeting AT a matched moment, carrying the query through so the
  // meeting view's own filter shows the matching lines in full. The words are
  // fetched there as the caller, so Nextcloud re-checks the ACL on the bytes —
  // which is why the list can show a short quote and the meeting can show the
  // whole line without those being the same disclosure.
  function openSearchMoment(detail: { entry: MeetingCatalogEntry; startMs: number; query: string }) {
    notFoundMessage = "";
    window.history.pushState(
      {},
      "",
      viewerHref(buildViewerHash({ meeting: detail.entry.id, timeMs: detail.startMs })),
    );
    selectedMeetingId = detail.entry.id;
    transcriptQueryForMeeting = detail.query;
  }

  function loadCatalogMeeting(meeting: MeetingCatalogEntry) {
    notFoundMessage = "";
    pushMeetingUrl(meeting.id);
    selectedMeetingId = meeting.id;
    // Opened from a search that matched what was said: carry the words into
    // the transcript's own search, as opening one of its moments does. A
    // meeting found by its title only would open on "nothing matches".
    transcriptQueryForMeeting = (transcriptHits.get(meeting.id)?.length ?? 0) > 0 ? searchQuery : "";
  }

  // Delete flows (D-861). The confirm dialog is the shell's: every entry point
  // — sheet header, list row, selection bar — lands in requestDeleteMeetings
  // and nothing deletes without passing the dialog.
  function requestDeleteMeetings(entries: MeetingCatalogEntry[]) {
    if (!canDelete || entries.length === 0 || deleteBusy) {
      return;
    }
    deleteConfirm = entries;
  }

  async function runMeetingDeletes(entries: MeetingCatalogEntry[]) {
    const remove = dataProvider.deleteMeeting?.bind(dataProvider);
    if (!remove || deleteBusy) {
      return;
    }
    deleteBusy = true;
    deleteNotice = "";
    const failed: MeetingCatalogEntry[] = [];
    for (const entry of entries) {
      // Optimistic: the row leaves the list before the operator answers, and
      // the remembered id keeps the next catalog poll from flashing it back.
      deletedMeetingIds.add(entry.id);
      catalogMeetings = catalogMeetings.filter((listed) => listed.id !== entry.id);
      if (selectedMeetingId === entry.id) {
        handleBackToList();
      }
      // Unpick directly: a deliberate delete is not a "no longer in the
      // archive" loss, so it must not land in the selection's dropped notice.
      if (isSelected(selection, entry.id)) {
        selection = toggleSelected(selection, entry.id);
      }
      try {
        await remove(entry);
      } catch (error) {
        if (error instanceof AnnotationError && error.status === 404) {
          // Already out of our readable set — somebody else deleted it, or a
          // replayed request. Gone is the outcome either way.
          continue;
        }
        deletedMeetingIds.delete(entry.id);
        failed.push(entry);
      }
    }
    if (failed.length > 0) {
      // A poll may already have restored a failed target while later deletes
      // were pending. Merge by id, keeping any fresher catalog entry.
      const restored = new Map(failed.map((entry) => [entry.id, entry]));
      for (const entry of catalogMeetings) {
        restored.set(entry.id, entry);
      }
      catalogMeetings = sortMeetingCatalogEntries([...restored.values()]);
      deleteNotice =
        failed.length === 1
          ? `“${failed[0].title}” could not be deleted.`
          : `${failed.length} meetings could not be deleted.`;
    }
    deleteFailed = failed;
    deleteBusy = false;
  }

  function confirmDeleteMeetings() {
    const entries = deleteConfirm ?? [];
    deleteConfirm = null;
    void runMeetingDeletes(entries);
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

  // A rename applies to everyone's view (D-862). A set title is folded into
  // the catalog in place; a reset needs the recorded default back, which only
  // the server knows, so it refetches the catalog instead. Either way a refresh
  // already in flight was fetched pre-rename: invalidate it (and release the
  // running guard) so a stale response can't revert the title, same as
  // roomNameSaved.
  function handleRenamed(event: CustomEvent<{ id: string; title: string | null }>) {
    const { id, title } = event.detail;
    ++catalogRequestGeneration;
    catalogRefreshRunning = false;
    if (title !== null) {
      catalogMeetings = catalogMeetings.map((entry) =>
        entry.id === id ? { ...entry, title } : entry,
      );
    } else {
      void refreshCatalog();
    }
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
    // The installed app's list is backed by Cassini's local metadata index.
    // Fetching one .opus per card would turn a thousand-meeting list into a
    // thousand serial network requests. Static exports still hydrate here.
    if (embedded) return;
    const generation = ++catalogHydrationGeneration;
    for (const meeting of meetings) {
      if (generation !== catalogHydrationGeneration) {
        return;
      }
      if (
        !(meeting.meetingPath ?? meeting.audioPath) ||
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

  function roomNameSaved(state: RoomNameState) {
    ++catalogRequestGeneration;
    catalogRefreshRunning = false;
    catalogMeetings = catalogMeetings.map((entry) => entry.roomId === state.roomId ? {...entry, roomDisplayName: state.displayName} : entry);
    editingRoomId = null;
    void refreshCatalog();
  }

  async function refreshCatalog() {
    // Embedded mode keeps probing even when no catalog existed at mount time:
    // a fresh install can stay open while its first recording is published.
    if ((!catalogMode && !embedded) || catalogRefreshRunning) {
      return;
    }
    catalogRefreshRunning = true;
    const generation = ++catalogRequestGeneration;
    try {
      const catalog = await dataProvider.loadCatalog();
      if (destroyed || generation !== catalogRequestGeneration || !catalog) {
        return;
      }
      // A meeting deleted from here stays gone even when the poll's listing
      // still carries it (the delete and the listing can cross on the wire).
      const listedMeetings = catalog.meetings.filter((entry) => !deletedMeetingIds.has(entry.id));
      catalogMode = true;
      bundledMode = false;
      catalogMeetings = listedMeetings;
      catalogPending = false;
      listError = "";
      void hydrateCatalogMeetingMetadata(listedMeetings);
      // A deep link that could not be satisfied at mount is satisfied here, the
      // first time a catalog actually arrives.
      if (!selectedMeetingId && pendingMeetingId !== null) {
        applyCatalogSelection(listedMeetings, pendingMeetingId);
      } else if (selectedMeetingId) {
        notFoundMessage = listedMeetings.some((entry) => entry.id === selectedMeetingId)
          ? ""
          : `Meeting not found in catalog: ${selectedMeetingId}`;
      }
      pendingMeetingId = null;
    } catch (error) {
      if (destroyed || generation !== catalogRequestGeneration) {
        return;
      }
      // Preserve the last known-good list while surfacing the refresh failure.
      listError = error instanceof Error ? error.message : String(error);
    } finally {
      if (generation === catalogRequestGeneration) catalogRefreshRunning = false;
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

  // retryInsightRun asks the provider to retry a failed run and puts the
  // record it answers with — queued again, attempt incremented — in the list
  // in place of the failed one, from wherever the reader pressed Retry: the
  // browse card or the document sheet (D-749). A refresh follows so the card
  // keeps moving on the shared tick. One retry at a time: the button is the
  // lock on this side, as the run's status is on the operator's.
  let retryingInsightId = "";
  let insightRetryError: { id: string; message: string } | null = null;

  async function retryInsightRun(record: InsightRecord) {
    const provider = dataProvider;
    if (!provider.retryInsight || retryingInsightId !== "") {
      return;
    }
    retryingInsightId = record.id;
    insightRetryError = null;
    try {
      const updated = await provider.retryInsight(record.id);
      if (destroyed) {
        return;
      }
      insights = insights.map((row) => (row.id === updated.id ? updated : row));
    } catch (error) {
      if (destroyed) {
        return;
      }
      // Whatever the provider said — a 409 "already running" included, which
      // is an answer rather than a failure and is followed by the refresh that
      // shows the run moving.
      insightRetryError = {
        id: record.id,
        message: error instanceof Error ? error.message : String(error),
      };
    } finally {
      retryingInsightId = "";
    }
    void refreshInsights();
  }

  function retrySelectedInsight() {
    if (selectedInsight) {
      void retryInsightRun(selectedInsight);
    }
  }

  // renameInsightRun sends the sheet's new name and puts the record it answers
  // with in the list, which is where the sheet, the card and the meeting's
  // Insights card all read their headline from (D-791).
  let renamingInsightId = "";
  let insightRenameError: { id: string; message: string } | null = null;

  async function renameInsightRun(record: InsightRecord, name: string) {
    const provider = dataProvider;
    if (!provider.renameInsight) {
      return;
    }
    if (renamingInsightId !== "") {
      // Said, not swallowed: the sheet keeps its input open on an error, and a
      // request that was never sent must not close it as if it had been.
      insightRenameError = { id: record.id, message: "Another rename is still saving. Try again in a moment." };
      return;
    }
    renamingInsightId = record.id;
    insightRenameError = null;
    try {
      const updated = await provider.renameInsight(record.id, name);
      if (destroyed) {
        return;
      }
      insights = insights.map((row) => (row.id === updated.id ? updated : row));
    } catch (error) {
      if (destroyed) {
        return;
      }
      insightRenameError = {
        id: record.id,
        message: error instanceof Error ? error.message : String(error),
      };
    } finally {
      renamingInsightId = "";
    }
  }

  function renameSelectedInsight(event: CustomEvent<string>) {
    if (selectedInsight) {
      void renameInsightRun(selectedInsight, event.detail);
    }
  }

  // A refusal is about the sheet it was shown in. Closing the sheet, a hash
  // navigation or a listing that no longer carries the run all tear the editor
  // down without a cancel, and the next pencil on that insight must not open
  // on a stale reason under an untouched draft.
  $: if (insightRenameError && insightRenameError.id !== selectedInsightId) {
    insightRenameError = null;
  }

  // handleInsightCreated is what Generate does once the operator has answered
  // (D-749): the new record goes to the top of the list, where every insight
  // is shown, and the Prepare panel closes — the question has been asked, and
  // the panel's subject was the set it was asked of. Optimistic, so the card
  // is on screen before the shared refresh tick; the refresh that follows
  // replaces it with the operator's own listing. dropMissingInsight cannot
  // close a sheet over it: nothing is open, and the listing will carry it.
  function handleInsightCreated(record: InsightRecord) {
    insights = [record, ...insights.filter((row) => row.id !== record.id)];
    prepareOpen = false;
    void refreshInsights();
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

  // Tags reload on return rather than on the timer: each load spends one of the
  // caller's searches (annotations/tags shares search's rate budget).
  function refreshOnReturn() {
    refreshCatalogWhenVisible();
    if (document.visibilityState === "visible") {
      refreshTags();
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
  $: canTag =
    typeof dataProvider.loadTagVocabulary === "function" &&
    typeof dataProvider.applyAnnotationOps === "function";
  // A vocabulary is for looks, canTag is for writes (D-775). Keeping these one
  // value meant a provider that could read tags but not write them rendered
  // them unstyled. Nothing leaks by separating them: the tag filter and the
  // picker are gated on canTag where they are used.
  $: vocabularyTags = tagVocabulary ? mergeVocabularyTags(tagVocabulary.tags) : null;
  $: meetingTags = (tagVocabulary ? tagsByMeeting(tagVocabulary) : new Map()) as MeetingTags;
  // A tag deleted or merged away must not leave the list narrowed by a box that is gone.
  $: activeTagIds = selectedTagIds.filter((id) => vocabularyTags?.some((tag) => tag.tagId === id));
  $: if (activeTagIds.length < 2) {
    tagMatch = "any";
  }
  $: roomMeetings = filterByTags(
    filterMeetingsByRoom(catalogMeetings, selectedRoomKey),
    meetingTags,
    activeTagIds,
    tagMatch,
  );
  $: bulkTagState = wholeTagState(meetingTags, selection.ids);
  $: annotationCalls = bindAnnotations(dataProvider, selectedMeetingId);

  // A picked meeting can leave the archive under a 15-second refresh; run this
  // against every catalog the shell observes.
  $: syncSelectionToCatalog(catalogMeetings);
  $: pickedIds = new Set(selection.ids);
  $: pickedMeetings = selectedEntries(selection, catalogMeetings);
  $: selectionTotals = summarizeSelection(pickedMeetings);
  $: selectionGaps = describeSelectionGaps(selectionTotals);
  $: hiddenSelectedCount = countHiddenByView(selection, visibleMeetings);
  // The same question as countHiddenByView, answered per meeting, for the list
  // behind the bar's count: a pick the current room or search is not showing
  // says so beside its own row rather than only in a total.
  $: shownMeetingIds = new Set(visibleMeetings.map((meeting) => meeting.id));
  $: hiddenSelectedIds = new Set(selection.ids.filter((id) => !shownMeetingIds.has(id)));
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

  // Import exists only where a provider can stage, start and list (D-828): the
  // in-Nextcloud build. Without it there is no tab switcher at all, so the
  // static, public and bundled builds render pixel-identically.
  $: importsOffered =
    typeof dataProvider.stageImportUpload === "function" &&
    typeof dataProvider.startImports === "function" &&
    typeof dataProvider.listImports === "function";
  // Which half of Browse the right-hand pane shows. The Import tab is a
  // persistent workbench, not an overlay: Escape and the layer bookkeeping are
  // untouched by it.
  let browseTab: "meetings" | "import" = "meetings";
  // True while imports are staging or running server-side — the dot on the tab
  // label, reported by the panel (which stays mounted so the dot is honest
  // before the tab is first opened).
  let importActivity = false;
  $: if (!importsOffered) {
    browseTab = "meetings";
  }
  // Which kinds the browse list is showing. Owned here rather than in the list
  // because the control that changes it is in the rail: two components reading
  // one filter cannot each keep their own copy of it.
  let browseTypes: BrowseTypeFilter = ALL_BROWSE_TYPES;
  // What each kind would contribute under the current room and search, reported
  // by the list because the search box is the list's.
  let browseCounts = { meetings: 0, insights: 0 };
  // A build with no insights must not be left narrowed to insights: the control
  // that would put the meetings back is not rendered there.
  $: if (!insightsOffered) {
    browseTypes = ALL_BROWSE_TYPES;
  }
  $: canLoadInsightDocument = typeof dataProvider.loadInsightDocument === "function";
  // Retry is offered exactly where something can perform it: the card and the
  // sheet render the control only when the provider has the method.
  $: canRetryInsight = typeof dataProvider.retryInsight === "function";
  $: canRenameInsight = typeof dataProvider.renameInsight === "function";
  // The offline copy (D-792). Files is only where there is a Nextcloud to open
  // it in; a source links to its own sheet in this viewer, wherever this is.
  $: insightFilesUrl =
    selectedInsight && dataProvider.insightFilesUrl ? dataProvider.insightFilesUrl(selectedInsight) : "";
  const insightSourceUrl = (source: MeetingCatalogEntry) =>
    viewerHref(buildViewerHash({ meeting: source.id }));
  // Resolved against the WHOLE catalog, not the room-narrowed list: an insight
  // spanning rooms names sources in each of them, and counting only the ones in
  // the room being looked at would make the same insight claim a different
  // number of meetings depending on where you saw it.
  $: insightSources = resolveInsightSources(insights, catalogMeetings);
  $: insightSourceCounts = new Map(
    [...insightSources].map(([id, sources]) => [id, sources.length]),
  );
  // An insight carries no tags, so a tag filter leaves none of them.
  $: roomInsights = activeTagIds.length > 0 ? [] : filterInsightsByRoom(insights, selectedRoomKey);
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
      window.addEventListener("focus", refreshOnReturn);
      document.addEventListener("visibilitychange", refreshOnReturn);
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
    refreshTags();

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
      catalogPending = false;
    }
  });

  onDestroy(() => {
    // Set before anything else: in-flight awaits check this to decide whether
    // they may still touch component state or browser history.
    destroyed = true;
    window.removeEventListener("popstate", handlePopState);
    window.removeEventListener("hashchange", handlePopState);
    window.removeEventListener("focus", refreshOnReturn);
    document.removeEventListener("visibilitychange", refreshOnReturn);
    tagLoader.stop();
    meetingMarks.stop();
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
      initialQuery={transcriptQueryForMeeting}
      {dataProvider}
      meeting={selectedMeeting}
      previewTags={meetingTags.get(selectedMeeting?.id ?? "") ?? []}
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
  <div class="browse-shell" class:with-tabs={importsOffered}>
    {#if importsOffered}
      <!-- Second-level tabs, visually subordinate to the shell's Browse/Operator
           nav above. A segmented control rather than another nav bar. -->
      <div class="browse-tabs" inert={topLayer > 0} role="tablist" aria-label="Browse sections">
        <button
          type="button"
          role="tab"
          class="browse-tab"
          class:active={browseTab === "meetings"}
          aria-selected={browseTab === "meetings"}
          on:click={() => (browseTab = "meetings")}
        >Meetings</button>
        <button
          type="button"
          role="tab"
          class="browse-tab"
          class:active={browseTab === "import"}
          aria-selected={browseTab === "import"}
          on:click={() => (browseTab = "import")}
        >
          Import
          {#if importActivity}<span class="tab-dot" role="status" aria-label="Imports are running"></span>{/if}
        </button>
      </div>
    {/if}
    <div class="contents" class:tab-hidden={browseTab === "import"} inert={topLayer > 0 && topLayer !== 3}>
    <RoomsRail
      rooms={roomBuckets}
      editable={!!dataProvider.loadRoomName && !!dataProvider.saveRoomName}
      on:rename={(event) => (editingRoomId = event.detail)}
      {selectedRoomKey}
      totalCount={catalogMeetings.length}
      open={railOpen}
      {insightsOffered}
      types={browseTypes}
      meetingCount={browseCounts.meetings}
      insightCount={browseCounts.insights}
      on:select={handleRoomSelect}
      on:close={() => (railOpen = false)}
      on:toggleType={(event) => (browseTypes = toggleBrowseType(browseTypes, event.detail as BrowseType))}
      tagsOffered={canTag}
      tags={vocabularyTags}
      {tagsFailed}
      roomsLoading={catalogPending}
      insightsLoading={!insightsLoaded && !insightsError}
      tagsLoading={canTag && !vocabularyTags && !tagsFailed}
      selectedTagIds={activeTagIds}
      {tagMatch}
      {audience}
      on:toggleTag={(event) => toggleTagFilter(event.detail)}
      on:tagMatch={(event) => (tagMatch = event.detail)}
      on:manageTags={() => (tagManagerOpen = true)}
    />
    </div>

    <div class="contents" class:tab-hidden={browseTab === "import"} inert={topLayer > 0}>
    <MeetingList
      meetings={roomMeetings}
      types={browseTypes}
      totalCount={catalogMeetings.length}
      insights={roomInsights}
      totalInsightCount={insights.length}
      {insightsOffered}
      {insightsLoaded}
      {insightsError}
      {insightSourceCounts}
      insightsRetryable={canRetryInsight}
      {retryingInsightId}
      {insightRetryError}
      {selectedInsightId}
      {selectedRoomName}
      {selectedMeetingId}
      {pickedIds}
      selectable={canPrepare}
      bottomOverlay={selectionBarUp}
      bottomOverlayHeight={selectionDockHeight}
      {ncMode}
      {themeMode}
      errorMessage={listError}
      loading={catalogPending || (insightsOffered && !insightsLoaded && !insightsError)}
      {searchOffered}
      {searchState}
      {searchMessage}
      {transcriptHits}
      {transcriptOnlyMeetings}
      {searchCoverage}
      on:query={(event) => handleSearchQuery(event.detail)}
      on:openMoment={(event) => openSearchMoment(event.detail)}
      on:select={(event) => loadCatalogMeeting(event.detail)}
      on:pick={handlePick}
      on:openInsight={(event) => openInsight(event.detail)}
      on:retryInsight={(event) => void retryInsightRun(event.detail)}
      on:visible={(event) => (visibleMeetings = event.detail)}
      on:counts={(event) => (browseCounts = event.detail)}
      on:clearRoom={() => (selectedRoomKey = null)}
      on:openRooms={() => (railOpen = true)}
      on:toggleTheme={toggleTheme}
      {meetingTags}
      tags={vocabularyTags}
      tagFilterCount={activeTagIds.length}
      tagFilterIds={activeTagIds}
      on:removeTag={(event) => toggleTagFilter(event.detail)}
      {tagNotice}
      {tagRetryable}
      on:tagMeeting={(event) => tagMeeting(event.detail.meeting, event.detail.pick)}
      deletable={canDelete}
      {deleteNotice}
      deleteRetryable={deleteFailed.length > 0}
      on:deleteMeeting={(event) => requestDeleteMeetings([event.detail])}
      on:dismissDeleteNotice={() => {
        deleteNotice = "";
        deleteFailed = [];
      }}
      on:retryDelete={() => void runMeetingDeletes(deleteFailed)}
      on:clearTags={() => (selectedTagIds = [])}
      on:dismissTagNotice={() => annotationNotice ? meetingMarks.dismiss(annotationNotice.meetingId) : listTagSession.dismissNotice()}
      on:retryTag={() => annotationNotice ? meetingMarks.retry(annotationNotice.meetingId) : listTagSession.retry()}
    />
    </div>

    {#if importsOffered}
      <ImportPanel
        {dataProvider}
        active={browseTab === "import"}
        on:activity={(event) => (importActivity = event.detail)}
        on:imported={() => void refreshCatalog()}
      />
    {/if}

    {#if selectionBarUp}
      <div class="selection-dock" class:tab-hidden={browseTab === "import"} bind:offsetHeight={selectionDockHeight} inert={topLayer > 0}>
        <SelectionBar
          count={selection.ids.length}
          hiddenCount={hiddenSelectedCount}
          droppedCount={selection.dropped.length}
          entries={pickedMeetings}
          hiddenIds={hiddenSelectedIds}
          {meetingTags}
          on:unpick={(event) => handlePick(event)}
          on:open={(event) => loadCatalogMeeting(event.detail)}
          on:clear={handleClearSelection}
          on:prepare={() => (prepareOpen = true)}
          on:dismissDropped={() => (selection = acknowledgeDropped(selection))}
          deletable={canDelete}
          {deleteBusy}
          on:delete={() => requestDeleteMeetings([...pickedMeetings])}
          tags={dataProvider.applyAnnotationBatch ? vocabularyTags : null}
          tagSelected={bulkTagState.selected}
          tagMixed={bulkTagState.mixed}
          tagReport={$listTagSession.bulkReport}
          tagRetry={$listTagSession.retryable}
          on:retryTag={() => listTagSession.retry()}
          on:tag={(event) => tagSelection(event.detail)}
        />
      </div>
    {/if}

    {#if railOpen}
      <button
        type="button"
        class="shell-scrim rail-scrim"
        tabindex="-1"
        inert={topLayer > 3}
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
        tabindex="-1"
        inert={topLayer > 1}
        aria-label={selectedInsight ? "Close the insight" : "Close the meeting"}
        transition:fade={scrimFade()}
        on:click={closeSheet}
      ></button>
      <aside
        class="meeting-sheet"
        class:tagging={!selectedInsight && Boolean(dataProvider.loadMeetingAnnotations)}
        inert={topLayer > 1}
        use:focusLayer
        transition:sheetSlide={{}}
      >
        {#if selectedInsight}
          <InsightDocument
            insight={selectedInsight}
            sources={insightSources.get(selectedInsight.id) ?? []}
            missingSourceCount={missingInsightSources(selectedInsight, insightSources.get(selectedInsight.id) ?? [])}
            documentMarkdown={insightDocument}
            documentError={insightDocumentError}
            documentLoading={insightDocumentLoading}
            canLoadDocument={canLoadInsightDocument}
            canRetry={canRetryInsight}
            filesUrl={insightFilesUrl}
            meetingUrl={insightSourceUrl}
            retrying={retryingInsightId === selectedInsight.id}
            retryError={insightRetryError?.id === selectedInsight.id ? insightRetryError.message : ""}
            canRename={canRenameInsight}
            renaming={renamingInsightId === selectedInsight.id}
            renameError={insightRenameError?.id === selectedInsight.id ? insightRenameError.message : ""}
            on:close={closeSheet}
            on:openSource={openInsightSource}
            on:retry={retrySelectedInsight}
            on:rename={renameSelectedInsight}
            on:renameCancel={() => (insightRenameError = null)}
          />
        {:else}
          <!-- The other direction (D-721): a meeting says which insights read
               it. It used to be a strip pinned under the sheet, below the whole
               transcript, where nobody scrolled to it; it is a section of the
               meeting now, under the summary. Still the shell's fact rather
               than the artifact's — what a meeting was used FOR is not part of
               the recording — so it is handed down rather than looked up. -->
          <MeetingView
            initialQuery={transcriptQueryForMeeting}
            {dataProvider}
            meeting={selectedMeeting}
            previewTags={meetingTags.get(selectedMeeting?.id ?? "") ?? []}
            bundled={false}
            inSheet={true}
            {isDesktop}
            {prefersReducedMotion}
            hasCatalog={catalogMeetings.length > 0}
            {notFoundMessage}
            {linkedInsights}
            {insightSourceCounts}
            on:back={handleBackToList}
            on:enriched={handleEnriched}
            on:renamed={handleRenamed}
            on:openInsight={(event) => openInsight(event.detail)}
            {canDelete}
            on:deleteMeeting={() => selectedMeeting && requestDeleteMeetings([selectedMeeting])}
            tagVocabulary={vocabularyTags ?? []}
            loadAnnotations={annotationCalls.load}
            applyAnnotations={annotationCalls.apply}
            marksSession={annotationCalls.session}
            on:tagsChanged={(event) => reconcileMeetingTags(event.detail)}
          />
        {/if}
      </aside>
    {/if}

    {#if prepareOpen}
      <button
        type="button"
        class="shell-scrim prepare-scrim"
        tabindex="-1"
        inert={topLayer > 2}
        aria-label="Close Prepare"
        transition:fade={scrimFade()}
        on:click={() => (prepareOpen = false)}
      ></button>
      <aside class="prepare-sheet" inert={topLayer > 2} use:focusLayer transition:sheetSlide={{}}>
        <PreparePanel
          entries={pickedMeetings}
          totals={selectionTotals}
          gaps={selectionGaps}
          loadBundle={loadSelectedBundle}
          loadMeeting={(entry) => dataProvider.loadMeetingForEntry(entry)}
          on:unpick={(event) => handlePick(event)}
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
            <slot name="prepare-generate" {entries} onInsightCreated={handleInsightCreated} />
          </svelte:fragment>
        </PreparePanel>
      </aside>
    {/if}

    <!-- Kept mounted while closed, so a job started before closing still reports. -->
    {#if dataProvider.updateTag}
      <TagManager
        tags={vocabularyTags}
        provider={dataProvider}
        open={tagManagerOpen}
        on:close={() => (tagManagerOpen = false)}
        on:changed={() => refreshTags(true)}
      />
    {/if}

    <!-- Deleting is the one action here that cannot be taken back, so it is
         the one action behind a dialog (D-861). -->
    {#if deleteConfirm}
      <ConfirmDialog
        title={deleteConfirm.length === 1
          ? `Delete “${deleteConfirm[0].title}”?`
          : `Delete ${deleteConfirm.length} meetings?`}
        body="Deleting removes the recording, transcript and tags for everyone who can see it. This cannot be undone."
        confirmLabel={deleteConfirm.length === 1 ? "Delete meeting" : `Delete ${deleteConfirm.length} meetings`}
        busy={deleteBusy}
        on:cancel={() => (deleteConfirm = null)}
        on:confirm={confirmDeleteMeetings}
      />
    {/if}
  </div>
{/if}

<!-- Keep the modal inside the themed root: in Nextcloud's shadow DOM, the
     color tokens are scoped here rather than on the document or shadow host. -->
{#if editingRoomId}
  {#key editingRoomId}
    <RoomNameDialog roomId={editingRoomId} initialName={roomBuckets.find((room) => room.roomId === editingRoomId)?.name ?? editingRoomId} provider={dataProvider}
      on:close={() => (editingRoomId = null)} on:saved={(event) => roomNameSaved(event.detail)}
      on:unavailable={() => refreshCatalog()} />
  {/key}
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
  /* With the Import tab offered, a tabs row spans both columns above the rail
     and the list (D-828). Only then: without it the single implicit row keeps
     the layout every other build has. */
  .browse-shell.with-tabs {
    grid-template-rows: auto minmax(0, 1fr);
  }
  .browse-tabs {
    grid-column: 1 / -1;
    display: flex;
    gap: 2px;
    width: max-content;
    margin: 8px 12px 0;
    padding: 2px;
    border: 1px solid var(--color-base-300);
    border-radius: calc(var(--radius-box, 12px) - 2px);
    background: var(--color-base-200);
  }
  .browse-tab {
    position: relative;
    padding: 4px 16px;
    border-radius: calc(var(--radius-box, 12px) - 4px);
    font-weight: 600;
    font-size: 0.875rem;
    color: var(--color-base-content);
    opacity: 0.65;
    cursor: pointer;
  }
  .browse-tab.active {
    background: var(--color-base-100);
    opacity: 1;
    box-shadow: 0 1px 2px oklch(0% 0 0 / 0.12);
  }
  .tab-dot {
    position: absolute;
    top: 4px;
    right: 6px;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--color-primary);
  }
  /* Hides a pane the inactive tab owns. !important because the wrappers carry
     Tailwind's `.contents` (display: contents), which would otherwise win. */
  .tab-hidden {
    display: none !important;
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
  .sheet-scrim,
  .prepare-scrim,
  .rail-scrim {
    -webkit-backdrop-filter: blur(3px);
    backdrop-filter: blur(3px);
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
    /* Never the whole width: a strip of the blurred list always shows beside
       it, the thing a reader clicks to go back. */
    --sheet-peek: 48px;
    width: min(680px, calc(100% - var(--sheet-peek)));
    display: flex;
    flex-direction: column;
    background-color: var(--color-base-200);
    border-left: 1px solid var(--color-base-300);
    box-shadow: -8px 0 30px oklch(0% 0 0 / 0.22);
  }
  /* Room for the marking rail on the left and the brackets on the right (D-746). */
  .meeting-sheet.tagging {
    width: min(940px, calc(100% - var(--sheet-peek)));
  }

  /* The selection bar floats over the list it belongs to — inset past the rail
     track, and absolute against the shell rather than fixed to the viewport,
     for exactly the reason the sheet is (see the note in the script). 288px is
     the rail's 268px plus the bar's own margin. */
  .selection-dock {
    position: absolute;
    left: 276px;
    right: 8px;
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
      left: 8px;
      right: 8px;
    }
    .prepare-sheet {
      top: auto;
      left: 0;
      right: 0;
      bottom: 0;
      width: 100%;
      height: 100%;
      border-left: 0;
      border-top: 0;
      border-radius: 0;
      box-shadow: 0 -8px 30px oklch(0% 0 0 / 0.22);
    }
    /* A side drawer on a phone leaves the content it covers unreachable and
       reads as a page; a bottom sheet reads as a layer over the list, which
       shows above it rather than beside it. */
    .meeting-sheet {
      --sheet-peek: 0px;
      top: auto;
      left: 0;
      right: 0;
      bottom: 0;
      width: 100%;
      height: 100%;
      border-left: 0;
      border-top: 0;
      border-radius: 0;
      box-shadow: 0 -8px 30px oklch(0% 0 0 / 0.22);
    }
  }
</style>
