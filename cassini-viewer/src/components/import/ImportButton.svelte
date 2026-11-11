<script lang="ts">
  // The Import button beside search. Its label is the import status while the
  // sheet is closed: running, just finished, or failed (see ImportPanel).
  import { Check, CircleAlert, CloudUpload, LoaderCircle } from "@lucide/svelte";
  import { LL } from "../../i18n";
  import type { ImportStatus } from "./importStatus";

  export let status: ImportStatus = { kind: "idle", count: 0 };
  export let expanded = false;

  $: text = $LL.viewer.browse.import;
  $: label =
    status.kind === "running"
      ? text.button_running({ count: status.count })
      : status.kind === "done"
        ? text.button_done({ count: status.count })
        : status.kind === "failed"
          ? text.button_failed({ count: status.count })
          : text.button();
</script>

<button
  type="button"
  class="import-button"
  class:import-button-running={status.kind === "running"}
  class:import-button-done={status.kind === "done"}
  class:import-button-failed={status.kind === "failed"}
  aria-expanded={expanded}
  title={label}
  on:click
>
  {#if status.kind === "running"}
    <LoaderCircle size={15} class="import-spin" aria-hidden="true" />
  {:else if status.kind === "done"}
    <Check size={15} aria-hidden="true" />
  {:else if status.kind === "failed"}
    <CircleAlert size={15} aria-hidden="true" />
  {:else}
    <CloudUpload size={15} aria-hidden="true" />
  {/if}
  <span aria-live="polite">{label}</span>
</button>

<style>
  .import-button {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 0 14px;
    cursor: pointer;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--color-base-content);
    white-space: nowrap;
  }
  .import-button:hover {
    border-color: color-mix(in oklch, var(--color-base-content) 35%, transparent);
  }
  .import-button-running {
    border-color: color-mix(in oklch, var(--color-primary) 55%, transparent);
  }
  .import-button-running :global(svg) {
    color: var(--color-primary);
  }
  .import-button-done {
    border-color: color-mix(in oklch, var(--color-success) 55%, transparent);
  }
  .import-button-done :global(svg) {
    color: var(--color-success);
  }
  .import-button-failed {
    border-color: color-mix(in oklch, var(--color-error) 60%, transparent);
  }
  .import-button-failed :global(svg) {
    color: var(--color-error);
  }
  .import-button :global(.import-spin) {
    animation: import-spin 1s linear infinite;
  }
  @keyframes import-spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .import-button :global(.import-spin) {
      animation: none;
    }
  }
  /* Narrow: the icon carries it; the label stays as the accessible name. */
  @media (max-width: 480px) {
    .import-button {
      padding: 0 10px;
    }
    .import-button span {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip-path: inset(50%);
    }
  }
</style>
