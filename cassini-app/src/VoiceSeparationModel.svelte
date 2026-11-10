<script lang="ts">
  // The optional speaker separation model, as its own Settings card: people
  // run it from a meeting's participant list, so it is not a step of what
  // happens after each meeting (docs/speaker-separation.md). Download, check,
  // cancel and retry work like a speech model; the inventory is the one the
  // transcript step polls.
  import { LL } from "cassini-viewer/i18n";
  import type { OperatorClient } from "./operator/client";
  import type { SpeechModelInventory } from "./operator/types";
  import ModelStatusRow from "./ModelStatusRow.svelte";
  import { jobRunning, jobStepText, mebibytes, modelJob, voiceSeparationModel } from "./speechModels";
  export let client: OperatorClient | null = null;
  export let inventory: SpeechModelInventory | null = null;
  let busy = false;
  let error = "";
  $: model = voiceSeparationModel(inventory);
  // It always installs and runs on the CPU, whatever transcribes.
  $: job = modelJob(inventory, model, "cpu");
  $: running = jobRunning(job);
  $: unsupported = model?.runtime_supported === false;
  $: t = $LL.app.settings.voice_separation;
  $: state = !model ? "" : running && job ? jobStepText(job)
    : model.ready ? t.state_ready()
    : unsupported ? t.state_unsupported()
    : model.installed ? t.state_installed()
    : job?.state === "cancelled" ? t.state_cancelled()
    : t.state_not_installed();
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
  <section class="op-tint voice-card" aria-labelledby="voice-separation-heading">
    <div>
      <h2 id="voice-separation-heading" class="set-row-name op-card-title">{t.heading()} <span class="optional">{t.optional()}</span></h2>
      <p class="set-row-sub">{t.description()}</p>
    </div>
    <ModelStatusRow name={model.name} size={mebibytes(model.download_bytes)} {state} quiet>
      <svelte:fragment slot="action">
        {#if running && job}
          <button type="button" class="btn btn-xs" disabled={busy} on:click={() => act("cancel")}>{t.cancel()}</button>
        {:else if !model.ready && !unsupported && (model.installed || inventory?.downloads_allowed)}
          <button type="button" class="btn btn-xs" disabled={busy} on:click={() => act(job ? "retry" : "install")}>{model.installed ? t.check() : job ? t.retry() : t.download()}</button>
        {/if}
      </svelte:fragment>
      {#if running && job && job.state === "downloading" && job.progress.total_bytes > 0}
        <progress max={job.progress.total_bytes} value={job.progress.completed_bytes} aria-label={t.progress_label()}></progress>
      {/if}
      {#if error || job?.error}<p class="op-error">{error || job?.error}</p>{/if}
    </ModelStatusRow>
  </section>
{/if}

<style>
  /* Same inset as the other settings cards (.pipe-step). */
  .voice-card { display: grid; gap: 12px; padding: 16px 18px 18px; }
  .optional { margin-left: 4px; font-size: 0.85em; font-weight: 400; color: color-mix(in oklch, var(--color-base-content) 60%, transparent); }
  progress { display: block; width: 100%; }
</style>
