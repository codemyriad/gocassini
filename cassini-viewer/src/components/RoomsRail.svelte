<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { Lock, Settings } from "@lucide/svelte";
  import type { RoomBucket } from "../viewer/rooms";
  import { isLastBrowseType, type BrowseType, type BrowseTypeFilter } from "../viewer/insights";
  import { matchTags, type VocabularyTag } from "../viewer/annotations";
  import type { TagMatch } from "../viewer/listTags";
  import { colorFor } from "../viewer/tagPalette";
  import TagChip from "./tags/TagChip.svelte";

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

  export let tagsOffered = false;
  // Null until the vocabulary loads; the operator answers 503 while it first indexes.
  export let tags: readonly VocabularyTag[] | null = null;
  export let tagsFailed = false;
  export let selectedTagIds: readonly string[] = [];
  export let tagMatch: TagMatch = "any";

  const dispatch = createEventDispatcher<{
    select: string | null;
    close: void;
    toggleType: BrowseType;
    toggleTag: string;
    tagMatch: TagMatch;
    manageTags: void;
  }>();

  const TAG_MATCHES: TagMatch[] = ["any", "all"];
  $: tagRows = tags ? matchTags(tags, "") : null;

  export let audience: "" | "participants" = "";
  const AUDIENCE = {
    participants: {
      label: "Shared with participants",
      detail:
        "Cassini shares each recording with the room's captured participants, including invited people who did not join. Guests without a Nextcloud account do not receive a share. Public meeting participants can share onward when Nextcloud permits it.",
    },
  } as const;
  const AUDIENCE_FOOTNOTE = "Nextcloud Files controls who can read each recording.";
  let audienceEl: HTMLElement;
  let audienceOpen = false;

  function closeAudienceOutside(event: MouseEvent) {
    if (audienceOpen && !event.composedPath().includes(audienceEl)) audienceOpen = false;
  }

  function select(key: string | null) {
    dispatch("select", key);
    // Picking a room is the drawer's whole purpose, so it closes behind you.
    // A no-op on desktop, where the rail is not a drawer.
    dispatch("close");
  }
</script>

<svelte:window on:click={closeAudienceOutside} />

