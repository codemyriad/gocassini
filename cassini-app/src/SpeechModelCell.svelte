<script lang="ts">
  // One speech model inside a quality row of the Transcript step: its name
  // and size, its state, one action, and under the row the download's
  // progress or error. The inventory is the one the panel polls; this never
  // fetches, it acts and asks the panel to refresh.
  //
  // Three root elements rather than one box: the row that holds this is a
  // grid, and the parent places the meta, the action and the detail in it.
  import { createEventDispatcher } from "svelte";
  import type { OperatorClient } from "./operator/client";
  import type { SpeechModel, SpeechModelInventory } from "./operator/types";
  import { jobRunning, jobStepText, modelJob } from "./speechModels";
  export let client: OperatorClient | null = null;
  export let inventory: SpeechModelInventory | null = null;
  export let modelId: string;
  export let device = "cpu";
  // Whether this is the model new recordings transcribe with, in the draft.
  export let selected = false;
  // "queued" when a download or check was started here: the panel picks the
  // model once it is ready. "refresh" after any action, so the row moves
  // before the next poll.
  const dispatch = createEventDispatcher<{ queued: SpeechModel; refresh: void }>();
  let busy = false;
  let error = "";
  $: model = inventory?.models.find((m) => m.id === modelId);
  $: job = modelJob(inventory, model, device);
  $: running = jobRunning(job);
  function size(n: number) {
    return n >= 1024 ** 3 ? `${(n / 1024 ** 3).toFixed(1)} GiB` : `${(n / 1024 / 1024).toFixed(1)} MiB`;
  }
  $: state = !model ? "" : running && job ? jobStepText(job)
    : model.ready ? "Ready"
    // Not checked against this build yet. The model in use is checked before
    // the next recording on its own; any other one when someone presses Check.
    : model.installed ? (selected ? "Installed · checked before the next recording" : "Installed · not checked on this build yet")
    : job?.state === "cancelled" ? "Download cancelled. Retry resumes it."
    : "Not installed";
  async function action(kind: "install" | "cancel" | "retry") {
    if (!client || !model || busy) return;
    busy = true;
    error = "";
    try {
      if (kind === "install") await client.installSpeechModel(model.id, model.revision, device);
      else if (job) await client.speechModelJobAction(job.id, kind);
      if (kind !== "cancel") dispatch("queued", model);
      dispatch("refresh");
    } catch (e) { error = e instanceof Error ? e.message : String(e); }
    finally { busy = false; }
  }
</script>

<span class="model-meta">
  {#if model}
    <span class="model-name">{model.name} · {size(model.download_bytes)}</span>
    <span class="model-state" class:ready={model.ready && !running} role="status">{state}</span>
  {/if}
</span>
<span class="model-action">
  {#if model}
    {#if running && job}
      <button type="button" class="btn btn-xs" disabled={busy} on:click={() => action("cancel")}>Cancel</button>
    {:else if model.ready}
      {#if selected}<span class="model-inuse">In use</span>{/if}
    {:else if model.installed || inventory?.downloads_allowed}
      <button type="button" class="btn btn-xs" disabled={busy} on:click={() => action(job ? "retry" : "install")}>{model.installed ? "Check" : job ? "Retry" : "Download"}</button>
    {/if}
  {/if}
</span>
{#if model && (error || (running && job) || job?.error)}
  <div class="model-detail">
    {#if error}<p class="model-error" role="status">{error}</p>{/if}
    {#if running && job}
      {#if job.state === "downloading" && job.progress.total_bytes > 0}
        <progress max={job.progress.total_bytes} value={job.progress.completed_bytes} aria-label="Model download progress"></progress>
        <p>{size(job.progress.completed_bytes)} / {size(job.progress.total_bytes)}{#if job.progress.reused_bytes > 0} · {size(job.progress.reused_bytes)} reused{/if}{job.progress.file ? ` · ${job.progress.file}` : ""}</p>
      {/if}
      <p>You can close this page; it carries on.</p>
    {:else if job?.error}
      <p class="model-error">{job.error}</p>
    {/if}
  </div>
{/if}

<style>
  .model-meta {
    display: grid;
    gap: 1px;
    min-width: 0;
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .model-name {
    overflow-wrap: anywhere;
  }
  .model-state {
    font-weight: 500;
  }
  .model-state.ready {
    color: var(--color-success);
  }
  .model-action {
    display: flex;
    align-items: center;
  }
  .model-inuse {
    padding: 2px 8px;
    font-size: 11px;
    font-weight: 600;
    color: var(--color-success);
    border: 1px solid color-mix(in srgb, var(--color-success) 45%, transparent);
    border-radius: var(--radius-field, 0.375rem);
    white-space: nowrap;
  }
  .model-detail {
    display: grid;
    gap: 4px;
    min-width: 0;
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .model-detail p {
    margin: 0;
  }
  .model-error {
    color: var(--color-warning);
  }
  progress {
    display: block;
    width: 100%;
  }
</style>
