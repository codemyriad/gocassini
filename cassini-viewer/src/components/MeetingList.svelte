<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { fly } from "svelte/transition";
  import { Sun, Moon, Calendar, Clock, Users, Search } from "@lucide/svelte";
  import { formatClockTime } from "../core/transcript";
  import {
    filterMeetingCatalogEntries,
    formatMeetingDateShort,
    type MeetingCatalogEntry,
  } from "../viewer/catalog";

  // Presentational left pane (D-420 V1). The shell owns which meeting is
  // selected, the catalog data, and theme state; the list owns only its filter.
  export let meetings: MeetingCatalogEntry[] = [];
  export let selectedMeetingId = "";
  export let ncMode = false;
  export let themeMode: "saturn-light" | "saturn-dark" = "saturn-light";
  export let errorMessage = "";
  // fly params are computed by the shell (they depend on reduced-motion /
  // viewport state the shell owns) and passed through so the region transition
  // is preserved without duplicating that state here.
  export let flyParams: Parameters<typeof fly>[1] = { duration: 0 };

  // The filter is list-local state — no other surface reads it.
  let filter = "";

  const dispatch = createEventDispatcher<{
    select: MeetingCatalogEntry;
    toggleTheme: void;
  }>();

  $: visibleMeetings = filterMeetingCatalogEntries(meetings, filter);
</script>

<section
  aria-label="Meeting list"
  class="meeting-list flex flex-col row-start-1 col-start-1 h-full min-h-0 min-w-0 min-[981px]:sticky min-[981px]:top-0 bg-base-100 backdrop-blur-xl border-r border-base-300"
  transition:fly={flyParams}
>
  <!-- Scroll container holds the sticky header so cards visibly scroll
       behind its blur. Scrollbar runs the full sidebar height as a
       consequence (the only way to get the scroll-behind effect). -->
  <div class="flex-1 min-h-0 overflow-y-auto overscroll-contain scroll-stable">
    <header class="sticky top-0 z-20 flex flex-col gap-2.5 px-4 py-3 border-b bg-base-100 border-base-300 min-[981px]:border-none min-[981px]:bg-base-100/50 backdrop-blur-lg">
      <div class="flex items-center justify-between gap-3">
        <h2 class="text-base font-bold text-base-content">
          Meetings
        </h2>
        {#if !ncMode}
        <label class="flex items-center gap-1.5 cursor-pointer">
          <Sun size={16} strokeWidth={2} class="text-base-content/80" aria-hidden="true" />
          <input
            type="checkbox"
            class="toggle toggle-sm rounded-lg text-base-content/80"
            aria-label="Toggle light or dark theme"
            checked={themeMode === "saturn-dark"}
            on:change={() => dispatch("toggleTheme")}
          />
          <Moon size={16} strokeWidth={2} class="text-base-content/80" aria-hidden="true" />
        </label>
        {/if}
      </div>
      {#if meetings.length > 1}
        <label class="input input-sm flex items-center gap-2 w-full">
          <Search size={14} class="text-base-content/60" aria-hidden="true" />
          <input
            type="search"
            class="grow"
            placeholder="Filter by name or date"
            aria-label="Filter meetings by name or date"
            bind:value={filter}
          />
        </label>
      {/if}
    </header>
    {#if meetings.length > 0}
      <div class="grid gap-3 p-4">
        {#if visibleMeetings.length === 0}
          <p role="status" class="text-sm text-base-content/70 leading-normal">
            No meetings match "{filter.trim()}".
          </p>
        {/if}
        {#each visibleMeetings as meeting (meeting.id)}
          <button
            on:click={() => dispatch("select", meeting)}
            type="button"
            aria-current={meeting.id === selectedMeetingId ? "page" : undefined}
            class="
              grid gap-1 w-full p-3 text-left rounded-lg border cursor-pointer transition-all duration-100
              border-base-300 bg-base-100/50
              hover:bg-base-200
              aria-[current=page]:border-primary
              aria-[current=page]:bg-primary/15
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
