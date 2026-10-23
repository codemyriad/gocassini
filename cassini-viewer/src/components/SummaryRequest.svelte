<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { FileText } from "@lucide/svelte";

  import { summaryCostLine, summaryEndpointLine, type SummaryRequestState } from "../viewer/meetingSummary";

  // Asking for a meeting's summary (D-814): "write" fills a missing one, which
  // any reader may; "regenerate" replaces the one readers have, which only an
  // administrator or a moderator of the room may. Either costs a model call,
  // so the cost is shown and confirmed before anything is sent.
  export let state: SummaryRequestState;
  export let mode: "write" | "regenerate";
  // Why the meeting has no summary, when its file says (write only).
  export let reasonText: string | null = null;
  export let error = "";

  const dispatch = createEventDispatcher<{ request: { replace: boolean } }>();
  let confirming = false;

  $: label = mode === "write" ? "Write summary" : "Regenerate summary";
  $: offered = mode === "write" ? state.canWrite : state.canRegenerate;
  $: if (state.state === "writing") confirming = false;

  function confirm() {
    confirming = false;
    dispatch("request", { replace: mode === "regenerate" });
  }
</script>

<div class="sr" class:sr-write={mode === "write"} class:sr-confirming={confirming}>
  {#if reasonText}
    <p class="sr-reason">{reasonText}</p>
  {/if}
  {#if state.state === "writing"}
    <p class="sr-status" role="status">
      <span class="cassini-spinner sr-spinner" aria-hidden="true"></span>
      Writing the summary… It appears here when it is ready.
    </p>
  {:else}
    {#if state.state === "failed"}
      <p class="sr-status sr-failed" role="status">The last attempt to write a summary failed.</p>
    {/if}
    {#if confirming}
      <div class="sr-confirm" role="alertdialog" aria-label={mode === "write" ? "Write a summary for this meeting?" : "Replace this meeting's summary?"}>
        <p class="sr-confirm-title">{mode === "write" ? "Write a summary for this meeting?" : "Replace this meeting's summary?"}</p>
        <p>{summaryCostLine(state)}</p>
        <p class="sr-note">{summaryEndpointLine(state)}</p>
        {#if mode === "write"}
          <p class="sr-note">Everyone who can open the meeting will see it.</p>
        {:else}
          <p class="sr-note">Everyone who can open the meeting sees the new summary instead of this one.</p>
        {/if}
        <div class="sr-actions">
          <button type="button" class="btn btn-sm btn-primary" on:click={confirm}>{label}</button>
          <button type="button" class="btn btn-sm btn-ghost" on:click={() => (confirming = false)}>Cancel</button>
        </div>
      </div>
    {:else if offered}
      <button type="button" class="btn btn-sm sr-button" on:click={() => (confirming = true)}>
        <FileText size={15} aria-hidden="true" />{label}
      </button>
    {/if}
  {/if}
  {#if error}
    <p class="sr-status sr-failed" role="alert">{error}</p>
  {/if}
</div>

<style>
  .sr {
    display: grid;
    gap: 8px;
    justify-items: start;
    font-size: 13px;
    color: color-mix(in oklch, var(--color-base-content) 75%, transparent);
  }
  .sr-write {
    margin-bottom: 16px;
    padding: 12px 14px;
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 0.5rem);
  }
  /* The confirmation is its own box; the block around it steps aside. */
  .sr-write.sr-confirming {
    padding: 0;
    border: 0;
  }
  .sr-reason {
    line-height: 1.45;
  }
  .sr-status {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .sr-failed {
    color: var(--color-error);
  }
  .sr-spinner {
    width: 12px;
    height: 12px;
    border-width: 2px;
  }
  .sr-confirm {
    display: grid;
    gap: 6px;
    width: 100%;
    padding: 12px 14px;
    border: 1px solid color-mix(in oklch, var(--color-warning) 50%, transparent);
    border-radius: var(--radius-box, 0.5rem);
    background-color: color-mix(in oklch, var(--color-warning) 10%, transparent);
    color: var(--color-base-content);
  }
  .sr-confirm-title {
    font-weight: 600;
  }
  .sr-note {
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .sr-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 2px;
  }
</style>
