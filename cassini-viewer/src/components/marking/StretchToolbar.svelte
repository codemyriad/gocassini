<script lang="ts">
  import { createEventDispatcher } from "svelte";

  import { formatPreciseTime } from "../../core/marking";
  import type { TagPick, VocabularyTag } from "../../viewer/annotations";
  import TagChip from "../tags/TagChip.svelte";
  import TagPicker from "../tags/TagPicker.svelte";
  import { pickColor, type PlacedMark } from "./session";

  export let startMs: number;
  export let endMs: number;
  // The mark being moved, when the stretch is one already made.
  export let mark: PlacedMark | null = null;
  export let moved = false;
  export let armed: TagPick | null = null;
  export let vocabulary: readonly VocabularyTag[] = [];
  export let busy = false;
  // Why the last write from here failed.
  export let error = "";

  const dispatch = createEventDispatcher<{ tag: TagPick; arm: TagPick; save: void; remove: void; clear: void }>();
  // Whether the tag about to be used stays ready for the next stretch.
  let keep = false;
  let tagButton: HTMLButtonElement;
  let picking = false;

  // Enter: nothing is ever saved on release, only here.
  export function confirm() {
    if (busy) {
      return;
    }
    if (mark) {
      if (moved) dispatch("save");
    } else if (armed) {
      dispatch("tag", armed);
    } else {
      picking = true;
    }
  }
</script>

<!-- The tag keeps the place it had as a chip, and the card is built around it:
     the same chip in the same spot, then who put it there, the times, and the
     two actions on one line. -->
<div class="grid w-44 gap-1 rounded-box border border-base-300 bg-base-100 p-1.5 text-xs shadow-md" role="toolbar" aria-label="This section">
  {#if mark}
    <!-- No tag chip here: the section's own chip sits directly above this card
         and never moves, so repeating it named the same thing twice. -->
    <p class="truncate px-0.5 text-base-content/60">{mark.item.actor.id}</p>
    <p class="px-0.5 font-mono tabular-nums text-base-content/70">
      <b class="text-base-content">{formatPreciseTime(startMs)}</b> → <b class="text-base-content">{formatPreciseTime(endMs)}</b>
    </p>
    {#if moved}
      <button type="button" class="btn btn-neutral btn-xs justify-between" disabled={busy} on:click={confirm}>Save move <kbd class="kbd kbd-xs">↵</kbd></button>
    {/if}
    <div class="mt-0.5 flex items-center gap-1.5">
      <button type="button" class="btn btn-error btn-xs flex-1" disabled={busy} on:click={() => dispatch("remove")}>Remove</button>
      <button type="button" class="btn btn-neutral btn-xs flex-1 gap-1" on:click={() => dispatch("clear")}>
        {moved ? "Cancel" : "Done"} <kbd class="kbd kbd-xs">Esc</kbd>
      </button>
    </div>
  {:else}
    <p class="px-1 font-mono tabular-nums text-base-content/70">
      <b class="text-base-content">{formatPreciseTime(startMs)}</b> → <b class="text-base-content">{formatPreciseTime(endMs)}</b>
    </p>
    <button
      bind:this={tagButton}
      type="button"
      class="btn btn-xs justify-between {armed ? 'armed' : 'btn-neutral'}"
      data-tag-color={armed ? pickColor(armed, vocabulary) : undefined}
      disabled={busy}
      aria-haspopup={armed ? undefined : "dialog"}
      aria-expanded={armed ? undefined : picking}
      on:click={confirm}
    >
      <span class="truncate">{armed ? `Tag as ${armed.label}` : "Tag this section"}</span><kbd class="kbd kbd-xs">↵</kbd>
    </button>
    <!-- Ticked before tagging, it keeps this tag ready for the next stretch —
         the repeat pass, offered where somebody is already tagging rather than
         as a mode to set beforehand. -->
    <label class="flex cursor-pointer items-center gap-1.5 px-1 py-0.5 text-base-content/70">
      <input type="checkbox" class="checkbox checkbox-xs" bind:checked={keep} />
      <span>Keep this tag ready</span>
    </label>
    <button type="button" class="btn btn-ghost btn-xs justify-between" on:click={() => dispatch("clear")}>Clear <kbd class="kbd kbd-xs">Esc</kbd></button>
  {/if}
  {#if error}
    <p class="px-1 text-error" role="alert">{error}</p>
  {/if}
</div>
{#if picking}
  <TagPicker
    tags={vocabulary}
    label="Tag this section"
    anchor={tagButton}
    on:pick={(event) => ((picking = false), keep && dispatch("arm", event.detail), dispatch("tag", event.detail))}
    on:close={() => (picking = false)}
  />
{/if}

<style>
  .armed {
    color: var(--color-base-100);
    background: var(--tag);
    border-color: var(--tag);
  }
</style>
