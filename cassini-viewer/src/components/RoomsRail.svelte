<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import type { RoomBucket } from "../viewer/rooms";
  import { isLastBrowseType, type BrowseType, type BrowseTypeFilter } from "../viewer/insights";

  // The rooms nav (D-654), and under it the two kinds the list holds.
  //
  // Presentational: the shell owns which room is selected, which kinds are
  // showing, and derives every count here; this renders them and reports
  // clicks.
  //
  // Show lives here rather than over the list because both are narrowings of
  // the same archive and belong in the same column — which is what the design
  // prototype does. It was in the list header while `types` was list-local
  // state; the shell owns it now, and the counts ride down from the list,
  // because they have to answer "is there anything behind that box?" under the
  // CURRENT search as well as the current room.
  //
  // On narrow viewports it IS the drawer — parked off-canvas and slid in on
  // `open` — rather than there being a second, phone-shaped room list to keep
  // in step with this one.
  export let rooms: RoomBucket[] = [];
  export let selectedRoomKey: string | null = null;
  export let totalCount = 0;
  export let open = false;

  // Whether this build has insights at all. False for a standalone export,
  // whose provider cannot list them — and then there is no Show section,
  // because a control that narrows to a kind of thing which cannot exist here
  // is a promise the build cannot keep.
  export let insightsOffered = false;
  export let types: BrowseTypeFilter = { meetings: true, insights: true };
  // What each kind would contribute under the current room and search.
  export let meetingCount = 0;
  export let insightCount = 0;

  const dispatch = createEventDispatcher<{
    select: string | null;
    close: void;
    toggleType: BrowseType;
  }>();

  function select(key: string | null) {
    dispatch("select", key);
    // Picking a room is the drawer's whole purpose, so it closes behind you.
    // A no-op on desktop, where the rail is not a drawer.
    dispatch("close");
  }
</script>

