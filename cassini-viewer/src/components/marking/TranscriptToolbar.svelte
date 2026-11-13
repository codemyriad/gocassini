<script lang="ts">
  import { claimEscape } from "../ui/escapeClaim";
  import { createEventDispatcher } from "svelte";
  import { ChevronDown, ChevronUp, ListFilter, Search, X } from "@lucide/svelte";

  import { LL, numberFormat } from "../../i18n";
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
      claimEscape(event);
      if (query) query = "";
      else field.blur();
    }
  }
</script>

<div class="flex flex-wrap items-center gap-x-2 gap-y-1.5" role="toolbar" aria-label={$LL.viewer.meeting.find.toolbar_label()}>
  <div class="flex min-w-0 flex-[1_1_15rem] items-center gap-1" role="search">
    <label class="tt-field input min-w-0 flex-1 border-base-300 shadow-none">
      <Search size={16} class="shrink-0 opacity-50" aria-hidden="true" />
      <input
        bind:this={field}
        bind:value={query}
        type="search"
        autocomplete="off"
        spellcheck="false"
        placeholder={$LL.viewer.meeting.find.field()}
        aria-label={$LL.viewer.meeting.find.field()}
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
          aria-label={$LL.viewer.meeting.find.only_matching()}
          title={onlyMatching
            ? $LL.viewer.meeting.find.only_matching_on_title()
            : $LL.viewer.meeting.find.only_matching_off_title()}
          on:click={() => (onlyMatching = !onlyMatching)}
        >
          <ListFilter size={13} aria-hidden="true" />
        </button>
        <span class="tt-matches">
          <span class="tt-count tabular-nums" role="status">
            {stops > 0
              ? $LL.viewer.meeting.find.position({ current: numberFormat().format(current + 1), count: numberFormat().format(stops) })
              : $LL.viewer.meeting.find.no_matches()}
          </span>
          <button type="button" class="tt-step" disabled={stops === 0} aria-label={$LL.viewer.meeting.find.previous()} title={$LL.viewer.meeting.find.previous_title()} on:click={() => dispatch("step", -1)}>
            <ChevronUp size={13} aria-hidden="true" />
          </button>
          <button type="button" class="tt-step" disabled={stops === 0} aria-label={$LL.viewer.meeting.find.next()} title={$LL.viewer.meeting.find.next_title()} on:click={() => dispatch("step", 1)}>
            <ChevronDown size={13} aria-hidden="true" />
          </button>
        </span>
        <button
          type="button"
          class="tt-clear"
          aria-label={$LL.viewer.meeting.find.clear()}
          title={$LL.viewer.meeting.find.clear()}
          on:click={() => ((query = ""), field?.focus())}
        >
          <X size={13} aria-hidden="true" />
        </button>
      {:else}
        <span class="tt-kbd"><Kbd size="sm"><span>{mac ? "⌘" : $LL.viewer.meeting.keys.ctrl()}</span><span>F</span></Kbd></span>
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
