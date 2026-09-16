<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { ChevronDown, ChevronUp, Search, X } from "@lucide/svelte";


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
        placeholder="Find in this meeting"
        aria-label="Find in this meeting"
        aria-keyshortcuts="Control+F Meta+F"
        on:keydown={onKeydown}
      />
      <!-- Where the count and the steppers live: they belong to what was
           typed, so they sit at the end of the field holding it rather than
           as a cluster of their own beside it. -->
      {#if finding}
        <!-- One object at the end of the field: the count and the two steppers
             are about the search's results, not about the words typed. -->
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
          <X size={14} aria-hidden="true" />
        </button>
      {:else}
        <kbd class="tt-kbd kbd kbd-sm">{mac ? "⌘" : "Ctrl"} F</kbd>
      {/if}
    </label>
  </div>
  <!-- Beside the field, as a switch with its sentence: a second view of the
       same search is a setting rather than an action, and as a plain chip it
       read as a button that would do something. -->
  {#if finding}
    <!-- The player's Auto-scroll control, in miniature: a tinted, outlined box
         holding its label and its switch, so the two toggles in this sheet are
         the same kind of thing. -->
    <label class="tt-only" class:on={onlyMatching}>
      <input
        type="checkbox"
        role="switch"
        class="toggle toggle-sm"
        bind:checked={onlyMatching}
      />
      <span title="Show only the turns your search matched">Matches only</span>
    </label>
  {/if}

</div>

<style>
  /* The field a reader types into, at the size of the fields elsewhere in the
     app rather than the size of the toolbar around it. */
  .tt-field {
    height: 2.25rem;
    font-size: 0.875rem;
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
  .tt-clear {
    display: inline-flex;
    flex: none;
    margin-left: 4px;
    padding: 3px;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: 5px;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .tt-clear:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 10%, transparent);
    color: var(--color-base-content);
  }

  /* The results pill, inside the field. */
  .tt-matches {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 5px;
    margin-left: 4px;
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

  .tt-only {
    flex: none;
    display: flex;
    align-items: center;
    gap: 6px;
    height: 2.25rem;
    padding: 0 8px;
    cursor: pointer;
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    font-size: 12.5px;
    white-space: nowrap;
    color: var(--color-base-content);
  }
  /* Off is grey, on is a stronger neutral, like the player's own toggle. */
  .tt-only.on {
    background-color: color-mix(in oklch, var(--color-base-content) 16%, transparent);
    border-color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }

  .tt-kbd {
    padding-inline: 6px;
    border-radius: 5px;
    font-size: 12px;
    line-height: 1.4;
  }
</style>