<nav aria-label="Rooms" class="rooms-rail" data-open={open}>
  <h2 class="rail-head">Rooms</h2>

  <div class="rail-list">
    <button
      type="button"
      on:click={() => select(null)}
      aria-pressed={selectedRoomKey === null}
      class="room-button"
    >
      <span class="room-name">All meetings</span>
      <span class="room-count">{totalCount}</span>
    </button>

    {#each rooms as room (room.key)}
      <button
        type="button"
        on:click={() => select(room.key)}
        aria-pressed={selectedRoomKey === room.key}
        class="room-button"
        class:room-none={!room.hasRoom}
      >
        <span class="room-name">{room.name}</span>
        <span class="room-count">{room.count}</span>
      </button>
    {/each}
  </div>

  {#if insightsOffered}
    <h2 class="rail-head rail-head-sub">Show</h2>
    <div class="rail-list" role="group" aria-label="Show">
      <!-- The last one standing cannot be turned off: an empty list is not a
           filter state anyone wants to land in, and it is reached in one click
           from here. -->
      <label class="type-row">
        <input
          type="checkbox"
          data-type="meetings"
          checked={types.meetings}
          disabled={isLastBrowseType(types, "meetings")}
          on:change={() => dispatch("toggleType", "meetings")}
        />
        <span class="room-name">Meetings</span>
        <span class="room-count">{meetingCount}</span>
      </label>
      <label class="type-row">
        <input
          type="checkbox"
          data-type="insights"
          checked={types.insights}
          disabled={isLastBrowseType(types, "insights")}
          on:change={() => dispatch("toggleType", "insights")}
        />
        <span class="room-name">Insights</span>
        <span class="room-count">{insightCount}</span>
      </label>
    </div>
  {/if}
</nav>

<style>
  /* Plain CSS rather than Tailwind utilities: aria-pressed drives four
     properties at once (fill, text, weight, the inset marker), which reads
     better as one rule than as a stack of aria-[pressed=true]: variants, and
     the narrow-viewport drawer needs a media query either way. */
  .rooms-rail {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.875rem 0;
    overflow-y: auto;
    overscroll-behavior: contain;
    background-color: var(--color-base-200);
    border-right: 1px solid var(--color-base-300);
  }

  .rail-head {
    flex: none;
    padding: 0 1rem 0.25rem;
    font-size: 10px;
    font-weight: 600;
    line-height: 1;
    letter-spacing: 0.09em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }

  /* A real gap, not a nudge: Rooms and Show are two different narrowings, and
     at 10px the Show heading read as another room. */
  .rail-head-sub {
    margin-top: 1.5rem;
  }

  .rail-list {
    display: flex;
    flex-direction: column;
  }

  /* Same geometry as a room row, so the two lists read as one column of
     narrowings rather than a nav with a form stuck under it. */
  .type-row {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 16px;
    cursor: pointer;
    color: var(--color-base-content);
    font-size: 0.875rem;
  }
  .type-row:hover {
    background-color: var(--color-base-300);
  }
  .type-row:has(input:disabled) {
    cursor: default;
    opacity: 0.75;
  }
  .type-row:has(input:disabled):hover {
    background: none;
  }

  .type-row input[type="checkbox"] {
    position: relative;
    flex: none;
    width: 16px;
    height: 16px;
    margin: 0;
    appearance: none;
    -webkit-appearance: none;
    cursor: pointer;
    background: transparent;
    border: 1px solid color-mix(in oklch, var(--color-base-content) 25%, transparent);
    border-radius: var(--radius-selector, 0.25rem);
  }
  /* Each box takes the colour of the thing it shows, so the filter reads
     against the list rather than against itself: base-content for a meeting
     row, secondary — this theme's amber, and the colour every insight surface
     uses — for an insight card. */
  .type-row input[data-type="meetings"]:checked {
    background-color: var(--color-base-content);
    border-color: var(--color-base-content);
  }
  .type-row input[data-type="insights"]:checked {
    background-color: var(--color-secondary);
    border-color: var(--color-secondary);
  }
  .type-row input[type="checkbox"]:checked::after {
    content: "";
    position: absolute;
    top: 1px;
    left: 4.5px;
    width: 3.5px;
    height: 8px;
    border-style: solid;
    border-width: 0 2px 2px 0;
    transform: rotate(45deg);
  }
  .type-row input[data-type="meetings"]:checked::after {
    border-color: var(--color-base-100);
  }
  .type-row input[data-type="insights"]:checked::after {
    border-color: var(--color-secondary-content);
  }
  .type-row input[type="checkbox"]:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }

  .room-button {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 16px;
    text-align: left;
    cursor: pointer;
    background: none;
    border: 0;
    color: var(--color-base-content);
    font-size: 0.875rem;
  }
  .room-button:hover {
    background-color: var(--color-base-300);
  }
  .room-button[aria-pressed="true"] {
    background-color: color-mix(
      in oklch,
      var(--color-primary) 15%,
      transparent
    );
    color: var(--color-primary);
    font-weight: 600;
    box-shadow: inset 2px 0 0 var(--color-primary);
  }

  /* "No room" is the absence of a room, not a room — italic so it does not read
     as a conversation someone could open. */
  .room-none .room-name {
    font-style: italic;
    opacity: 0.75;
  }
  .room-button[aria-pressed="true"].room-none .room-name {
    opacity: 1;
  }

  .room-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .room-count {
    flex: none;
    font-size: 0.6875rem;
    font-variant-numeric: tabular-nums;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .room-button[aria-pressed="true"] .room-count {
    color: var(--color-primary);
  }

  /* Narrow: the rail becomes the drawer. visibility (not just the transform)
     is what keeps a parked rail out of the tab order and the accessibility
     tree; it is delayed by the transition duration on the way out so the slide
     is still visible. */
  @media (max-width: 720px) {
    .rooms-rail {
      position: absolute;
      top: 0;
      bottom: 0;
      left: 0;
      z-index: 40;
      width: min(268px, 82vw);
      box-shadow: 6px 0 24px oklch(0% 0 0 / 0.18);
      transform: translateX(-100%);
      visibility: hidden;
      transition:
        transform 0.3s cubic-bezier(0.32, 0.72, 0, 1),
        visibility 0s linear 0.3s;
    }
    .rooms-rail[data-open="true"] {
      transform: none;
      visibility: visible;
      transition:
        transform 0.3s cubic-bezier(0.32, 0.72, 0, 1),
        visibility 0s;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .rooms-rail {
      transition: none;
    }
  }
</style>
