<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import type { OperatorClient } from "./operator/client";
  import type { SpeechModel, SpeechModelInventory } from "./operator/types";
  export let client: OperatorClient | null;
  export let quality = "balanced";
  export let device = "cpu";
  export let enabled = false;
  export let activeModel = "";
  export let activeRevision = "";
  const dispatch = createEventDispatcher<{ select: SpeechModel; disable: void }>();
  let inventory: SpeechModelInventory | null = null;
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
  $: modelID = device === "cuda" || quality === "best" ? "parakeet-tdt-0.6b-v3" : quality === "fast" ? "parakeet-tdt-ctc-110m-en-int8" : "parakeet-tdt-0.6b-v3-int8";
  $: model = inventory?.models.find((m) => m.id === modelID);
  $: job = inventory?.jobs.find((j) => j.model === modelID && j.device === device && j.revision === model?.revision);
  $: running = job && !["ready", "failed", "cancelled"].includes(job.state);
  $: selected = enabled && activeModel === model?.id && activeRevision === model?.revision;
  function size(n: number) { return `${(n / 1024 / 1024).toFixed(1)} MiB`; }
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
  <div class="step-head">
    <p class="set-row-name op-card-title">Transcription: {enabled ? "On" : "Off"}</p>
    {#if enabled}<button type="button" class="op-button" on:click={() => dispatch("disable")}>Turn off</button>{/if}
  </div>
  <p class="set-row-sub">{enabled ? "The selected model applies to future recordings after saving." : "Recordings are available as audio. No model is needed."}</p>
  {#if error}<p role="status" class="op-error">{error} {lastUpdate ? `Last updated ${lastUpdate}.` : ""}</p>{/if}
  {#if model}
    <p><strong>{model.name}</strong></p>
    <p class="set-row-sub">{model.description}. Download {size(model.download_bytes)} · Installed {size(model.installed_bytes)} including voice activity detection.</p>
    {#if running && job}
      <p role="status">{job.state} {job.progress.file ? `— ${job.progress.file}` : ""}</p>
      {#if job.progress.total_bytes > 0}
        <progress max={job.progress.total_bytes} value={job.progress.completed_bytes} aria-label="Model download progress"></progress>
        <p class="set-row-sub">{size(job.progress.completed_bytes)} / {size(job.progress.total_bytes)} · {Math.min(100, Math.round(job.progress.completed_bytes / job.progress.total_bytes * 100))}%
        {#if job.progress.reused_bytes > 0} · {size(job.progress.reused_bytes)} reused{/if}</p>
      {/if}
      <p class="set-row-sub">Last update: {job.updated_at}. You can close this page while installation continues.</p>
      <button type="button" class="op-button" disabled={busy} on:click={() => action("cancel")}>Cancel</button>
    {:else}
      <p role="status">{model.ready ? "Ready" : model.installed ? "Installed — runtime check needed" : "Not installed"}</p>
      {#if job?.error}<p class="op-error">{job.error}</p>{/if}
      {#if job?.state === "cancelled"}<p class="set-row-sub">Cancelled. Retained files can be reused on retry.</p>{/if}
      {#if model.ready}
        <button type="button" class="op-button" disabled={selected} on:click={() => dispatch("select", model!)}>{selected ? "Selected" : enabled ? "Use this model" : "Enable transcription"}</button>
        <p class="set-row-sub">Save settings to apply your choice. Installation never enables transcription automatically.</p>
      {:else if model.installed || inventory?.downloads_allowed}
        <button type="button" class="op-button" disabled={busy} on:click={() => action(job ? "retry" : "install")}>{model.installed ? "Check model" : job ? "Retry download" : "Download model"}</button>
      {/if}
    {/if}
    {#if !inventory?.downloads_allowed}
      <p class="set-row-sub">Downloads are disabled. Prepare a package with <code>cassini models pack</code> on a connected machine, copy it here, and run <code>cassini models import</code> inside this server's Cassini runtime. Imported models appear here automatically.</p>
    {/if}
  {:else if !error}<p class="set-row-sub">Loading model inventory…</p>{/if}
</div>
<style>
  .speech-models { margin-top: 1.5rem; padding-top: 1rem; border-top: 1px solid var(--color-border, #d7dde2); }
  .step-head { display: flex; align-items: center; gap: 1rem; }
  progress { display: block; width: 100%; margin: .5rem 0; }
  code { overflow-wrap: anywhere; }
</style>
