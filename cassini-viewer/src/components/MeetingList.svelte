<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { Sun, Moon, Search, PanelLeft, X } from "@lucide/svelte";
  import {
    filterMeetingCatalogEntries,
    formatMeetingDuration,
    type MeetingCatalogEntry,
  } from "../viewer/catalog";
  import { groupMeetingsByMonth, roomLabelOf } from "../viewer/rooms";

  // The browse list (D-420 V1, room-grouped in D-654). Presentational: the
  // shell owns the catalog, the room selection and which meeting is open; this
  // owns only its text filter and how the rows are laid out.

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

  // The filter is list-local state — no other surface reads it.
  let filter = "";

  const dispatch = createEventDispatcher<{
    select: MeetingCatalogEntry;
    clearRoom: void;
    openRooms: void;
    toggleTheme: void;
  }>();

  // Title and date, as it has been since D-420 — the ticket's "keep the
  // existing date/name filter working inside a room". It deliberately does NOT
  // reach into transcript text: a hit the list cannot show is a hit that looks
  // like a bug.
  $: visibleMeetings = filterMeetingCatalogEntries(meetings, filter);
  $: monthGroups = groupMeetingsByMonth(visibleMeetings);
  $: trimmedFilter = filter.trim();
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
        <input
          type="search"
          placeholder="Search meetings by name or date"
          aria-label="Search meetings by name or date"
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

    <!-- Fixed height: a chip appearing must not push the list down under the
         pointer. -->
    <div class="resultline" role="status">
      <span>{visibleMeetings.length} of {totalCount} meetings</span>
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

  <div class="list-scroll flex-1 min-h-0 overflow-y-auto overscroll-contain scroll-stable">
    {#if totalCount === 0}
      <div class="list-empty">
        <strong>No meetings yet</strong>
        <span>Published recordings appear here.</span>
      </div>
    {:else if visibleMeetings.length === 0}
      <div class="list-empty">
        <strong>Nothing matches</strong>
        <span>
          {#if selectedRoomName !== null && trimmedFilter}
            No meeting in {selectedRoomName} matches that search.
          {:else if selectedRoomName !== null}
            {selectedRoomName} has no meetings.
          {:else}
            No meeting matches that search.
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
      {#each monthGroups as group (group.key)}
        <h3 class="group-head">{group.label}</h3>
        {#each group.meetings as meeting (meeting.id)}
          <button
            type="button"
            class="meeting-row"
            aria-current={meeting.id === selectedMeetingId ? "page" : undefined}
            on:click={() => dispatch("select", meeting)}
          >
            <span class="row-main">
              <span class="row-title">{meeting.title}</span>
              <span class="row-meta">
                <span>{meeting.dateLabel}</span>
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

  .meeting-row {
    position: relative;
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: 0.75rem;
    width: 100%;
    padding: 9px 20px;
    text-align: left;
    cursor: pointer;
    background: none;
    border: 0;
    color: var(--color-base-content);
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
