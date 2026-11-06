<script lang="ts">
  import { onDestroy } from "svelte";
  import { Download } from "@lucide/svelte";
  import { requestSupportBundle, SupportDownloadError } from "./support/client";

  export let administrator = false;
  export let surface: "browse" | "operator" = "browse";
  let busy = false;
  let scrubSensitive = false;
  let error = "";
  let result = "";
  let controller: AbortController | null = null;
  let objectURL: string | null = null;
  let cleanupTimer: ReturnType<typeof setTimeout> | undefined;
  let alive = true;

  function cleanupURL(): void {
    if (objectURL) URL.revokeObjectURL(objectURL);
    objectURL = null;
    clearTimeout(cleanupTimer);
  }

  async function download(): Promise<void> {
    if (busy) return;
    busy = true;
    error = "";
    result = "";
    cleanupURL();
    const request = new AbortController();
    controller = request;
    const timeout = setTimeout(() => request.abort(), 35_000);
    try {
      const bundle = await requestSupportBundle(administrator, surface, request.signal, scrubSensitive ? "scrubbed" : "original");
      if (!alive) return;
      objectURL = URL.createObjectURL(bundle.blob);
      const link = document.createElement("a");
      link.href = objectURL;
      link.download = bundle.filename;
      document.body.append(link);
      try { link.click(); } finally { link.remove(); }
      // Let the browser start reading the Blob before releasing it.
      cleanupTimer = setTimeout(cleanupURL, 1000);
      result = bundle.administrator ? "Administrator support bundle downloaded." : "Your support bundle downloaded. An administrator can download additional server diagnostics.";
      result += scrubSensitive ? " Privacy filtering was on." : " Privacy filtering was off.";
    } catch (cause) {
      cleanupURL();
      if (alive) error = cause instanceof SupportDownloadError ? cause.message : request.signal.aborted
        ? "The support download timed out. Try again."
        : "Couldn’t reach Cassini. Check your connection and try again.";
    } finally {
      clearTimeout(timeout);
      if (alive) busy = false;
      if (controller === request) controller = null;
    }
  }

  onDestroy(() => { alive = false; controller?.abort(); cleanupURL(); });
</script>

<div class="support-download" aria-label="Cassini support">
  <div class="support-action">
    <button class="btn btn-sm btn-ghost" type="button" disabled={busy} on:click={download}>
      {#if busy}<span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
      {:else}<Download size={14} aria-hidden="true" />{/if}
      {busy ? "Preparing support bundle…" : "Download support bundle"}
    </button>
    <label class="support-choice">
      <input type="checkbox" class="checkbox checkbox-xs" bind:checked={scrubSensitive} disabled={busy} />
      Scrub sensitive information
    </label>
  </div>
  <p class="support-explanation">
    {#if scrubSensitive}Downloads scrubbed diagnostics. Review before sharing.
    {:else}Original logs may contain names, URLs, credentials or meeting text. Review or edit the ZIP before sharing.{/if}
    Nothing is sent automatically.
  </p>
  {#if error}<p class="text-error text-xs" role="alert">{error}</p>{/if}
  {#if result}<p class="text-xs" role="status">{result}</p>{/if}
</div>

<style>
  .support-download { flex-shrink: 0; color: var(--color-base-content, inherit); }
  .support-action { display: flex; flex-wrap: wrap; align-items: center; gap: 0.25rem 0.75rem; }
  .support-explanation { font-size: 0.75rem; opacity: 0.7; }
  .support-choice { display: inline-flex; align-items: center; gap: 0.375rem; font-size: 0.75rem; }
  p { margin: 0.25rem 0.5rem; }
</style>
