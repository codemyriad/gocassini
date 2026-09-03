<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { X } from "@lucide/svelte";

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

  const dispatch = createEventDispatcher<{
    clear: void;
    prepare: void;
    dismissDropped: void;
  }>();
</script>

<!-- Positioned against the browse shell, NOT the viewport: in the embedded
     build this renders inside a Nextcloud page, and a fixed bar would float
     over Nextcloud's own chrome and the shell's Browse/Operator nav. The
     meeting sheet is anchored the same way and for the same reason. -->
<div class="selection-bar" role="region" aria-label="Selected meetings">
  {#if count > 0}
    <div class="selbar-said">
      <p class="selbar-count">
        {count}
        {count === 1 ? "meeting selected" : "meetings selected"}
      </p>
      <p class="selbar-desc">
        {#if hiddenCount > 0}
          {hiddenCount} not shown here.
        {/if}
        Assemble them into one document you can take away.
      </p>
    </div>

    <div class="selbar-actions">
      <!-- Quiet by design: Prepare is the action, and a second outlined button
           beside it would compete with it. -->
      <button type="button" class="selbar-clear" on:click={() => dispatch("clear")}>
        Clear selection
      </button>
      <button type="button" class="selbar-prepare" on:click={() => dispatch("prepare")}>
        Prepare
      </button>
    </div>
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
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem 1rem;
    padding: 0.75rem 1rem;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 1rem);
    /* Floats clear of the list rather than capping it: rows scroll visibly
       behind and around it, so it reads as a separate thing rather than as the
       bottom of the list. */
    box-shadow:
      0 1px 2px oklch(0% 0 0 / 0.06),
      0 8px 24px oklch(0% 0 0 / 0.14);
  }

  .selbar-said {
    flex: 1 1 12rem;
    min-width: 0;
  }

  .selbar-count {
    font-size: 0.875rem;
    font-weight: 650;
    color: var(--color-primary);
  }

  .selbar-desc {
    font-size: 0.75rem;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }

  .selbar-actions {
    display: flex;
    flex: none;
    align-items: center;
    gap: 0.5rem;
  }

  .selbar-clear {
    padding: 6px 10px;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.8125rem;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .selbar-clear:hover {
    color: var(--color-base-content);
    background-color: var(--color-base-200);
  }

  .selbar-prepare {
    padding: 7px 16px;
    cursor: pointer;
    background-color: var(--color-primary);
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--color-primary-content);
  }
  .selbar-prepare:hover {
    background-color: color-mix(in oklch, var(--color-primary) 88%, black);
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
