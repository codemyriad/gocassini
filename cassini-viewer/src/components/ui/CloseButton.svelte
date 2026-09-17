<script lang="ts">
  import { X } from "@lucide/svelte";

  // The X that closes a drawer or a sheet: one size everywhere, and a target a
  // finger can find. It is 36px, and gives 6px back above and below and 8px at
  // the side it sits on, so a header keeps the height and the edge it had.
  export let label: string;
  export let title: string | undefined = undefined;
</script>

<button type="button" class="close-button" aria-label={label} {title} {...$$restProps} on:click>
  <X size={20} aria-hidden="true" />
</button>

<style>
  .close-button {
    display: inline-flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    margin: -6px -8px -6px 0;
    padding: 0;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .close-button:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 10%, transparent);
    color: var(--color-base-content);
  }
  .close-button:focus-visible {
    outline: 2px solid var(--color-base-content);
    outline-offset: -2px;
  }
</style>
