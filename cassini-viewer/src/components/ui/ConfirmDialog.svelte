<script lang="ts">
  // A small modal for an action that cannot be taken back. The shell renders
  // it as its topmost layer (everything under it goes inert), so this only
  // keeps focus in and answers Escape; see focusLayer.ts.
  import { createEventDispatcher } from "svelte";

  import { focusLayer } from "./focusLayer";

  export let title: string;
  export let body = "";
  export let confirmLabel = "Delete";
  export let cancelLabel = "Cancel";
  export let busy = false;

  const dispatch = createEventDispatcher<{ confirm: void; cancel: void }>();

  function onKeydown(event: KeyboardEvent) {
    if (event.key === "Escape") {
      event.stopPropagation();
      dispatch("cancel");
    }
  }
</script>

<button
  type="button"
  class="confirm-scrim"
  tabindex="-1"
  aria-label={cancelLabel}
  on:click={() => dispatch("cancel")}
></button>
<!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
<div
  class="confirm-dialog"
  role="alertdialog"
  aria-modal="true"
  aria-label={title}
  tabindex="-1"
  use:focusLayer
  on:keydown={onKeydown}
>
  <h2 class="text-base font-bold">{title}</h2>
  {#if body}
    <p class="text-sm text-base-content/70">{body}</p>
  {/if}
  <div class="confirm-actions">
    <!-- Cancel carries data-close so focusLayer lands on the safe answer. -->
    <button type="button" class="btn btn-sm" data-close disabled={busy} on:click={() => dispatch("cancel")}>
      {cancelLabel}
    </button>
    <button type="button" class="btn btn-sm btn-error" disabled={busy} on:click={() => dispatch("confirm")}>
      {confirmLabel}
    </button>
  </div>
</div>

<style>
  .confirm-scrim {
    position: absolute;
    inset: 0;
    /* Above the shell's sheets, including Manage tags (41/42). */
    z-index: 50;
    display: block;
    padding: 0;
    border: 0;
    cursor: pointer;
    background-color: oklch(0% 0 0 / 0.55);
  }
  .confirm-dialog {
    position: absolute;
    z-index: 51;
    left: 50%;
    top: 40%;
    transform: translate(-50%, -50%);
    display: grid;
    gap: 10px;
    width: min(26rem, calc(100% - 32px));
    padding: 16px;
    border-radius: var(--radius-box, 0.5rem);
    background-color: var(--color-base-100);
    color: var(--color-base-content);
    border: 1px solid var(--color-base-300);
    box-shadow: 0 18px 48px oklch(0% 0 0 / 0.3);
  }
  .confirm-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
