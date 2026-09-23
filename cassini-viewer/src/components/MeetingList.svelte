<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { Sun, Moon, Search, PanelLeft, Tag, TriangleAlert, Users, X } from "@lucide/svelte";
  import { plural, type TagPick, type VocabularyTag } from "../viewer/annotations";
  import { filterByTagLabel, wholeTagState, type MeetingTags } from "../viewer/listTags";
  import { colorFor } from "../viewer/tagPalette";
  import TagChip from "./tags/TagChip.svelte";
  import TagPicker from "./tags/TagPicker.svelte";
  import {
    filterMeetingCatalogEntries,
    formatMeetingDateWithDay,
    formatMeetingDuration,
    type MeetingCatalogEntry,
  } from "../viewer/catalog";
  import { roomLabelOf } from "../viewer/rooms";
  import { formatClockTime } from "../core/transcript";
  import {
    ALL_BROWSE_TYPES,
    buildBrowseFeed,
    filterInsights,
    groupBrowseFeedByMonth,
    type BrowseTypeFilter,
    type InsightRecord,
  } from "../viewer/insights";
  import InsightCard from "./InsightCard.svelte";
  import type { MeetingSearchHit } from "../viewer/meetingSearch";

  // The browse list (D-420 V1, room-grouped in D-654, insights folded in for
  // D-721). Presentational: the shell owns the catalog, the insights, the room
  // selection and which of them is open; this owns only its text filter, which
  // kinds it is showing, and how the rows are laid out.

  // Already narrowed to the selected room by the shell. The room filter is NOT
  // applied here, because the rail and the result-line chip both need to agree
  // with it and neither is inside this component.
  export let meetings: MeetingCatalogEntry[] = [];
  // The whole catalog's size, for "12 of 47 meetings" — the denominator is the
  // archive, not the current room, so a narrowed list says so.
  export let totalCount = 0;
  // Non-null only when a room is selected; drives the clearable chip and the
  // narrow-viewport rooms button's label.
  export let selectedRoomName: string | null = null;
  export let selectedMeetingId = "";
  export let ncMode = false;
  export let themeMode: "saturn-light" | "saturn-dark" = "saturn-light";
  export let errorMessage = "";

  // Which meetings are PICKED for a context bundle (D-626) — a different
  // question from selectedMeetingId, which is the one that is OPEN. Both are
  // owned by the shell; the row renders them side by side and never lets one
  // stand in for the other.
  export let pickedIds: ReadonlySet<string> = new Set();
  // Whether picking is offered at all. False wherever nothing can assemble a
  // bundle — a standalone export has no operator behind it — because a checkbox
  // that leads to no action is a promise the build cannot keep.
  export let selectable = false;
  // Something floats over the bottom of the list (the selection bar), so the
  // last rows need room to clear it. The list does not know what it is; it only
  // knows not to hide its own last row under it.
  export let bottomOverlay = false;
  // How tall that overlay actually is, measured by the shell. The inset used
  // to be a fixed 96px guess, which a bar carrying a second line — a loss
  // notice, the cap refusal — grew past, leaving the last rows to scroll under
  // it with no way to reach them.
  export let bottomOverlayHeight = 0;

  // The insights drawn from the meetings in this room (D-721) — already
  // narrowed by the shell, for the same reason the meetings are: the room
  // filter has to agree with the rail, which is not inside this component.
  export let insights: InsightRecord[] = [];
  // Every insight this caller has, for the denominator, the way totalCount is
  // the archive rather than the room.
  export let totalInsightCount = 0;
  // Whether this build has insights at all. False for a standalone export,
  // whose provider cannot list them — and then there is no type filter,
  // because a control that narrows to a kind of thing which cannot exist here
  // is a promise the build cannot keep.
  export let insightsOffered = false;
  // Whether a listing has ever come back. A count of zero that was never loaded
  // reads as "there are none", which is the one thing it does not know.
  export let insightsLoaded = false;
  // Non-empty when the last listing failed. A failed fetch and an empty list
  // are different facts, and neither of them is "there are no insights here".
  export let insightsError = "";
  export let selectedInsightId = "";
  // How many of each insight's sources this caller can read, resolved by the
  // shell against the WHOLE catalog — not against this room's meetings, which
  // would undercount an insight that spans rooms, which most of them do.
  export let insightSourceCounts: ReadonlyMap<string, number> = new Map();
  // Retry from the card (D-749): offered where the shell's provider can, and
  // the shell says which run is mid-retry and what the last retry answered.
  export let insightsRetryable = false;
  export let retryingInsightId = "";
  export let insightRetryError: { id: string; message: string } | null = null;

  export let meetingTags: MeetingTags = new Map();
  // Null offers no row tag button: the build cannot tag, or the vocabulary has not loaded.
  export let tags: readonly VocabularyTag[] | null = null;
  export let tagFilterCount = 0;
  // The tags the list is narrowed by, in the order they were picked.
  export let tagFilterIds: readonly string[] = [];
  export let tagNotice = "";
  let tagging: { meeting: MeetingCatalogEntry; anchor: HTMLElement } | null = null;

  // The filter is list-local state — no other surface reads it.
  let filter = "";
  // Which kinds the list is showing. The SHELL owns this now: the control moved
  // into the rooms rail, beside the other narrowing of the same archive, and
  // two components reading one filter cannot each keep their own copy of it.
  export let types: BrowseTypeFilter = ALL_BROWSE_TYPES;

  // Cross-meeting search (D-736). Owned by the shell, like insights: this
  // component renders what came back and says which state it is in, but never
  // makes the request — the visible set behind it is resolved per request from
  // Nextcloud and that is not a presentational concern.
  //
  // searchOffered is false for a build with no operator (a standalone export),
  // where the box narrows names and dates and claims nothing about transcripts.
  export let searchOffered = false;
  // "idle" | "searching" | "ok" | "rateLimited" | "indexUnavailable" | "failed"
  //
  // Each failure is its own state ON PURPOSE. Folding any of them into an empty
  // result would tell the reader that nothing was said about what they asked,
  // which is the opposite of the truth when the archive is simply unreachable.
  export let searchState: string = "idle";
  export let searchMessage = "";
  // Matched moments, by meeting id, in the server's rank order.
  export let transcriptHits: ReadonlyMap<string, readonly MeetingSearchHit[]> = new Map();
  // Meetings that matched ONLY on what was said — they are not in the local
  // name/date filter's output, so the list has to add them.
  export let transcriptOnlyMeetings: MeetingCatalogEntry[] = [];
  // What the last search could actually look at. Invariant 4 of the design:
  // the index never claims coverage it does not have. Saying "no meeting
  // matches" after searching 4 of 30 is a false negative dressed as an answer.
  export let searchCoverage: { visible: number; searched: number } | null = null;

  const dispatch = createEventDispatcher<{
    select: MeetingCatalogEntry;
    pick: MeetingCatalogEntry;
    openInsight: InsightRecord;
    retryInsight: InsightRecord;
    visible: MeetingCatalogEntry[];
    counts: { meetings: number; insights: number };
    clearRoom: void;
    openRooms: void;
    toggleTheme: void;
    tagMeeting: { meeting: MeetingCatalogEntry; pick: TagPick };
    clearTags: void;
    removeTag: string;
    dismissTagNotice: void;
    // What was typed. The shell debounces it and asks the operator.
    query: string;
    // Open a meeting AT a matched moment, carrying the query so the meeting
    // view's own in-meeting filter (D-623 slice 7) can show the matching lines
    // in full — the words are fetched there as the caller, so Nextcloud still
    // re-checks the ACL on the bytes.
    openMoment: { entry: MeetingCatalogEntry; startMs: number; query: string };
  }>();

  function toggleTagging(meeting: MeetingCatalogEntry, anchor: HTMLElement) {
    tagging = tagging?.meeting.id === meeting.id ? null : { meeting, anchor };
  }

  // Title and date, as it has been since D-420. It still does NOT reach into
  // transcript text: that question is answered by the operator, because the
  // words are not in this array — which is also what finally makes the old
  // comment's worry ("a hit the list cannot show") moot, since a hit now
  // arrives WITH the quote that justifies it.
  // What the list can answer locally: the meeting's name or date, and now the
  // names of the tags it carries (D-772). Unioned by filtering the narrowed
  // array itself rather than concatenating two results, which keeps the
  // catalog's order and cannot list a meeting twice when it matched both ways.
  //
  // A tag match needs no new row furniture: the row already renders its chips,
  // so the reason a meeting is here is on screen — which is the rule the
  // original title/date-only filter was written to protect.
  // Tags join the promise only where there are any. An install with no tags
  // yet would otherwise offer to search something that cannot match, which is
  // the same empty promise searchOffered exists to avoid.
  $: tagsSearchable = meetingTags.size > 0;
  $: searchBoxLabel = searchOffered
    ? tagsSearchable
      ? `Search ${matchNounPlural}, their tags and what was said in them`
      : `Search ${matchNounPlural} and what was said in them`
    : tagsSearchable
      ? `Search ${matchNounPlural} by name, date or tag`
      : `Search ${matchNounPlural} by name or date`;

  $: localMatchIds = new Set([
    ...filterMeetingCatalogEntries(meetings, filter).map((meeting) => meeting.id),
    ...filterByTagLabel(meetings, meetingTags, filter).map((meeting) => meeting.id),
  ]);
  $: nameMatches =
    filter.trim() === "" ? meetings : meetings.filter((meeting) => localMatchIds.has(meeting.id));
  // Name/date matches keep the catalog's order and come first; meetings that
  // matched only on what was said follow, in the server's rank order.
  $: visibleMeetings = filter.trim() === "" ? nameMatches : [...nameMatches, ...transcriptOnlyMeetings];
  // The shell owns the request; this only says what was typed.
  $: dispatch("query", filter);
  $: isSearching = searchState === "searching";
  // A failure the reader has to be able to tell from "nothing matched".
  // True only when the last search saw everything the caller can read.
  $: searchCoveredEverything =
    !searchOffered ||
    searchCoverage === null ||
    searchCoverage.searched >= searchCoverage.visible;
  $: searchProblem =
    searchState === "rateLimited" || searchState === "indexUnavailable" || searchState === "failed"
      ? searchMessage || "Search is unavailable right now."
      : "";
  // Both kinds are narrowed by the same search box, and both counts are
  // computed whether or not their kind is being shown: the count is what
  // answers "is there anything behind that switch?".
  $: visibleInsights = insightsOffered ? filterInsights(insights, filter) : [];
  // The rail draws the Show boxes and has to say what is behind each of them,
  // which is a count under the current room AND the current search — and the
  // search is here. Reported before the type filter is applied, because the
  // question a box answers is "what would I get back if I ticked this?".
  $: dispatch("counts", {
    meetings: visibleMeetings.length,
    insights: visibleInsights.length,
  });
  $: feedItems = buildBrowseFeed({
    meetings: visibleMeetings,
    insights: visibleInsights,
    types,
  });
  $: feedGroups = groupBrowseFeedByMonth(feedItems);
  $: trimmedFilter = filter.trim();
  $: filterTags = tagFilterIds
    .map((id) => tags?.find((tag) => tag.tagId === id))
    .filter((tag): tag is VocabularyTag => tag !== undefined);
  $: shownFilterTags = filterTags.length > 3 ? filterTags.slice(0, 2) : filterTags;
  $: moreFilterTags = filterTags.length - shownFilterTags.length;
  $: narrowed = selectedRoomName !== null || trimmedFilter !== "" || tagFilterCount > 0;

  function clearFilters(): void {
    filter = "";
    if (selectedRoomName !== null) {
      dispatch("clearRoom");
    }
    if (tagFilterCount > 0) {
      dispatch("clearTags");
    }
  }
  // The one narrowing that can empty the list without the search doing it.
  $: insightsOnly = insightsOffered && types.insights && !types.meetings;
  // The empty state names what it looked for, so a list showing both kinds
  // does not report that no MEETING matched while an insight was hidden by the
  // same search.
  $: matchNoun = insightsOnly
    ? "insight"
    : insightsOffered && types.insights
      ? "meeting or insight"
      : "meeting";
  $: matchNounPlural = insightsOnly
    ? "insights"
    : insightsOffered && types.insights
      ? "meetings or insights"
      : "meetings";
  // The text filter is list-local by design, so the shell cannot compute what
  // the list is actually showing — and it has to, to say how many picked
  // meetings this narrowing hides (D-626). Reported rather than moved: the
  // filter belongs to the list.
  //
  // What is REPORTED is what the list renders, which is not visibleMeetings the
  // moment the type filter is switched off: with Meetings off the list draws no
  // meeting rows at all, and reporting the search-filtered set would leave the
  // selection bar saying "3 meetings selected" over a list showing none of them
  // and omitting "3 not shown here" — the exact claim
  // selectionModel.countHiddenByView exists to prevent.
  $: dispatch("visible", types.meetings ? visibleMeetings : []);
  $: if (tagging && !visibleMeetings.some((meeting) => meeting.id === tagging?.meeting.id)) {
    tagging = null;
  }
