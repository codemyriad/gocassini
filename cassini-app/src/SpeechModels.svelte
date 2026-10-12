<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import type { OperatorClient } from "./operator/client";
  import type { SpeechModel, SpeechModelInventory } from "./operator/types";
  import ModelStatusRow from "./ModelStatusRow.svelte";
  import { speechModelFor } from "./operator/pipelineStatus";
  export let client: OperatorClient | null;
  export let quality = "balanced";
  export let device = "cpu";
  export let enabled = false;
  export let activeModel = "";
  export let activeRevision = "";
  // The model the transcription switch can turn on, if it is ready.
  export let readyModel: SpeechModel | null = null;
  // The last inventory read, so the panel's status strip can see downloads.
  export let inventory: SpeechModelInventory | null = null;
  const dispatch = createEventDispatcher<{ select: SpeechModel }>();
  let error = "";
  let busy = false;
  let mounted = false;
  let requestedDevice = "";
  let generation = 0;
  let lastUpdate = "";
  let refreshing = false;
  async function refresh() {
    if (!client || !mounted || refreshing) return;
    refreshing = true;
    const current = generation;
    const target = device;
    try {
      const result = await client.getSpeechModels(target);
      if (!mounted || current !== generation || target !== device) return;
      inventory = result;
      error = "";
      lastUpdate = new Date().toLocaleTimeString();
    } catch (e) {
      if (mounted && current === generation) error = `Cannot refresh model status: ${e instanceof Error ? e.message : String(e)}`;
    } finally { refreshing = false; }
  }
  onMount(() => {
    mounted = true;
    requestedDevice = device;
    void refresh();
    const timer = setInterval(() => void refresh(), 2000);
    return () => { mounted = false; generation++; clearInterval(timer); };
  });
  $: if (mounted && requestedDevice !== device) {
    requestedDevice = device;
    generation++;
    inventory = null;
    void refresh();
  }
  $: modelID = speechModelFor(quality, device);
  $: model = inventory?.models.find((m) => m.id === modelID);
  $: job = inventory?.jobs.find((j) => j.model === modelID && j.device === device && j.revision === model?.revision);
  $: running = job && !["ready", "failed", "cancelled"].includes(job.state);
  $: selected = enabled && activeModel === model?.id && activeRevision === model?.revision;
  $: readyModel = model?.ready ? model : null;
  function size(n: number) {
    return n >= 1024 ** 3 ? `${(n / 1024 ** 3).toFixed(1)} GiB` : `${(n / 1024 / 1024).toFixed(1)} MiB`;
  }
  $: percent = running && job && job.progress.total_bytes > 0 ? Math.min(100, Math.round(job.progress.completed_bytes / job.progress.total_bytes * 100)) : null;
  $: state = !model ? "" : running && job ? `${job.state}${percent === null ? "" : ` · ${percent}%`}`
    : model.ready ? "Ready"
    : model.installed ? "Installed — runtime check needed"
    : job?.state === "cancelled" ? "Download cancelled. Retry resumes it."
    : "Not installed";
  async function action(kind: "install" | "cancel" | "retry") {
    if (!client || !model || busy) return;
    busy = true;
    try {
      if (kind === "install") await client.installSpeechModel(model.id, model.revision, device);
      else if (job) await client.speechModelJobAction(job.id, kind);
      await refresh();
    } catch (e) { error = e instanceof Error ? e.message : String(e); }
    finally { busy = false; }
  }
</script>

<div class="speech-models">
  {#if error}<p role="status" class="op-error">{error} {lastUpdate ? `Last updated ${lastUpdate}.` : ""}</p>{/if}
  {#if model}
    <ModelStatusRow name={model.name} size={size(model.download_bytes)} {state}>
      <svelte:fragment slot="action">
        {#if running && job}
          <button type="button" class="op-button" disabled={busy} on:click={() => action("cancel")}>Cancel</button>
        {:else if model.ready}
          {#if enabled && !selected}<button type="button" class="op-button" on:click={() => dispatch("select", model!)}>Use this model</button>{/if}
        {:else if model.installed || inventory?.downloads_allowed}
          <button type="button" class="op-button" disabled={busy} on:click={() => action(job ? "retry" : "install")}>{model.installed ? "Check" : job ? "Retry" : "Download"}</button>
        {/if}
      </svelte:fragment>
      {#if running && job}
        {#if job.progress.total_bytes > 0}
          <progress max={job.progress.total_bytes} value={job.progress.completed_bytes} aria-label="Model download progress"></progress>
          <p class="set-row-sub">{size(job.progress.completed_bytes)} / {size(job.progress.total_bytes)}{#if job.progress.reused_bytes > 0} · {size(job.progress.reused_bytes)} reused{/if}{job.progress.file ? ` · ${job.progress.file}` : ""}</p>
        {/if}
        <p class="set-row-sub">You can close this page while the download continues.</p>
      {:else if job?.error}
        <p class="op-error">{job.error}</p>
      {/if}
      {#if !inventory?.downloads_allowed}
        <p class="set-row-sub">Downloads are disabled. Prepare a package with <code>cassini models pack</code> on a connected machine, copy it here, and run <code>cassini models import</code> inside this server's Cassini runtime. Imported models appear here automatically.</p>
      {/if}
    </ModelStatusRow>
  {:else if !error}<p class="set-row-sub">Loading model inventory…</p>{/if}
</div>
<style>
  .speech-models { display: grid; gap: 8px; margin-top: 1rem; }
  progress { display: block; width: 100%; }
  code { overflow-wrap: anywhere; }
</style>
