<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { cubicOut } from "svelte/easing";
  import { ChevronDown, FileText, Tag, TriangleAlert, X } from "@lucide/svelte";
  import { plural, type TagPick, type VocabularyTag } from "../viewer/annotations";
  import { formatMeetingDateWithDay, type MeetingCatalogEntry } from "../viewer/catalog";
  import type { MeetingTags } from "../viewer/listTags";
  import { colorFor } from "../viewer/tagPalette";
  import TagChip from "./tags/TagChip.svelte";
  import TagPicker from "./tags/TagPicker.svelte";
  import { MAX_SELECTED_MEETINGS } from "../viewer/selectionModel";

  // The selection bar (D-626). Presentational: the shell owns the selection and
  // decides whether this exists at all.
  //
  // It carries only what a row cannot: how many are picked, how many of them
  // this narrowing is not showing, anything that has left the archive under the
  // selection, and the way forward.
  //
  // Two states, because a loss can take the last pick with it. With picks, it
  // is the count, the actions, and any notice under them. With none, it is the
  // notice alone — a count and a Prepare button over an empty set would describe
  // a bundle nobody can ask for, but the loss that emptied it is the whole
  // reason this is still on screen.

  export let count = 0;
  // Picked meetings the current room/search does not show. A selection spans
  // rooms and survives every narrowing, so a bar that reported only the total
  // would read as a claim about the visible list.
  export let hiddenCount = 0;
  // Picked meetings that left the catalog while they were picked. Reported
  // rather than silently dropped: the bundle would otherwise change under the
  // user between picking and preparing.
  export let droppedCount = 0;
  // Null offers no Tag action: the build cannot tag, or the vocabulary has not loaded.
  export let tags: readonly VocabularyTag[] | null = null;
  export let tagSelected: readonly string[] = [];
  export let tagMixed: readonly string[] = [];
  export let tagReport = "";
  export let tagBusy = false;
  export let tagRetry = false;
  // The picked meetings themselves, in pick order. The count alone leaves a
  // reader who has since changed room or search with no way to see WHICH
  // meetings a bundle would carry.
  export let entries: readonly MeetingCatalogEntry[] = [];
  // Of those, the ones the current narrowing is not showing.
  export let hiddenIds: ReadonlySet<string> = new Set();
  // Each picked meeting's tags, for the list behind the count. Whole-meeting
  // tags only: a tag on one stretch of a transcript says nothing about the
  // meeting the bundle carries, and this is where a bulk tag is seen to land.
  export let meetingTags: MeetingTags = new Map();

  const MAX_ROW_TAGS = 2;
  const wholeTags = (id: string) =>
    (meetingTags.get(id) ?? []).filter((entry) => entry.whole).map((entry) => entry.tag);

  // Above the operator's cap Prepare would open a panel whose every action is
  // refused, so the button says the number instead (D-749).
  $: overCap = count > MAX_SELECTED_MEETINGS;
  $: excess = count - MAX_SELECTED_MEETINGS;

  const dispatch = createEventDispatcher<{
    clear: void;
    prepare: void;
    dismissDropped: void;
    tag: TagPick;
    unpick: MeetingCatalogEntry;
    open: MeetingCatalogEntry;
    retryTag: void;
  }>();

  // Whether the list of picks is open. Closed by default: the bar floats over
  // the list it describes, and a panel nobody asked for would cover it.
  let listing = false;

  // Rises out of the bar it belongs to, rather than appearing over the list.
  // Reduced motion gets the same panel with no travel.
  function riseUp(_node: Element, { duration = 180 }: { duration?: number }) {
    const still =
      typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (still) {
      return { duration: 0 };
    }
    return {
      duration,
      easing: cubicOut,
      css: (t: number, u: number) => `opacity: ${t}; transform: translateY(${u * 8}px) scale(${0.985 + t * 0.015})`,
    };
  }
  $: if (count === 0) {
    listing = false;
  }

  let tagButton: HTMLButtonElement;
  let tagging = false;
</script>

<!-- Positioned against the browse shell, NOT the viewport: in the embedded
     build this renders inside a Nextcloud page, and a fixed bar would float
     over Nextcloud's own chrome and the shell's Browse/Operator nav. The
     meeting sheet is anchored the same way and for the same reason. -->