<nav aria-label="Rooms" class="rooms-rail" data-open={open}>
  <div class="rail-head rail-head-row rooms-head">
    <h2>Rooms</h2>
    {#if audience}
      <span class="audience" bind:this={audienceEl}>
        <button
          type="button"
          class="audience-note"
          aria-expanded={audienceOpen}
          aria-describedby="audience-detail"
          on:click={() => (audienceOpen = !audienceOpen)}
          on:keydown={(event) => {
            if (event.key === "Escape") audienceOpen = false;
          }}
        >
          <Lock size={11} aria-hidden="true" />
          {AUDIENCE[audience].label}
        </button>
        <span id="audience-detail" role="tooltip" class="tag-popover audience-detail" class:open={audienceOpen}>
          {AUDIENCE[audience].detail}
          <span class="audience-foot">{AUDIENCE_FOOTNOTE}</span>
        </span>
      </span>
    {/if}
  </div>

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

  {#if tagsOffered}
    <div class="rail-head rail-head-sub rail-head-row">
      <h2>Tags</h2>
      <button
        type="button"
        class="manage-tags"
        aria-label="Manage tags"
        title="Manage tags"
        on:click={() => dispatch("manageTags")}
      >
        <Settings size={14} aria-hidden="true" />
      </button>
    </div>
    {#if tagRows}
      <div class="rail-list" role="group" aria-label="Filter by tag">
        {#each tagRows as tag (tag.tagId)}
          <label class="type-row" data-tag-color={colorFor(tag)}>
            <input
              type="checkbox"
              class="cassini-check tag-box"
              checked={selectedTagIds.includes(tag.tagId)}
              on:change={() => dispatch("toggleTag", tag.tagId)}
            />
            <span class="rail-tag">
              <TagChip label={tag.label} color={colorFor(tag)} icon={tag.icon} />
            </span>
            <span class="room-count">{tag.meetings}</span>
          </label>
        {:else}
          <p class="rail-note">No tags yet.</p>
        {/each}
      </div>
      {#if selectedTagIds.length >= 2}
        <div class="tag-match">
          <span class="tag-match-label" aria-hidden="true">Match</span>
          <div class="segmented" role="group" aria-label="Show meetings with">
            {#each TAG_MATCHES as mode}
              <button
                type="button"
                aria-pressed={tagMatch === mode}
                aria-label={`${mode} of the ticked tags`}
                class="segment"
                on:click={() => dispatch("tagMatch", mode)}>{mode}</button
              >
            {/each}
          </div>
        </div>
      {/if}
    {:else if tagsFailed}
      <p class="rail-note">Tags are unavailable right now.</p>
    {/if}
  {/if}

  {#if insightsOffered}
    <h2 class="rail-head rail-head-sub">Show</h2>
    <div class="rail-list" role="group" aria-label="Show">
      <!-- The last one standing cannot be turned off: an empty list is not a
           filter state anyone wants to land in, and it is reached in one click
           from here. -->
      <label class="type-row">
        <input
          type="checkbox"
          class="cassini-check"
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
          class="cassini-check"
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
  /* Plain CSS rather than Tailwind utilities: aria-pressed drives two
     properties at once (fill and the inset marker), which reads
     better as one rule than as a stack of aria-[pressed=true]: variants, and
     the narrow-viewport drawer needs a media query either way. */
  .rooms-rail {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 16px 0 0.875rem;
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
    line-height: 20px;
  }
  .type-row:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 6%, transparent);
  }
  .type-row:has(input:disabled) {
    cursor: default;
    opacity: 0.75;
  }
  .type-row:has(input:disabled):hover {
    background: none;
  }

  .type-row input[data-type]:checked {
    background-color: var(--color-base-content);
    border-color: var(--color-base-content);
  }
  .type-row input[data-type]:checked::after {
    border-color: var(--color-base-100);
  }
  .type-row input.tag-box:checked {
    background-color: var(--tag);
    border-color: var(--tag);
  }
  .type-row input.tag-box:checked::after {
    border-color: var(--color-base-100);
  }
  .rail-tag {
    display: flex;
    flex: 1;
    min-width: 0;
  }
  .rail-tag :global(span.tag-chip) {
    flex: 0 1 auto;
  }
  .rail-tag :global(.tag-chip span.tag-dot) {
    translate: none;
  }

  .tag-match {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 2px 16px 4px;
  }
  .tag-match-label {
    font-size: 0.75rem;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .segmented {
    flex: 1;
    display: flex;
    gap: 3px;
    padding: 4px;
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
    border-radius: var(--radius-field, 0.5rem);
  }
  .segment {
    flex: 1;
    padding: 2px 8px;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: calc(var(--radius-field, 0.5rem) - 4px);
    font-size: 0.75rem;
    font-weight: 500;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    transition: background-color 0.12s ease, color 0.12s ease;
  }
  .segment:hover {
    color: var(--color-base-content);
  }
  .segment[aria-pressed="true"] {
    background-color: var(--color-base-200);
    color: var(--color-base-content);
    box-shadow:
      0 1px 2px oklch(0% 0 0 / 0.12),
      0 0 0 1px color-mix(in oklch, var(--color-base-content) 8%, transparent);
  }
  @media (prefers-reduced-motion: reduce) {
    .segment {
      transition: none;
    }
  }

  .rail-head-row.rooms-head {
    position: relative;
    gap: 6px;
  }
  .audience-note {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin: -3px 0;
    padding: 2px 6px;
    cursor: help;
    background-color: color-mix(in oklch, var(--color-base-content) 7%, transparent);
    border: 0;
    border-radius: 5px;
    font-size: 10.5px;
    font-weight: 500;
    line-height: 14px;
    letter-spacing: normal;
    text-transform: none;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .audience-note:hover,
  .audience-note[aria-expanded="true"] {
    background-color: color-mix(in oklch, var(--color-base-content) 12%, transparent);
    color: var(--color-base-content);
  }
  .audience-detail {
    display: none;
    position: absolute;
    top: calc(100% + 4px);
    left: 8px;
    right: 8px;
    padding: 8px 10px;
    font-size: 12px;
    font-weight: 400;
    line-height: 1.45;
    letter-spacing: normal;
    text-transform: none;
    text-wrap: pretty;
    color: var(--color-base-content);
  }
  .audience-foot {
    display: block;
    margin-top: 8px;
    padding-top: 6px;
    border-top: 1px solid var(--color-base-300);
    font-size: 11px;
    text-wrap: balance;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .audience:hover .audience-detail,
  .audience:focus-within .audience-detail,
  .audience-detail.open {
    display: block;
  }

  .rail-head-row {
    display: flex;
    align-items: center;
    gap: 2px;
  }
  .manage-tags {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    margin: -6px 0;
    padding: 0;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: var(--radius-selector, 0.25rem);
    color: inherit;
  }
  .manage-tags:hover {
    color: var(--color-base-content);
    background-color: color-mix(in oklch, var(--color-base-content) 6%, transparent);
  }
  .rail-note {
    padding: 4px 16px;
    font-size: 0.8125rem;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
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
    background-color: color-mix(in oklch, var(--color-base-content) 6%, transparent);
  }
  .room-button[aria-pressed="true"] {
    background-color: color-mix(in oklch, var(--color-primary) 35%, transparent);
    box-shadow: inset 3px 0 0 var(--color-primary);
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
