<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { ChevronDown, ChevronUp, ListFilter, Search, X } from "@lucide/svelte";

  import Kbd from "../ui/Kbd.svelte";

  export let query = "";
  export let onlyMatching = false;
  export let stops = 0;
  export let current = -1;

  const dispatch = createEventDispatcher<{ step: 1 | -1 }>();
  const mac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);
  let field: HTMLInputElement;

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
    <label class="tt-field input min-w-0 flex-1 border-base-300 shadow-none">
      <Search size={16} class="shrink-0 opacity-50" aria-hidden="true" />
      <input
        bind:this={field}
        bind:value={query}
        type="search"
        autocomplete="off"
        spellcheck="false"
        placeholder="Find in transcript"
        aria-label="Find in transcript"
        aria-keyshortcuts="Control+F Meta+F"
        on:keydown={onKeydown}
      />
      <!-- Where the count and the steppers live: they belong to what was
           typed, so they sit at the end of the field holding it rather than
           as a cluster of their own beside it. -->
      {#if finding}
        <!-- One object at the end of the field: the count and the two steppers
             are about the search's results, not about the words typed. -->
        <button
          type="button"
          class="tt-filter"
          class:on={onlyMatching}
          aria-pressed={onlyMatching}
          aria-label="Show only matching turns"
          title={onlyMatching
            ? "Showing only the turns that mention your search. Click to show the whole transcript again."
            : "Hide every turn that doesn't mention your search, so you can read the matches together."}
          on:click={() => (onlyMatching = !onlyMatching)}
        >
          <ListFilter size={13} aria-hidden="true" />
        </button>
        <span class="tt-matches">
          <span class="tt-count tabular-nums" role="status">
            {stops > 0 ? `${current + 1} of ${stops}` : "No matches"}
          </span>
          <button type="button" class="tt-step" disabled={stops === 0} aria-label="Previous match" title="Previous match (Shift+Enter)" on:click={() => dispatch("step", -1)}>
            <ChevronUp size={13} aria-hidden="true" />
          </button>
          <button type="button" class="tt-step" disabled={stops === 0} aria-label="Next match" title="Next match (Enter)" on:click={() => dispatch("step", 1)}>
            <ChevronDown size={13} aria-hidden="true" />
          </button>
        </span>
        <button
          type="button"
          class="tt-clear"
          aria-label="Clear the search"
          title="Clear the search"
          on:click={() => ((query = ""), field?.focus())}
        >
          <X size={13} aria-hidden="true" />
        </button>
      {:else}
        <span class="tt-kbd"><Kbd size="sm"><span>{mac ? "⌘" : "Ctrl"}</span><span>F</span></Kbd></span>
      {/if}
    </label>
  </div>

</div>

<style>
  /* The field a reader types into, at the size of the fields elsewhere in the
     app rather than the size of the toolbar around it. */
  .tt-field {
    height: 2.25rem;
    padding-inline: 8px;
    border-radius: 8px;
    font-size: 0.875rem;
    background-color: var(--color-base-200);
    border-color: var(--color-base-300);
  }
  .tt-field:focus-within {
    outline: none;
    border-color: color-mix(in oklch, var(--color-base-content) 50%, var(--color-base-200));
    box-shadow: 0 0 0 3px color-mix(in oklch, var(--color-base-content) 12%, transparent);
  }
  /* The browser's own clear control for type="search" is a blue gradient disc
     that cannot be styled, as it is in the browse list's field. */
  .tt-field input::-webkit-search-cancel-button,
  .tt-field input::-webkit-search-decoration {
    -webkit-appearance: none;
    appearance: none;
  }
  /* The shortcut chip: readable at the field's size, and squared off like the
     code chips elsewhere rather than rounded like a pill. */
  /* Ours, because the browser's own clear control cannot be styled. */

  .tt-clear,
  .tt-filter {
    display: inline-flex;
    flex: none;
    align-items: center;
    justify-content: center;
    box-sizing: border-box;
    width: 22px;
    height: 22px;
    padding: 0;
    cursor: pointer;
    border: 1px solid transparent;
    border-radius: 5px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .tt-clear {
    margin-left: -3px;
    background-color: color-mix(in oklch, var(--color-base-content) 9%, transparent);
  }
  .tt-filter {
    margin-left: 4px;
    background-color: transparent;
    border-color: color-mix(in oklch, var(--color-base-content) 22%, transparent);
  }
  .tt-filter.on {
    background-color: color-mix(in oklch, var(--color-base-content) 9%, transparent);
    border-color: transparent;
    color: var(--color-base-content);
  }
  .tt-clear:hover,
  .tt-filter:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 15%, transparent);
    color: var(--color-base-content);
  }

  .tt-kbd {
    display: inline-flex;
    flex: none;
  }
  @media (hover: none) and (pointer: coarse) {
    .tt-kbd {
      display: none;
    }
  }

  /* The results pill, inside the field. */
  .tt-matches {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 5px;
    margin-left: -3px;
    padding: 2px;
    background-color: color-mix(in oklch, var(--color-base-content) 9%, transparent);
    /* The corners a tag chip has, like every other chip in the app. */
    border-radius: 5px;
  }
  .tt-count {
    flex: none;
    padding-left: 6px;
    font-size: 12px;
    white-space: nowrap;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .tt-step {
    display: inline-flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: 5px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .tt-step:hover:not(:disabled) {
    background-color: color-mix(in oklch, var(--color-base-content) 12%, transparent);
    color: var(--color-base-content);
  }
  .tt-step:disabled {
    cursor: default;
    opacity: 0.4;
  }


</style>
