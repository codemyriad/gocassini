<script lang="ts">
  import { onDestroy } from "svelte";
  import { Download, ShieldCheck } from "@lucide/svelte";
  import { requestSupportBundle, SupportDownloadError } from "./support/client";

  // administrator: Operator › Help's installation bundle. Otherwise the
  // reader's own bundle from Browse › Help (D-910). Both offer scrubbing:
  // a reader's original bundle still holds their user ID and error text.
  export let administrator = false;
  export let surface: "browse" | "operator" = "browse";
  // Browse › Help offers an administrator the way to Operator › Help instead
  // of a second copy of the installation bundle.
  export let openOperatorHelp: (() => void) | null = null;
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
      result = `${bundle.administrator ? "Administrator support bundle" : "Support bundle"} downloaded. Privacy filtering was ${scrubSensitive ? "on" : "off"}.`;
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
  <p class="support-lede">
    {#if administrator}
      The bundle covers the whole installation: versions and settings, the last seven days of jobs, stage logs
      and sharing events. It has no recordings, transcripts or summaries.
    {:else}
      The bundle includes your Nextcloud user ID, your browser details, the recordings shared with you and any
      error messages. It has no meeting content and no server logs.
    {/if}
  </p>

  <label class="support-choice">
    <input type="checkbox" class="cassini-check" bind:checked={scrubSensitive} disabled={busy} />
    <span>
      <span class="support-choice-label">Scrub sensitive information</span>
      <span class="support-hint">
        {#if administrator}
          Logs can include names, URLs, passwords or meeting text. Scrubbing removes them, but may also remove
          details needed to find the problem.
        {:else}
          Leaves out your user ID and the error messages. Counts and status codes stay.
        {/if}
      </span>
    </span>
  </label>

  <div class="support-action">
    <button class="btn btn-sm btn-primary" type="button" disabled={busy} on:click={download}>
      {#if busy}<span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
      {:else}<Download size={14} aria-hidden="true" />{/if}
      {busy ? "Preparing support bundle…" : "Download support bundle"}
    </button>
  </div>
  <p class="support-fine">Nothing is sent automatically. Check the ZIP before you share it.</p>
  {#if error}<p class="support-status text-error" role="alert">{error}</p>{/if}
  {#if result}<p class="support-status" role="status">{result}</p>{/if}

  {#if openOperatorHelp}
    <aside class="support-admin" aria-labelledby="support-admin-title">
      <ShieldCheck size={16} class="support-admin-icon" aria-hidden="true" />
      <div>
        <h3 id="support-admin-title">Administrators</h3>
        <p>This bundle covers only your own account. For a problem with recording or processing, download the
          installation bundle instead: it adds server logs and job history.</p>
        <button type="button" class="btn btn-xs btn-ghost support-admin-link" on:click={openOperatorHelp}>
          Open Operator › Help
        </button>
      </div>
    </aside>
  {/if}
</div>

<style>
  .support-download {
    display: grid;
    gap: 14px;
    flex-shrink: 0;
    font-size: 13px;
    line-height: 1.5;
    color: var(--color-base-content, inherit);
  }
  p, h3 { margin: 0; }
  .support-lede { color: color-mix(in oklch, var(--color-base-content) 80%, transparent); }
  .support-choice {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    cursor: pointer;
  }
  .support-choice input { margin-top: 2px; }
  .support-choice-label { display: block; font-weight: 500; }
  .support-hint {
    display: block;
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .support-action { display: flex; }
  .support-fine {
    margin-top: -6px;
    font-size: 12px;
    font-style: italic;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .support-status { font-size: 12px; }
  .support-admin {
    display: flex;
    gap: 10px;
    margin-top: 4px;
    padding: 12px;
    border: 1px solid color-mix(in oklch, var(--color-base-content) 12%, transparent);
    border-radius: var(--radius-box, 0.5rem);
    background-color: color-mix(in oklch, var(--color-base-content) 4%, transparent);
  }
  .support-admin :global(.support-admin-icon) {
    flex: none;
    margin-top: 2px;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .support-admin h3 { font-size: 13px; font-weight: 600; }
  .support-admin p {
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .support-admin-link { margin: 6px 0 0 -8px; gap: 4px; }
</style>
