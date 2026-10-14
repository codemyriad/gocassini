<script lang="ts">
  // An optional model as its own Settings card: voice separation and topic
  // suggestions. Neither is a step of transcription, and both always install
  // and run on the CPU, whatever transcribes. Download, check, cancel and
  // retry work like a speech model; the inventory is the one the transcript
  // step polls. The default slot says what the model is for.
  import type { OperatorClient } from "./operator/client";
  import type { SpeechModel, SpeechModelInventory } from "./operator/types";
  import ModelStatusRow from "./ModelStatusRow.svelte";
  import { jobRunning, jobStepText, mebibytes, modelJob } from "./speechModels";
  export let client: OperatorClient | null = null;
  export let inventory: SpeechModelInventory | null = null;
  export let model: SpeechModel | undefined;
  export let title: string;
  export let headingId: string;
  let busy = false;
  let error = "";
  $: job = modelJob(inventory, model, "cpu");
  $: running = jobRunning(job);
  $: unsupported = model?.runtime_supported === false;
  $: state = !model ? "" : running && job ? jobStepText(job)
    : model.ready ? "Ready"
    : unsupported ? "Not available in this server's Cassini build"
    : model.installed ? "Installed — runtime check needed"
    : job?.state === "cancelled" ? "Download cancelled. Retry resumes it."
    : "Not installed";
  async function act(kind: "install" | "cancel" | "retry") {
    if (!client || !model || busy) return;
    busy = true;
    error = "";
    try {
      if (kind === "install") await client.installSpeechModel(model.id, model.revision, "cpu");
      else if (job) await client.speechModelJobAction(job.id, kind);
    } catch (e) { error = e instanceof Error ? e.message : String(e); }
    finally { busy = false; }
  }
</script>

{#if model}
  <section class="op-tint optional-card" aria-labelledby={headingId}>
    <div>
      <h2 id={headingId} class="set-row-name op-card-title">{title} <span class="optional">(optional)</span></h2>
      <p class="set-row-sub"><slot /></p>
    </div>
    <ModelStatusRow name={model.name} size={mebibytes(model.download_bytes)} {state} quiet>
      <svelte:fragment slot="action">
        {#if running && job}
          <button type="button" class="btn btn-xs" disabled={busy} on:click={() => act("cancel")}>Cancel</button>
        {:else if !model.ready && !unsupported && (model.installed || inventory?.downloads_allowed)}
          <button type="button" class="btn btn-xs" disabled={busy} on:click={() => act(job ? "retry" : "install")}>{model.installed ? "Check" : job ? "Retry" : "Download"}</button>
        {/if}
      </svelte:fragment>
      {#if running && job && job.state === "downloading" && job.progress.total_bytes > 0}
        <progress max={job.progress.total_bytes} value={job.progress.completed_bytes} aria-label="{title} download progress"></progress>
      {/if}
      {#if error || job?.error}<p class="op-error">{error || job?.error}</p>{/if}
    </ModelStatusRow>
  </section>
{/if}

<style>
  /* Same inset as the other settings cards (.pipe-step, .hints-card). */
  .optional-card { display: grid; gap: 12px; padding: 16px 18px 18px; }
  .optional { margin-left: 4px; font-size: 0.85em; font-weight: 400; color: color-mix(in oklch, var(--color-base-content) 60%, transparent); }
  progress { display: block; width: 100%; }
</style>
