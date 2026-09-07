<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import type { RoomBucket } from "../viewer/rooms";

  // The rooms nav (D-654). Presentational: the shell owns which room is
  // selected and derives the buckets; this only renders them and reports
  // clicks.
  //
  // On narrow viewports it IS the drawer — parked off-canvas and slid in on
  // `open` — rather than there being a second, phone-shaped room list to keep
  // in step with this one.
  export let rooms: RoomBucket[] = [];
  export let selectedRoomKey: string | null = null;
  export let totalCount = 0;
  export let open = false;

  const dispatch = createEventDispatcher<{
    select: string | null;
    close: void;
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

  .rail-list {
    display: flex;
    flex-direction: column;
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