<div class="selection-bar" role="region" aria-label="Selected meetings">
  {#if count > 0}
    <div class="selbar-said">
      <div class="selbar-head">
        <button
          type="button"
          class="selbar-count"
          aria-expanded={listing}
          aria-label={listing ? "Hide the selected meetings" : "Show the selected meetings"}
          on:click={() => (listing = !listing)}
        >
          {count}
          {count === 1 ? "meeting selected" : "meetings selected"}
          <ChevronDown size={13} class="selbar-count-chev" aria-hidden="true" />
        </button>
        <button type="button" class="selbar-clear" on:click={() => dispatch("clear")}>
          Clear selection
          <X size={12} aria-hidden="true" />
        </button>
      </div>
      <p class="selbar-desc">
        {#if hiddenCount > 0}
          {hiddenCount} not shown here.
        {/if}
        Tag, download or get insights from them.
      </p>
      <!-- Not a note among the others: over the cap, Prepare is refused, so it
           is the one thing in this bar standing between a selection and what it
           is for. -->
      {#if overCap}
        <p class="selbar-over" role="status">
          <TriangleAlert size={14} aria-hidden="true" />
          <span>
            You can work with up to {MAX_SELECTED_MEETINGS} meetings at once. Unpick {excess === 1
              ? "one"
              : excess}.
          </span>
        </p>
      {/if}
      {#if tagReport}
        <p class="selbar-desc" role="status">{tagReport}</p>
        {#if tagRetry}<button type="button" class="link text-xs" on:click={() => dispatch("retryTag")}>Retry tag update</button>{/if}
      {/if}
    </div>

    <div class="selbar-actions">
      {#if tags}
        <button
          bind:this={tagButton}
          type="button"
          class="selbar-tag"
          aria-haspopup="dialog"
          disabled={tagBusy}
          aria-expanded={tagging}
          on:click={() => (tagging = !tagging)}
        >
          <Tag size={14} aria-hidden="true" />
          Tag
        </button>
      {/if}
      <button
        type="button"
        class="selbar-prepare"
        disabled={overCap}
        on:click={() => dispatch("prepare")}
      >
        <FileText size={14} aria-hidden="true" />
        Prepare
      </button>
    </div>
    {#if listing}
      <!-- Above the bar, not over the actions: this answers "which ones", and
           the row it belongs to is the count it opens from. -->
      <div class="selbar-list" role="group" aria-label="Selected meetings" transition:riseUp={{}}>
        <ul>
          {#each entries as entry (entry.id)}
            <li class="selbar-item">
              <button type="button" class="selbar-item-open" on:click={() => dispatch("open", entry)}>
                <span class="selbar-item-title">{entry.title}</span>
                <span class="selbar-item-meta">
                  {formatMeetingDateWithDay(entry.dateLabel)}
                  {#each wholeTags(entry.id).slice(0, MAX_ROW_TAGS) as tag (tag.tagId)}
                    <TagChip label={tag.label} color={colorFor(tag)} icon={tag.icon} />
                  {/each}
                  {#if wholeTags(entry.id).length > MAX_ROW_TAGS}
                    <span class="selbar-item-more">+{wholeTags(entry.id).length - MAX_ROW_TAGS}</span>
                  {/if}
                  {#if hiddenIds.has(entry.id)}
                    <span class="selbar-item-hidden">Not shown here</span>
                  {/if}
                </span>
              </button>
              <button
                type="button"
                class="selbar-item-drop"
                aria-label={`Unpick ${entry.title}`}
                on:click={() => dispatch("unpick", entry)}
              >
                <X size={13} aria-hidden="true" />
              </button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if tags && tagging}
      <TagPicker
        {tags}
        label={`Tag ${plural(count, "meeting")}`}
        multiple
        selected={tagSelected}
        mixed={tagMixed}
        anchor={tagButton}
        on:pick={(event) => { tagging = false; dispatch("tag", event.detail); }}
        on:close={() => (tagging = false)}
      />
    {/if}
  {/if}

  {#if droppedCount > 0}
    <p class="selbar-dropped" role="status">
      <span>
        {droppedCount === 1
          ? "1 selected meeting is no longer in the archive and was removed."
          : `${droppedCount} selected meetings are no longer in the archive and were removed.`}
      </span>
      <button
        type="button"
        on:click={() => dispatch("dismissDropped")}
        aria-label="Dismiss the removal notice"
      >
        <X size={12} aria-hidden="true" />
      </button>
    </p>
  {/if}
</div>

<style>
  /* Plain CSS, like the list and the rail: this is one small composed surface
     with a floating geometry and a stacked shadow, which reads better as a
     handful of rules than as utility stacks across six elements. */
  .selection-bar {
    position: relative;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem 1rem;
    padding: 0.75rem 1rem;
    background-color: var(--color-base-200);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-xl, 0.75rem);
    /* Floats clear of the list rather than capping it: rows scroll visibly
       behind and around it, so it reads as a separate thing rather than as the
       bottom of the list. */
    box-shadow:
      0 1px 3px oklch(0% 0 0 / 0.18),
      0 6px 16px oklch(0% 0 0 / 0.24),
      0 18px 42px oklch(0% 0 0 / 0.34),
      0 32px 72px oklch(0% 0 0 / 0.28);
  }

  .selbar-said {
    flex: 1 1 12rem;
    min-width: 0;
  }

  .selbar-count {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 0;
    cursor: pointer;
    background: none;
    border: 0;
    font-size: 0.875rem;
    font-weight: 650;
    color: var(--color-primary);
  }
  .selbar-count :global(.selbar-count-chev) {
    transition: transform 120ms ease;
  }
  .selbar-count[aria-expanded="true"] :global(.selbar-count-chev) {
    transform: rotate(180deg);
  }

  .selbar-list {
    position: absolute;
    left: 0;
    right: 0;
    bottom: calc(100% + 8px);
    max-height: min(320px, 50vh);
    overflow-y: auto;
    overscroll-behavior: contain;
    padding: 6px;
    background-color: var(--color-base-200);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-xl, 0.75rem);
    box-shadow:
      0 1px 3px oklch(0% 0 0 / 0.18),
      0 12px 32px oklch(0% 0 0 / 0.28);
  }
  .selbar-item {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .selbar-item-open {
    display: flex;
    flex: 1;
    flex-wrap: wrap;
    align-items: center;
    min-width: 0;
    gap: 2px 10px;
    padding: 6px 8px;
    text-align: left;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    color: var(--color-base-content);
  }
  .selbar-item-open:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
  }
  .selbar-item-title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.8125rem;
    font-weight: 550;
  }
  .selbar-item-meta {
    display: flex;
    flex: none;
    align-items: center;
    gap: 8px;
    white-space: nowrap;
    font-size: 0.6875rem;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .selbar-item-more {
    font-variant-numeric: tabular-nums;
  }
  .selbar-item-hidden {
    padding: 0 5px;
    border-radius: 999px;
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
  }
  .selbar-item-drop {
    display: inline-flex;
    flex: none;
    padding: 6px;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .selbar-item-drop:hover {
    color: var(--color-base-content);
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
  }

  .selbar-desc {
    font-size: 0.75rem;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }

  .selbar-over {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 6px;
    padding: 6px 10px;
    background-color: color-mix(in oklch, var(--color-error) 12%, transparent);
    border: 1px solid color-mix(in oklch, var(--color-error) 45%, transparent);
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.75rem;
    font-weight: 600;
    line-height: 1.4;
    color: var(--color-base-content);
  }
  .selbar-over :global(svg) {
    flex: none;
    color: var(--color-error);
  }

  .selbar-actions {
    display: flex;
    flex: none;
    align-items: center;
    gap: 0.5rem;
  }

  .selbar-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 10px;
    margin-bottom: 2px;
  }

  .selbar-clear {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    box-sizing: border-box;
    height: 22px;
    padding: 0 7px 0 8px;
    cursor: pointer;
    background-color: transparent;
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.75rem;
    font-weight: 600;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .selbar-clear:hover {
    color: var(--color-base-content);
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
  }

  .selbar-tag {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    padding: 6px 12px;
    cursor: pointer;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--color-base-content);
  }
  .selbar-tag:hover,
  .selbar-tag[aria-expanded="true"] {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, var(--color-base-100));
  }
  .selbar-tag:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }

  .selbar-prepare {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    padding: 6px 12px;
    cursor: pointer;
    background-color: var(--color-primary);
    border: 1px solid transparent;
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--color-primary-content);
  }
  .selbar-prepare:not(:disabled):hover {
    background-color: color-mix(in oklch, var(--color-primary) 88%, black);
  }
  .selbar-prepare:disabled {
    cursor: not-allowed;
    background-color: var(--color-base-300);
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }

  /* Full width beneath both: losing a meeting out of a selection is a change to
     what Prepare will produce, so it is not squeezed in beside the count — and
     it is the whole bar when the loss took the last pick with it. */
  .selbar-dropped {
    display: flex;
    flex: 1 0 100%;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.75rem;
    color: var(--color-warning, #b45309);
  }
  .selbar-dropped button {
    display: inline-flex;
    flex: none;
    padding: 0;
    background: none;
    border: 0;
    cursor: pointer;
    color: inherit;
  }

  @media (max-width: 560px) {
    /* Thumb-sized targets: the two actions split the row evenly. */
    .selbar-actions {
      flex: 1 0 100%;
    }
    .selbar-actions button {
      flex: 1;
    }
  }
</style>
