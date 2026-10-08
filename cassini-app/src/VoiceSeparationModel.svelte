<script lang="ts">
  // The optional speaker separation model in Settings: download, check,
  // cancel and retry like a speech model, but never selectable for
  // transcription. Once it is ready, people separate voices from a meeting's
  // participant list (docs/speaker-separation.md).
  import { createEventDispatcher } from "svelte";
  import type { SpeechModel, SpeechModelJob } from "./operator/types";
  import { jobRunning, mebibytes } from "./speechModels";
  export let model: SpeechModel;
  export let job: SpeechModelJob | undefined = undefined;
  export let downloadsAllowed = false;
  export let busy = false;
  const dispatch = createEventDispatcher<{ install: void; cancel: void; retry: void }>();
  $: running = jobRunning(job);
  $: unsupported = model.runtime_supported === false;
</script>

<div class="voice-separation">
  <p class="set-row-name op-card-title">Voice separation (optional)</p>
  <p class="set-row-sub">When several people share one device in a call, someone who can read the meeting can separate their voices from its participant list. It runs on this server, only when asked. {model.name}: download {mebibytes(model.download_bytes)} · installed {mebibytes(model.installed_bytes)}.</p>
  {#if running && job}
    <p role="status">{job.state} {job.progress.file ? `— ${job.progress.file}` : ""}</p>
    {#if job.progress.total_bytes > 0}
      <progress max={job.progress.total_bytes} value={job.progress.completed_bytes} aria-label="Voice separation download progress"></progress>
      <p class="set-row-sub">{mebibytes(job.progress.completed_bytes)} / {mebibytes(job.progress.total_bytes)} · {Math.min(100, Math.round(job.progress.completed_bytes / job.progress.total_bytes * 100))}%</p>
    {/if}
    <button type="button" class="op-button" disabled={busy} on:click={() => dispatch("cancel")}>Cancel</button>
  {:else}
    <p role="status">{model.ready ? "Ready" : unsupported ? "This server's Cassini runtime cannot run voice separation." : model.installed ? "Installed — runtime check needed" : "Not installed"}</p>
    {#if job?.error}<p class="op-error">{job.error}</p>{/if}
    {#if model.ready}
      <p class="set-row-sub">People can now separate voices in a meeting's participant list. Transcription settings are not affected.</p>
    {:else if !unsupported && (model.installed || downloadsAllowed)}
      <button type="button" class="op-button" disabled={busy} on:click={() => dispatch(job ? "retry" : "install")}>{model.installed ? "Check model" : job ? "Retry download" : "Download voice separation"}</button>
    {/if}
  {/if}
</div>
<style>
  .voice-separation { margin-top: 1rem; padding-top: 1rem; border-top: 1px solid var(--color-border, #d7dde2); }
  progress { display: block; width: 100%; margin: .5rem 0; }
</style>
