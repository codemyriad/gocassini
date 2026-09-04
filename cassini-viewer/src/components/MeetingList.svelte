<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { Sun, Moon, Search, PanelLeft, X } from "@lucide/svelte";
  import {
    filterMeetingCatalogEntries,
    formatMeetingDateShort,
    formatMeetingDuration,
    type MeetingCatalogEntry,
  } from "../viewer/catalog";
  import { roomLabelOf } from "../viewer/rooms";
  import {
    ALL_BROWSE_TYPES,
    buildBrowseFeed,
    filterInsights,
    groupBrowseFeedByMonth,
    isLastBrowseType,
    toggleBrowseType,
    type BrowseTypeFilter,
    type InsightRecord,
  } from "../viewer/insights";
  import InsightCard from "./InsightCard.svelte";

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

  // The filter is list-local state — no other surface reads it.
  let filter = "";
  // Which kinds the list is showing. List-local for the same reason: unlike the
  // room, nothing outside this component narrows by it.
  let types: BrowseTypeFilter = ALL_BROWSE_TYPES;

  const dispatch = createEventDispatcher<{
    select: MeetingCatalogEntry;
    pick: MeetingCatalogEntry;
    openInsight: InsightRecord;
    visible: MeetingCatalogEntry[];
    clearRoom: void;
    openRooms: void;
    toggleTheme: void;
  }>();

  // Title and date, as it has been since D-420 — the ticket's "keep the
  // existing date/name filter working inside a room". It deliberately does NOT
  // reach into transcript text: a hit the list cannot show is a hit that looks
  // like a bug.
  $: visibleMeetings = filterMeetingCatalogEntries(meetings, filter);
  // Both kinds are narrowed by the same search box, and both counts are
  // computed whether or not their kind is being shown: the count is what
  // answers "is there anything behind that switch?".
  $: visibleInsights = insightsOffered ? filterInsights(insights, filter) : [];
  // A build with no insights must not be left narrowed to insights: the control
  // that would put the meetings back is not rendered there.
  $: if (!insightsOffered) {
    types = ALL_BROWSE_TYPES;
  }
  $: feedItems = buildBrowseFeed({
    meetings: visibleMeetings,
    insights: visibleInsights,
    types,
  });
  $: feedGroups = groupBrowseFeedByMonth(feedItems);
  $: trimmedFilter = filter.trim();
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
        <span>{selectedRoomName ?? "All"}</span>
      </button>

      <label class="search-field">
        <Search size={15} aria-hidden="true" />
        <!-- The label names the kinds the list is currently showing, because
             this box narrows both of them and narrows insights ALONE when the
             Meetings toggle is off. -->
        <input
          type="search"
          placeholder={`Search ${matchNounPlural} by name or date`}
          aria-label={`Search ${matchNounPlural} by name or date`}
          bind:value={filter}
        />
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

    <!-- The two kinds in the list, and which of them it is showing. Only where
         insights exist at all: a static export has none and gets the list it
         has always had. The prototype puts this in the rooms rail; it lives
         here because the rail is one narrowing of the archive and this is
         another one of the same list, beside the search that already narrows
         it the same way. -->
    {#if insightsOffered}
      <div class="typefilter" role="group" aria-label="Show">
        <button
          type="button"
          class="type-toggle"
          aria-pressed={types.meetings}
          disabled={isLastBrowseType(types, "meetings")}
          on:click={() => (types = toggleBrowseType(types, "meetings"))}
        >
          Meetings
        </button>
        <button
          type="button"
          class="type-toggle"
          aria-pressed={types.insights}
          disabled={isLastBrowseType(types, "insights")}
          on:click={() => (types = toggleBrowseType(types, "insights"))}
        >
          Insights
        </button>
      </div>
    {/if}

    <!-- Fixed height: a chip appearing must not push the list down under the
         pointer. -->
    <div class="resultline" role="status">
      <span>{visibleMeetings.length} of {totalCount} meetings</span>
      <!-- Three states, and none of them is the other two: a count once a
           listing has come back, the fact that it did not when it failed, and
           nothing at all while the first one is still in flight. A "0" that was
           never loaded would be a claim nobody made. -->
      {#if insightsOffered && insightsLoaded}
        <span class="dot" aria-hidden="true"></span>
        <span>{visibleInsights.length} of {totalInsightCount} insights</span>
      {:else if insightsOffered && insightsError}
        <span class="dot" aria-hidden="true"></span>
        <span>insights unavailable</span>
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
      {#if trimmedFilter}
        <span class="chip">
          “{trimmedFilter}”
          <button type="button" on:click={() => (filter = "")} aria-label="Clear search">
            <X size={12} aria-hidden="true" />
          </button>
        </span>
      {/if}
    </div>
  </header>

  <div
    class="list-scroll flex-1 min-h-0 overflow-y-auto overscroll-contain scroll-stable"
    class:list-scroll-inset={bottomOverlay}
  >
    <!-- Stated here rather than swallowed: the list below is complete for
         meetings and incomplete for insights, and only one of those two things
         went wrong. -->
    {#if insightsError}
      <p class="list-note" role="status">
        Insights could not be listed: {insightsError} The meetings are unaffected.
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
    {:else if feedItems.length === 0}
      <div class="list-empty">
        <strong>Nothing matches</strong>
        <span>
          {#if selectedRoomName !== null && trimmedFilter}
            No {matchNoun} in {selectedRoomName} matches that search.
          {:else if selectedRoomName !== null}
            {selectedRoomName} has no {matchNounPlural}.
          {:else if trimmedFilter}
            No {matchNoun} matches that search.
          {:else if insightsOnly && !insightsLoaded}
            Your insights are still loading.
          {:else if insightsOnly && insightsError}
            Your insights could not be listed, so none can be shown here.
          {:else}
            There are no {matchNounPlural} to show.
          {/if}
        </span>
        {#if trimmedFilter}
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
              on:open={() => dispatch("openInsight", item.insight)}
            />
          {:else}
            {@const meeting = item.meeting}
            <!-- The row is a container, not a control, so that picking and
                 opening can sit side by side: a checkbox cannot live inside a
                 button, and demoting the whole row to a click-handling div would
                 cost it keyboard focus. `.meeting-row` and aria-current stay on
                 THIS element because that pair is what the row's open state is
                 styled from, in this file and in app.css's Nextcloud-theme
                 override; the open button repeats aria-current because that is
                 the element a screen reader lands on. -->
            <div
              class="meeting-row"
              class:row-pickable={selectable}
              aria-current={meeting.id === selectedMeetingId ? "page" : undefined}
            >
              {#if selectable}
                <label class="row-pick">
                  <input
                    type="checkbox"
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
                    <span>{formatMeetingDateShort(meeting.dateLabel)}</span>
                    <span class="dot" aria-hidden="true"></span>
                    <span class="row-room">{roomLabelOf(meeting)}</span>
                    {#if typeof meeting.speakerCount === "number"}
                      <span class="dot" aria-hidden="true"></span>
                      <span>{meeting.speakerCount} speakers</span>
                    {/if}
                  </span>
                </span>
                {#if typeof meeting.digestDurationMs === "number"}
                  <span class="row-duration">
                    {formatMeetingDuration(meeting.digestDurationMs)}
                  </span>
                {/if}
              </button>
            </div>
          {/if}
        {/each}
      {/each}
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

<style>
  /* Plain CSS for the list surface: the rows are a repeated, dense layout with
     a hairline rule and three interlocking states (hover, open, group heading),
     which is shorter and easier to keep coherent here than as utility stacks
     on every row. */
  .searchbar {
    z-index: 5;
    padding: 1rem 1.25rem 0.75rem;
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
    padding: 0 0.75rem;
    height: 2.375rem;
    /* Same ground as the list below it: the border defines the field, not a
       change of surface. */
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .search-field:focus-within {
    border-color: var(--color-primary);
    box-shadow: 0 0 0 3px
      color-mix(in oklch, var(--color-primary) 15%, transparent);
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
    margin-top: 0.5rem;
    font-size: 0.75rem;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
    overflow: hidden;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }

  /* An active narrowing is state, not decoration: primary-coloured so it is
     obvious the list is filtered rather than complete. */
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    min-width: 0;
    padding: 3px 8px;
    border-radius: 20px;
    background-color: color-mix(
      in oklch,
      var(--color-primary) 15%,
      transparent
    );
    border: 1px solid
      color-mix(in oklch, var(--color-primary) 38%, transparent);
    font-size: 0.71875rem;
    font-weight: 550;
    color: var(--color-primary);
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .chip button {
    display: inline-flex;
    flex: none;
    padding: 0;
    background: none;
    border: 0;
    cursor: pointer;
    color: color-mix(in oklch, var(--color-primary) 70%, transparent);
  }
  .chip button:hover {
    color: var(--color-primary);
  }

  .resultline .dot {
    flex: none;
    width: 3px;
    height: 3px;
    border-radius: 50%;
    background-color: currentColor;
  }

  .typefilter {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    margin-top: 0.5rem;
  }
  /* Pressed is the ON state, and the resting state is legible rather than
     greyed: both kinds are shown by default, so "off" is the exception the eye
     should catch. */
  .type-toggle {
    padding: 3px 10px;
    cursor: pointer;
    background: none;
    border: 1px solid var(--color-base-300);
    border-radius: 20px;
    font-size: 0.71875rem;
    font-weight: 550;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .type-toggle:hover:not(:disabled) {
    border-color: color-mix(in oklch, var(--color-base-content) 35%, transparent);
  }
  .type-toggle[aria-pressed="true"] {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
    border-color: color-mix(in oklch, var(--color-base-content) 25%, transparent);
    color: var(--color-base-content);
  }
  /* The last kind standing cannot be switched off; it stays legible because it
     is still reporting what the list is showing. */
  .type-toggle:disabled {
    cursor: default;
  }

  /* An incomplete list says so where the list is, not in the footer with the
     catalog's own errors: the two failures are independent and either one can
     happen without the other. */
  .list-note {
    margin: 0.75rem 1.25rem 0;
    padding: 0.5rem 0.75rem;
    font-size: 0.8125rem;
    line-height: 1.45;
    background-color: color-mix(in oklch, var(--color-warning) 20%, transparent);
    border-radius: var(--radius-field, 0.5rem);
    color: var(--color-base-content);
  }

  .group-head {
    position: sticky;
    top: 0;
    z-index: 1;
    padding: 1.875rem 1.25rem 0.5rem;
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
    padding-bottom: 96px;
  }

  .meeting-row {
    position: relative;
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    align-items: center;
    width: 100%;
    padding: 9px 20px;
    color: var(--color-base-content);
  }
  /* Only when picking is offered: without the checkbox the row keeps exactly
     the geometry it had before D-626. */
  .row-pickable {
    grid-template-columns: auto minmax(0, 1fr);
    gap: 0.5rem;
  }

  /* The open action fills the rest of the row, so a click anywhere but the
     checkbox still opens the meeting — the two never compete for the same
     pixel. */
  .row-open {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
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
    margin: -6px 0;
    cursor: pointer;
  }
  .row-pick input {
    width: 15px;
    height: 15px;
    margin: 0;
    cursor: pointer;
    accent-color: var(--color-primary);
  }
  .row-pick input:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }
  /* The open row is a solid primary fill under Nextcloud theming (app.css), so
     a primary checkbox would vanish into it. currentColor is whatever that
     row's text resolved to — primary-content there, base-content in the
     viewer's own themes, where the open row is only a tint. */
  .meeting-row[aria-current="page"] .row-pick input {
    accent-color: currentColor;
  }
  /* Inset to the row's padding so the rule separates rows rather than cutting
     the column edge to edge. */
  .meeting-row::after {
    content: "";
    position: absolute;
    left: 20px;
    right: 20px;
    bottom: 0;
    height: 1px;
    /* Decoration: it is painted over the open action's hit area, and a rule
       that swallowed a click would put a dead line across every row. */
    pointer-events: none;
    background-color: var(--color-base-300);
  }
  .meeting-row:hover {
    background-color: var(--color-base-200);
  }
  .meeting-row[aria-current="page"] {
    background-color: color-mix(
      in oklch,
      var(--color-primary) 15%,
      transparent
    );
    box-shadow: inset 2px 0 0 var(--color-primary);
  }

  .row-main {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .row-title {
    font-weight: 550;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row-meta {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    font-size: 0.75rem;
    font-variant-numeric: tabular-nums;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .row-room {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row-meta .dot {
    flex: none;
    width: 3px;
    height: 3px;
    border-radius: 50%;
    background-color: currentColor;
  }
  .row-duration {
    flex: none;
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
    padding: 3rem 1.25rem;
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
    color: var(--color-primary);
  }
  .list-empty-action:hover {
    background-color: color-mix(
      in oklch,
      var(--color-primary) 15%,
      transparent
    );
  }

  @media (max-width: 720px) {
    .rooms-button {
      display: flex;
    }
  }
</style>
