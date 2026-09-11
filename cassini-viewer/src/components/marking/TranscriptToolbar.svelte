<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { ChevronDown, ChevronUp, Search, Tag } from "@lucide/svelte";

  import type { VocabularyTag } from "../../viewer/annotations";
  import TagChip from "../tags/TagChip.svelte";
  import TagPicker from "../tags/TagPicker.svelte";
  import { pickColor, type TagPick } from "./session";

  export let query = "";
  export let onlyMatching = false;
  export let stops = 0;
  export let current = -1;
  export let tagging = false;
  export let armed: TagPick | null = null;
  export let vocabulary: readonly VocabularyTag[] = [];
  export let marksCount = 0;
  export let marksOpen = false;

  const dispatch = createEventDispatcher<{ step: 1 | -1 }>();
  const mac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);
  let field: HTMLInputElement;
  let armButton: HTMLButtonElement;
  let picking = false;

  $: finding = query.trim() !== "";

  export function focusFind() {
    field.focus();
    field.select();
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key === "Enter" && stops > 0) {
      event.preventDefault();
      dispatch("step", event.shiftKey ? -1 : 1);
    } else if (event.key === "Escape") {
      // Handled here, so the shell does not close the meeting on it.
      event.preventDefault();
      if (query) query = "";
      else field.blur();
    }
  }
</script>

<div class="flex flex-wrap items-center gap-x-2 gap-y-1.5" role="toolbar" aria-label="Transcript">
  <div class="flex min-w-0 flex-[1_1_15rem] items-center gap-1" role="search">
    <label class="input input-sm min-w-0 flex-1 border-base-300 shadow-none">
      <Search size={14} class="shrink-0 opacity-50" aria-hidden="true" />
      <input
        bind:this={field}
        bind:value={query}
        type="search"
        autocomplete="off"
        spellcheck="false"
        placeholder="Find in this meeting"
        aria-label="Find in this meeting"
        aria-keyshortcuts="Control+F Meta+F"
        on:keydown={onKeydown}
      />
      {#if !finding}<kbd class="kbd kbd-xs">{mac ? "⌘" : "Ctrl"} F</kbd>{/if}
    </label>
    {#if finding}
      <span class="min-w-[4.5em] text-center text-xs tabular-nums whitespace-nowrap text-base-content/70" role="status">
        {stops > 0 ? `${current + 1} of ${stops}` : "No matches"}
      </span>
      <button type="button" class="btn btn-ghost btn-xs btn-square" disabled={stops === 0} aria-label="Previous match" title="Previous match (Shift+Enter)" on:click={() => dispatch("step", -1)}>
        <ChevronUp size={14} aria-hidden="true" />
      </button>
      <button type="button" class="btn btn-ghost btn-xs btn-square" disabled={stops === 0} aria-label="Next match" title="Next match (Enter)" on:click={() => dispatch("step", 1)}>
        <ChevronDown size={14} aria-hidden="true" />
      </button>
    {/if}
  </div>
  {#if finding}
    <label class="flex cursor-pointer items-center gap-1.5 text-xs whitespace-nowrap text-base-content/70">
      <input type="checkbox" role="switch" class="toggle toggle-xs" bind:checked={onlyMatching} />
      Only matching turns
    </label>
  {/if}
  {#if tagging}
    <div class="ml-auto flex items-center gap-1.5">
      {#if armed}
        <div class="join" role="group" aria-label="Marking with a tag">
          <button bind:this={armButton} type="button" class="join-item btn btn-xs" title="Change the tag" aria-haspopup="dialog" aria-expanded={picking} on:click={() => (picking = !picking)}>
            <TagChip label={armed.label} color={pickColor(armed, vocabulary)} />
          </button>
          <button type="button" class="join-item btn btn-xs" on:click={() => (armed = null)}>Stop <kbd class="kbd kbd-xs">Esc</kbd></button>
        </div>
      {:else}
        <button bind:this={armButton} type="button" class="btn btn-xs" aria-haspopup="dialog" aria-expanded={picking} on:click={() => (picking = !picking)}>
          <Tag size={13} aria-hidden="true" />Mark with a tag…
        </button>
      {/if}
      <button type="button" class="btn btn-xs" aria-expanded={marksOpen} on:click={() => (marksOpen = !marksOpen)}>
        Marks <span class="font-mono tabular-nums opacity-60">{marksCount}</span>
      </button>
    </div>
    {#if picking}
      <TagPicker
        tags={vocabulary}
        label="Mark with a tag"
        anchor={armButton}
        on:pick={(event) => ((armed = event.detail), (picking = false))}
        on:close={() => (picking = false)}
      />
    {/if}
  {/if}
</div>
