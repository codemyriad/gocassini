<script lang="ts">
  import { createEventDispatcher } from "svelte";

  import { formatPreciseTime } from "../../core/marking";
  import type { VocabularyTag } from "../../viewer/annotations";
  import TagChip from "../tags/TagChip.svelte";
  import TagPicker from "../tags/TagPicker.svelte";
  import { pickColor, type PlacedMark, type TagPick } from "./session";

  export let startMs: number;
  export let endMs: number;
  // The mark being moved, when the stretch is one already made.
  export let mark: PlacedMark | null = null;
  export let moved = false;
  export let armed: TagPick | null = null;
  export let vocabulary: readonly VocabularyTag[] = [];
  export let busy = false;

  const dispatch = createEventDispatcher<{ tag: TagPick; save: void; remove: void; clear: void }>();
  let root: HTMLElement;
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

<div bind:this={root} class="grid w-44 gap-1 rounded-box border border-base-300 bg-base-100 p-1.5 text-xs shadow-md" role="toolbar" aria-label="This stretch">
  <p class="px-1 font-mono tabular-nums text-base-content/70">
    <b class="text-base-content">{formatPreciseTime(startMs)}</b> → <b class="text-base-content">{formatPreciseTime(endMs)}</b>
  </p>
  {#if mark}
    <div class="flex min-w-0 items-center gap-1.5 px-1">
      <TagChip label={mark.tag.label} color={mark.color} icon={mark.icon} />
      <span class="truncate text-base-content/60">{mark.item.actor.id}</span>
    </div>
    {#if moved}
      <button type="button" class="btn btn-neutral btn-xs justify-between" disabled={busy} on:click={confirm}>Save move <kbd class="kbd kbd-xs">↵</kbd></button>
    {/if}
    <button type="button" class="btn btn-ghost btn-xs justify-start text-error" disabled={busy} on:click={() => dispatch("remove")}>Remove</button>
    <button type="button" class="btn btn-ghost btn-xs justify-between" on:click={() => dispatch("clear")}>
      {moved ? "Cancel" : "Done"} <kbd class="kbd kbd-xs">Esc</kbd>
    </button>
  {:else}
    <button
      type="button"
      class="btn btn-xs justify-between {armed ? 'armed' : 'btn-neutral'}"
      data-tag-color={armed ? pickColor(armed, vocabulary) : undefined}
      disabled={busy}
      aria-haspopup={armed ? undefined : "dialog"}
      aria-expanded={armed ? undefined : picking}
      on:click={confirm}
    >
      <span class="truncate">{armed ? `Tag as ${armed.label}` : "Tag this stretch"}</span><kbd class="kbd kbd-xs">↵</kbd>
    </button>
    <button type="button" class="btn btn-ghost btn-xs justify-between" on:click={() => dispatch("clear")}>Clear <kbd class="kbd kbd-xs">Esc</kbd></button>
  {/if}
</div>
{#if picking}
  <TagPicker
    tags={vocabulary}
    label="Tag this stretch"
    anchor={root}
    on:pick={(event) => ((picking = false), dispatch("tag", event.detail))}
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