</script>

<section
  aria-label="Meeting list"
  class="meeting-list flex flex-col min-w-0 min-h-0 h-full bg-base-100"
>
  <header class="searchbar flex-none">
    <div class="search-row">
      <!-- Narrow only: the rail is off-canvas there, so this is the way back to
           it. Labelled with the live room so the button also reports what the
           list below is showing. -->
      <button
        type="button"
        class="rooms-button"
        on:click={() => dispatch("openRooms")}
        aria-label="Choose a room"
      >
        <PanelLeft size={15} aria-hidden="true" />
        <span
          >{selectedRoomName ?? "All"}{tagFilterCount > 0 ? ` · ${plural(tagFilterCount, "tag")}` : ""}</span
        >
      </button>

      <label class="search-field">
        <Search size={15} aria-hidden="true" />
        <!-- The label names the kinds the list is currently showing, because
             this box narrows both of them and narrows insights ALONE when the
             Meetings toggle is off. -->
        <input
          type="search"
          placeholder={searchBoxLabel}
          aria-label={searchBoxLabel}
          bind:value={filter}
        />
        {#if isSearching}
          <span class="search-status" aria-live="polite">Searching…</span>
        {/if}
        <!-- Our own, because the one the browser draws for type="search" is
             unthemeable: a blue gradient disc in the middle of a dark field. -->
        {#if trimmedFilter}
          <button type="button" class="search-clear" aria-label="Clear search" on:click={() => (filter = "")}>
            <X size={14} aria-hidden="true" />
          </button>
        {/if}
      </label>

      {#if !ncMode}
        <label class="theme-toggle">
          <Sun size={15} strokeWidth={2} class="text-base-content/70" aria-hidden="true" />
          <input
            type="checkbox"
            class="toggle toggle-xs rounded-lg text-base-content/80"
            aria-label="Toggle light or dark theme"
            checked={themeMode === "saturn-dark"}
            on:change={() => dispatch("toggleTheme")}
          />
          <Moon size={15} strokeWidth={2} class="text-base-content/70" aria-hidden="true" />
        </label>
      {/if}
    </div>

    <!-- The two kinds in the list, and which of them it is showing, are in the
         rooms rail: both are narrowings of the same archive and belong in the
         same column, which is the prototype's own arrangement. -->

    <!-- Fixed height: a chip appearing must not push the list down under the
         pointer. -->
    <div class="resultline" role="status">
      {#if types.meetings}
        <span>{narrowed ? `${visibleMeetings.length} of ${totalCount} meetings` : plural(totalCount, "meeting")}</span>
      {/if}
      <!-- A count once a listing has come back and there is something to
           count, the fact that it did not when it failed, and nothing at all
           while the first one is still in flight. -->
      {#if insightsOffered && insightsLoaded}
        {#if types.insights && totalInsightCount > 0}
          {#if types.meetings}<span class="rule" aria-hidden="true"></span>{/if}
          <span>{narrowed ? `${visibleInsights.length} of ${totalInsightCount} insights` : plural(totalInsightCount, "insight")}</span>
        {/if}
      {:else if insightsOffered && insightsError}
        <span class="rule" aria-hidden="true"></span>
        <span>Insights could not be listed.</span>
      {/if}
      {#if selectedRoomName !== null}
        <span class="chip">
          {selectedRoomName}
          <button
            type="button"
            on:click={() => dispatch("clearRoom")}
            aria-label="Show every room"
          >
            <X size={12} aria-hidden="true" />
          </button>
        </span>
      {/if}
      {#each shownFilterTags as tag (tag.tagId)}
        <TagChip
          label={tag.label}
          color={colorFor(tag)}
          icon={tag.icon}
          removable
          on:remove={() => dispatch("removeTag", tag.tagId)}
        />
      {/each}
      {#if moreFilterTags > 0}
        <span class="chip" title={filterTags.slice(shownFilterTags.length).map((tag) => tag.label).join(", ")}>
          +{moreFilterTags}
        </span>
      {:else if tagFilterCount > 0 && filterTags.length === 0}
        <span class="chip">
          {plural(tagFilterCount, "tag")}
          <button type="button" on:click={() => dispatch("clearTags")} aria-label="Clear tag filters">
            <X size={12} aria-hidden="true" />
          </button>
        </span>
      {/if}
      {#if trimmedFilter}
        <span class="chip">
          “{trimmedFilter}”
          <button type="button" on:click={() => (filter = "")} aria-label="Clear search">
            <X size={12} aria-hidden="true" />
          </button>
        </span>
      {/if}
      {#if narrowed}
        <button type="button" class="clear-filters" on:click={clearFilters}>Clear filters</button>
      {/if}
    </div>
  </header>

  <div
    class="list-scroll flex-1 min-h-0 overflow-y-auto overscroll-contain scroll-stable"
    class:list-scroll-inset={bottomOverlay}
    style={bottomOverlay ? `--list-bottom-inset: ${Math.round(bottomOverlayHeight) + 24}px` : undefined}
  >
    <!-- Stated here rather than swallowed: the list below is complete for
         meetings and incomplete for insights, and only one of those two things
         went wrong. -->
    {#if insightsError}
      <p class="list-note list-note-error" role="status">
        <TriangleAlert size={14} aria-hidden="true" />
        <span>Insights could not be listed.</span>
      </p>
    {/if}
    {#if tagNotice}
      <p class="list-note" role="status">
        <TriangleAlert size={14} aria-hidden="true" />
        <span>
          {tagNotice}
          <button type="button" class="link" on:click={() => dispatch("dismissTagNotice")}>Dismiss</button>
        </span>
      </p>
    {/if}
    {#if totalCount === 0 && totalInsightCount === 0}
      <div class="list-empty">
        <strong>No meetings yet</strong>
        <span>Published recordings appear here.</span>
      </div>
    <!-- "You have never made one" is a claim about the whole archive, so every
         narrowing has to be off and the listing has to have come back before it
         can be made. A room selected, a search typed, a listing still in flight
         or a listing that FAILED each make it false — and the branch below
         already has the right words for all four, including the "Show every
         room" way out of the room case. -->
    {:else if feedItems.length === 0 && insightsOnly && !trimmedFilter && selectedRoomName === null && insightsLoaded && !insightsError && totalInsightCount === 0}
      <div class="list-empty">
        <strong>No insights yet</strong>
        <span>Pick some meetings, ask one question of them, and the answer is kept here beside them.</span>
      </div>
      <!-- Everything the branch above will not claim. The last two arms are the
           states it is gated on: nothing is narrowing the list and it is still
           empty, so the reason is the listing itself — and a listing that failed
           and one still in flight are different facts, neither of which is "you
           have none". -->
    {:else if feedItems.length === 0 && insightsOnly && !trimmedFilter && selectedRoomName === null && !insightsLoaded && !insightsError}
      <div class="list-empty">
        <strong>Loading insights…</strong>
      </div>
    {:else if feedItems.length === 0}
      <div class="list-empty">
        <strong>Nothing matches</strong>
        <span>
          {#if tagFilterCount > 0 && insightsOnly}
            Insights carry no tags, so a tag filter hides them.
          {:else if tagFilterCount > 0}
            No meeting here has {tagFilterCount === 1 ? "that tag" : "those tags"}{trimmedFilter
              ? " and matches that search"
              : ""}.
          {:else if selectedRoomName !== null && trimmedFilter}
            No {matchNoun} in {selectedRoomName} matches that search.
          {:else if selectedRoomName !== null}
            {selectedRoomName} has no {matchNounPlural}.
          {:else if trimmedFilter && searchProblem}
            <!-- NOT "nothing matches": the transcript half of the question was
                 never answered, and saying nothing matched would be a claim the
                 search never got to make. -->
            Names and dates match no {matchNoun}, and what was said could not be
            searched.
          {:else if trimmedFilter && !searchCoveredEverything}
            No {matchNoun} matches that search — but only {searchCoverage?.searched}
            of {searchCoverage?.visible} could be searched for what was said in them.
          {:else if trimmedFilter}
            No {matchNoun} matches that search.
          {:else if insightsOnly && insightsError}
            Insights could not be listed.
          {:else}
            There are no {matchNounPlural} to show.
          {/if}
        </span>
        {#if tagFilterCount > 0}
          <button type="button" class="list-empty-action" on:click={() => dispatch("clearTags")}>
            Clear tag filter
          </button>
        {:else if trimmedFilter}
          <button type="button" class="list-empty-action" on:click={() => (filter = "")}>
            Clear search
          </button>
        {:else if selectedRoomName !== null}
          <button
            type="button"
            class="list-empty-action"
            on:click={() => dispatch("clearRoom")}
          >
            Show every room
          </button>
        {/if}
      </div>
    {:else}
      {#each feedGroups as group (group.key)}
        <h3 class="group-head">{group.label}</h3>
        {#each group.items as item (item.key)}
          {#if item.kind === "insight"}
            <!-- No checkbox, ever: a context bundle is made of meetings, and an
                 insight is what came out of one. It opens in the same sheet a
                 meeting does. -->
            <InsightCard
              insight={item.insight}
              sourceCount={insightSourceCounts.get(item.insight.id) ?? 0}
              selected={item.insight.id === selectedInsightId}
              canRetry={insightsRetryable}
              retrying={retryingInsightId === item.insight.id}
              retryError={insightRetryError?.id === item.insight.id ? insightRetryError.message : ""}
              on:open={() => dispatch("openInsight", item.insight)}
              on:retry={() => dispatch("retryInsight", item.insight)}
            />
          {:else}
            {@const meeting = item.meeting}
            {@const rowTags = meetingTags.get(meeting.id) ?? []}
            {@const rowRoom = roomLabelOf(meeting)}
            {@const showRoom = selectedRoomName === null && rowRoom !== meeting.title}
            <!-- The row is a container, not a control, so that picking and
                 opening can sit side by side: a checkbox cannot live inside a
                 button, and demoting the whole row to a click-handling div would
                 cost it keyboard focus. `.meeting-row` and aria-current stay on
                 THIS element because that pair is what the row's open state is
                 styled from, in this file and in app.css's high-contrast
                 hover rule; the open button repeats aria-current because that is
                 the element a screen reader lands on. -->
            <div
              class="meeting-row"
              class:row-pickable={selectable}
              aria-current={meeting.id === selectedMeetingId ? "page" : undefined}
              class:row-picked={selectable && pickedIds.has(meeting.id)}
            >
              {#if selectable}
                <label class="row-pick">
                  <input
                    type="checkbox"
                    class="cassini-check"
                    checked={pickedIds.has(meeting.id)}
                    aria-label={`Select ${meeting.title}`}
                    on:change={() => dispatch("pick", meeting)}
                  />
                </label>
              {/if}
              <button
                type="button"
                class="row-open"
                aria-current={meeting.id === selectedMeetingId ? "page" : undefined}
                on:click={() => dispatch("select", meeting)}
              >
                <span class="row-main">
                  <span class="row-title">{meeting.title}</span>
                  <span class="row-meta">
                    <span>{formatMeetingDateWithDay(meeting.dateLabel)}</span>
                    {#if showRoom}
                      <span class="rule" aria-hidden="true"></span>
                      <span class="row-room">{rowRoom}</span>
                    {/if}
                    {#if typeof meeting.speakerCount === "number"}
                      <span class="rule" aria-hidden="true"></span>
                      <span
                        class="row-speakers"
                        title={plural(meeting.speakerCount, "speaker")}
                        aria-label={plural(meeting.speakerCount, "speaker")}
                      >
                        <Users size={12} aria-hidden="true" />{meeting.speakerCount}
                      </span>
                    {/if}
                  </span>
                  {#if rowTags.length > 0}
                    <span class="row-tags">
                      {#each rowTags.slice(0, 3) as { tag } (tag.tagId)}
                        <TagChip label={tag.label} color={colorFor(tag)} icon={tag.icon} />
                      {/each}
                      {#if rowTags.length > 3}
                        <span class="row-tags-more" title={rowTags.slice(3).map(({ tag }) => tag.label).join(", ")}
                          >+{rowTags.length - 3}</span
                        >
                      {/if}
                    </span>
                  {/if}
                </span>
              </button>
              {#if tags}
                <button
                  type="button"
                  class="row-tag"
                  aria-haspopup="dialog"
                  aria-expanded={tagging?.meeting.id === meeting.id}
                  aria-label={`Tag ${meeting.title}`}
                  title="Tag this meeting"
                  on:click={(event) => toggleTagging(meeting, event.currentTarget)}
                >
                  <Tag size={16} aria-hidden="true" />
                </button>
              {/if}
              {#if typeof meeting.digestDurationMs === "number"}
                <span class="row-duration">
                  {formatMeetingDuration(meeting.digestDurationMs)}
                </span>
              {/if}
              <!-- OUTSIDE row-open on purpose: each moment is its own button,
                   and a button inside a button is invalid markup that browsers
                   resolve by dropping one of them. -->
              {#if (transcriptHits.get(meeting.id) ?? []).length > 0}
                <div class="row-moments">
                  {#each transcriptHits.get(meeting.id) ?? [] as moment (moment.segmentId + moment.startMs)}
                    <button
                      type="button"
                      class="row-moment"
                      title={moment.snippet}
                      on:click={() =>
                        dispatch("openMoment", {
                          entry: meeting,
                          startMs: moment.startMs,
                          query: filter,
                        })}
                    >
                      <span class="row-moment-at">{formatClockTime(moment.startMs)}</span>
                      <span class="row-snippet">{moment.snippet || "matched here"}</span>
                      {#if moment.matched === "alias"}
                        <!-- Finding "casino" is not the same as finding
                             "cassini", and a row that hides the difference is
                             lying by omission. -->
                        <span class="row-moment-alias" title="Matched a known mistranscription">~</span>
                      {/if}
                    </button>
                  {/each}
                </div>
              {/if}
            </div>
          {/if}
        {/each}
      {/each}
    {/if}
  </div>

  {#if tags && tagging}
    {@const meeting = tagging.meeting}
    <TagPicker
      {tags}
      label={`Tag ${meeting.title}`}
      multiple
      order="alphabetical"
      selected={wholeTagState(meetingTags, [meeting.id]).selected}
      anchor={tagging.anchor}
      on:pick={(event) => dispatch("tagMeeting", { meeting, pick: event.detail })}
      on:close={() => (tagging = null)}
    />
  {/if}

  <!-- A search failure is NOT an empty result, and must not read as one: the
       list below may be showing name matches only, and the reader has to know
       that the transcript half of their question went unanswered. -->
  {#if searchProblem && filter.trim() !== ""}
    <div class="search-problem" role="status">{searchProblem}</div>
  {/if}

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

<style>
  .meeting-list {
    --list-x: 20px;
  }
  @media (max-width: 720px) {
    .meeting-list {
      --list-x: 1rem;
    }
  }

  /* Plain CSS for the list surface: the rows are a repeated, dense layout with
     a hairline rule and three interlocking states (hover, open, group heading),
     which is shorter and easier to keep coherent here than as utility stacks
     on every row. */
  .search-status {
    flex: none;
    font-size: 0.6875rem;
    color: color-mix(in oklab, var(--color-base-content) 55%, transparent);
    white-space: nowrap;
  }

  .search-problem {
    flex: none;
    margin: 0 var(--list-x) 0.5rem;
    padding: 0.5rem 0.75rem;
    border-radius: 0.5rem;
    font-size: 0.75rem;
    line-height: 1.35;
    color: var(--color-warning-content, inherit);
    background-color: color-mix(in oklab, var(--color-warning) 18%, transparent);
    border: 1px solid color-mix(in oklab, var(--color-warning) 40%, transparent);
  }

  /* The matched moments under a row. `speaker: quote` is the mock's
     .row-snippet; the timestamps are what make each one reachable. */
  .row-moments {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    margin-top: 0.25rem;
  }

  .row-moment {
    display: flex;
    align-items: baseline;
    gap: 0.4rem;
    padding: 0.1rem 0;
    background: none;
    border: 0;
    font: inherit;
    font-size: 0.75rem;
    text-align: left;
    color: color-mix(in oklab, var(--color-base-content) 75%, transparent);
    cursor: pointer;
  }

  .row-moment:hover .row-snippet {
    color: var(--color-base-content);
  }

  .row-moment-at {
    flex: none;
    font-variant-numeric: tabular-nums;
    color: color-mix(in oklab, var(--color-base-content) 55%, transparent);
  }

  .row-snippet {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .row-moment-alias {
    flex: none;
    font-size: 0.6875rem;
    opacity: 0.7;
  }

  .searchbar {
    z-index: 5;
    padding: 1rem var(--list-x) 6px;
    background-color: var(--color-base-100);
    border-bottom: 1px solid var(--color-base-300);
  }

  .search-row {
    display: flex;
    align-items: stretch;
    gap: 0.5rem;
  }

  /* Shown only where the rail is a drawer. */
  .rooms-button {
    display: none;
    flex: none;
    align-items: center;
    gap: 6px;
    max-width: 42vw;
    padding: 0 12px;
    cursor: pointer;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.8125rem;
    font-weight: 550;
    color: var(--color-base-content);
  }
  .rooms-button:hover {
    border-color: color-mix(in oklch, var(--color-base-content) 35%, transparent);
  }
  .rooms-button span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .search-field {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0 12px;
    height: 40px;
    background-color: var(--color-base-200);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 16%, var(--color-base-200));
    border-radius: var(--radius-field, 0.5rem);
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .search-field:focus-within {
    border-color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
    box-shadow: 0 0 0 3px color-mix(in oklch, var(--color-base-content) 11%, var(--color-base-100));
  }
  .search-field input {
    flex: 1;
    min-width: 0;
    background: none;
    border: 0;
    outline: none;
    font-size: 0.9375rem;
    color: var(--color-base-content);
  }
  .search-field input::-webkit-search-cancel-button,
  .search-field input::-webkit-search-decoration {
    -webkit-appearance: none;
    appearance: none;
  }
  .search-clear {
    display: inline-flex;
    flex: none;
    padding: 4px;
    margin-right: -4px;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .search-clear:hover {
    color: var(--color-base-content);
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
  }

  .search-field input::placeholder {
    color: color-mix(in oklch, var(--color-base-content) 50%, transparent);
  }

  .theme-toggle {
    display: flex;
    flex: none;
    align-items: center;
    gap: 0.375rem;
    cursor: pointer;
  }

  .resultline {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    height: 30px;
    margin-top: 6px;
    font-size: 0.75rem;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
    overflow: hidden;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }

  .clear-filters {
    flex: none;
    padding: 0;
    cursor: pointer;
    background: none;
    border: 0;
    font-size: 0.75rem;
    font-weight: 550;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .clear-filters:hover {
    color: var(--color-base-content);
  }

  /* Sized like a tag chip (TagChip.svelte), without the dot: a filter is not
     one of the vocabulary's colours. */
  .chip {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 4px;
    min-width: 0;
    max-width: 16rem;
    box-sizing: border-box;
    height: 22px;
    padding: 0 7px 0 8px;
    font-size: 11.5px;
    font-weight: 550;
    line-height: 1;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: color-mix(in oklch, var(--color-base-content) 85%, transparent);
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 16%, transparent);
    border-radius: 5px;
  }

  .resultline :global(.tag-chip) {
    height: 22px;
    gap: 4px;
    padding: 0 7px 0 8px;
  }

  .chip button {
    display: inline-flex;
    flex: none;
    margin: -2px 0;
    padding: 0 0 0 2px;
    background: none;
    border: 0;
    cursor: pointer;
    color: inherit;
    opacity: 0.7;
  }
  .chip button:hover {
    opacity: 1;
  }

  /* The rows' own separator, so one list punctuates its counts and its rows
     the same way. */
  .resultline .rule {
    flex: none;
    width: 1px;
    height: 10px;
    margin: 0 2px;
    background-color: color-mix(in oklch, var(--color-base-content) 22%, transparent);
  }

  /* An incomplete list says so where the list is, not in the footer with the
     catalog's own errors: the two failures are independent and either one can
     happen without the other. */
  .list-note {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: start;
    gap: 0.5rem;
    margin: 1rem var(--list-x) 0;
    padding: 0.625rem 0.75rem;
    font-size: 0.8125rem;
    line-height: 1.45;
    background-color: color-mix(in oklch, var(--color-warning) 20%, transparent);
    border-radius: var(--radius-field, 0.5rem);
    color: var(--color-base-content);
  }
  .list-note :global(svg) {
    margin-top: 1px;
    color: var(--color-warning, #b45309);
  }
  /* A listing that failed is not a notice about the archive: nothing here is
     going to fill that gap until it is fixed. */
  .list-note-error {
    background-color: color-mix(in oklch, var(--color-error) 15%, transparent);
    border: 1px solid color-mix(in oklch, var(--color-error) 45%, transparent);
  }
  .list-note-error :global(svg) {
    color: var(--color-error);
  }

  .group-head {
    position: sticky;
    top: 0;
    z-index: 2;
    padding: var(--list-x) var(--list-x) 0.5rem;
    font-size: 11px;
    font-weight: 650;
    line-height: 1;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    background-color: var(--color-base-100);
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }

  /* Room for whatever floats over the bottom of the list, so its last row can
     be scrolled clear of it rather than sitting permanently underneath. */
  .list-scroll-inset {
    padding-bottom: var(--list-bottom-inset, 96px);
  }

  .meeting-row {
    position: relative;
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    grid-auto-flow: column;
    column-gap: 0.25rem;
    align-items: center;
    width: 100%;
    padding: 9px var(--list-x);
    color: var(--color-base-content);
    transition: background-color 0.15s ease;
  }
  @media (prefers-reduced-motion: reduce) {
    .meeting-row {
      transition: none;
    }
  }
  /* Only when picking is offered: without the checkbox the row keeps exactly
     the geometry it had before D-626. */
  .row-pickable {
    grid-template-columns: auto minmax(0, 1fr);
  }

  /* The open action fills the rest of the row, so a click anywhere but the
     checkbox still opens the meeting — the two never compete for the same
     pixel. */
  .row-open {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    align-items: center;
    gap: 0.75rem;
    width: 100%;
    padding: 0;
    text-align: left;
    cursor: pointer;
    background: none;
    border: 0;
    color: inherit;
  }
  /* The button sits inside the row's padding, so its own box stops 20px short
     of each side and 9px short of top and bottom. Before D-626 the row WAS the
     button and all of that opened the meeting; without this the strip only
     lights up on hover and does nothing when clicked. Stretched over the padded
     row (which is the positioned ancestor) rather than moved onto the button,
     so the row keeps one geometry in both the pickable and plain layouts. */
  .row-open::before {
    content: "";
    position: absolute;
    inset: 0;
  }

  .row-pick {
    display: flex;
    flex: none;
    align-items: center;
    /* Above the open action's hit area: the checkbox keeps its own pixels, and
       the two still never compete for the same one. */
    position: relative;
    z-index: 1;
    /* Padding, not a bigger box: the hit target has to be thumb-sized without
       pushing the title off its baseline. */
    padding: 6px;
    margin: -6px 0.5rem -6px -6px;
    cursor: pointer;
  }
  .row-pick:hover input:not(:checked),
  .meeting-row:hover .row-pick input:not(:checked) {
    border-color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  .row-pick input:checked {
    background-color: var(--color-primary);
    border-color: var(--color-primary);
  }
  .row-pick input:checked::after {
    border-color: var(--color-primary-content);
  }
  /* Inset to the row's padding so the rule separates rows rather than cutting
     the column edge to edge. */
  .meeting-row::after {
    content: "";
    position: absolute;
    left: var(--list-x);
    right: var(--list-x);
    bottom: 0;
    height: 1px;
    /* Decoration: it is painted over the open action's hit area, and a rule
       that swallowed a click would put a dead line across every row. */
    pointer-events: none;
    background-color: var(--color-base-300);
  }
  /* Hover is the rail's shade and nothing more; picked and open are the
     insight card's open tint (24% of the accent in that shade), and the colour
     alone says it: no rule down the side. */
  .meeting-row:hover {
    background-color: var(--color-base-200);
  }
  .meeting-row.row-picked::after,
  .meeting-row[aria-current="page"]::after {
    background-color: color-mix(in oklch, var(--color-base-content) 14%, transparent);
  }
  .meeting-row.row-picked,
  .meeting-row[aria-current="page"] {
    background-color: color-mix(in oklch, var(--color-primary) 24%, var(--color-base-200));
  }
  .meeting-row[aria-current="page"] {
    transition: none;
  }

  .row-main {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: center;
    gap: 2px 12px;
    min-width: 0;
  }
  .row-title {
    grid-column: 1 / -1;
    line-height: 22px;
    font-weight: 550;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* Wraps, so tag chips that don't fit go under the date instead of crushing it. */
  .row-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 2px 8px;
    min-width: 0;
    font-size: 0.75rem;
    line-height: 18px;
    font-variant-numeric: tabular-nums;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .row-speakers {
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }
  .row-room {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row-meta .rule {
    flex: none;
    width: 1px;
    height: 10px;
    margin: 0 2px;
    background-color: color-mix(in oklch, var(--color-base-content) 22%, transparent);
  }
  .row-tags {
    display: inline-flex;
    gap: 4px;
    min-width: 0;
    overflow: hidden;
  }
  .row-tags-more {
    flex: none;
    font-weight: 600;
  }
  /* Above the open action's hit area, like the checkbox. Hidden, not removed, so it stays in the tab order. */
  .row-tag {
    position: relative;
    z-index: 1;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    padding: 0;
    cursor: pointer;
    background: none;
    border: 1px solid transparent;
    border-radius: var(--radius-field, 0.5rem);
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
    opacity: 0;
  }
  .meeting-row:hover .row-tag,
  .row-tag:focus-visible,
  .row-tag[aria-expanded="true"] {
    opacity: 1;
    background-color: var(--color-base-100);
    border-color: var(--color-base-300);
  }
  @media (hover: none), (max-width: 720px) {
    .row-tag {
      opacity: 1;
    }
  }
  .row-duration {
    flex: none;
    min-width: 4.5ch;
    text-align: right;
    font-size: 0.75rem;
    font-weight: 500;
    font-variant-numeric: tabular-nums;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }

  .list-empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.5rem;
    padding: 3rem var(--list-x);
    text-align: center;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .list-empty strong {
    font-size: 0.9375rem;
    font-weight: 600;
    color: var(--color-base-content);
  }
  .list-empty-action {
    margin-top: 0.25rem;
    padding: 6px 12px;
    cursor: pointer;
    background: none;
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    color: var(--color-base-content);
  }
  .list-empty-action:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
  }

  @media (max-width: 720px) {
    .rooms-button {
      display: flex;
    }
    /* The row's own controls line up with the title's line, not with the
       middle of a block that has grown a second and third line under it. A row
       whose text fits on one line is unchanged: its first line IS the row. */
    .meeting-row {
      align-items: start;
    }
    .row-pick,
    .row-tag,
    .row-duration {
      align-self: start;
      min-height: 22px;
      display: flex;
      align-items: center;
    }
    /* Two pixels below the title's own centre: the title's cap height sits
       high in its line box, so a mathematically centred control reads as
       riding above the word beside it. */
    .row-tag,
    .row-duration {
      margin-top: 2px;
    }
    /* The checkbox's hit area hangs 6px above its box, which at the top of the
       row is 6px above the title's line rather than around it. */
    .row-pick {
      margin-top: -1px;
    }
    .row-tag {
      width: 22px;
      height: 22px;
    }
    .row-main {
      grid-template-columns: minmax(0, 1fr);
    }

    .row-tag :global(svg) {
      width: 14px;
      height: 14px;
    }
  }
</style>
