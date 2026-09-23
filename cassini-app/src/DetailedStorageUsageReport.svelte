<script lang="ts">
  import { onMount } from "svelte";
  import { HardDrive, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import { formatStorageBytes, storageUsageTotal } from "./operator/storageUsage";
  import type { OperatorClient } from "./operator/client";
  import type { ArtifactStorageFileType, DetailedStorageUsage } from "./operator/types";

  const FORMAT_COLORS = ["#5e81ac", "#a3be8c", "#d08770", "#b48ead", "#ebcb8b", "#88c0d0", "#bf616a"];
  export let operatorClient: OperatorClient | null = null;

  let usage: DetailedStorageUsage | null = null;
  let loading = true;
  let recalculating = false;
  let loadError = "";
  let lastActionWasRecalculation = false;

  $: publishedTotal = usage ? storageUsageTotal(usage.published) : null;
  // Show the cached state immediately, then rebuild without making the first
  // paint wait for a recursive filesystem and WebDAV scan.
  onMount(() => void loadThenRebuild());

  async function loadThenRebuild() {
    await load();
    void load(true);
  }

  async function load(recalculate = false): Promise<void> {
    if (!operatorClient) {
      loading = false;
      loadError = "Cassini’s operator connection is not available.";
      return;
    }
    loading = true;
    recalculating = recalculate;
    loadError = "";
    try {
      usage = recalculate
        ? await operatorClient.recalculateDetailedStorageUsage()
        : await operatorClient.getDetailedStorageUsage();
      lastActionWasRecalculation = recalculate;
      loading = false;
    } catch (error) {
      loadError = error instanceof Error ? error.message : String(error);
    } finally {
      loading = false;
      recalculating = false;
    }
  }

  function measuredAt(value: string): string {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "just now" : date.toLocaleString();
  }
  function formatLabel(extension: string): string { return extension === "none" ? "No extension" : extension; }
  function formatColor(extension: string): string {
    let hash = 0;
    for (const char of extension) hash = ((hash << 5) - hash + char.charCodeAt(0)) | 0;
    return FORMAT_COLORS[Math.abs(hash) % FORMAT_COLORS.length] ?? FORMAT_COLORS[0];
  }
  function formatShare(format: ArtifactStorageFileType, total: number): number {
    return total > 0 ? (format.bytes / total) * 100 : 0;
  }
</script>

<section class="report" aria-labelledby="detailed-storage-title">
  <header class="report-head">
    <div>
      <div class="op-panel-title"><HardDrive size={18} aria-hidden="true" /><h1 id="detailed-storage-title">Storage</h1></div>
      <p>Published storage, working archive, and build history.</p>
    </div>
    <div class="report-actions">
      <button class="op-btn recalculate" class:refreshing={recalculating} type="button" on:click={() => void load(true)} disabled={loading}>
        <RefreshCw size={15} aria-hidden="true" />
        {recalculating ? "Calculating…" : usage?.measured_at ? "Recalculate" : "Calculate"}
      </button>
    </div>
  </header>

  {#if loadError && !usage}
    <div class="alert alert-error text-sm"><TriangleAlert size={16} aria-hidden="true" />Couldn’t load storage details: {loadError}</div>
  {:else if loading && !usage}
    <div class="report-state">Loading this index…</div>
  {:else if usage && usage.measured_at === ""}
    <div class="report-state"><strong>Storage has not been calculated yet</strong><span>Last calculated: Never.</span></div>
  {:else if usage}
    {#if loadError}<div class="alert alert-error text-sm"><TriangleAlert size={16} aria-hidden="true" />Couldn’t recalculate: {loadError}</div>{/if}
    <div class="refresh-line">
      <span>Last calculated {measuredAt(usage.measured_at)}</span>
      {#if lastActionWasRecalculation}
        <strong>Index updated</strong>
      {/if}
    </div>

    <section class="report-card op-tint" aria-labelledby="published-storage-title">
      <header class="section-head">
        <div><h3 id="published-storage-title">Published storage</h3><p>Both possible Nextcloud storage-mode roots.</p></div>
        <strong>{publishedTotal === null ? "Partial result" : formatStorageBytes(publishedTotal)}</strong>
      </header>
      <div class="published-rows">
        {#each usage.published as source (source.id)}
          <article><div><h4>{source.label}</h4><p>{source.location}</p></div>{#if source.error}<span class="error" title={source.error}>Couldn’t measure</span>{:else}<strong>{formatStorageBytes(source.bytes)}</strong>{/if}</article>
        {/each}
      </div>
    </section>

    {#each usage.directories as directory (directory.id)}
      <section class="report-card op-tint" aria-labelledby={`storage-directory-${directory.id}`}>
        <header class="section-head">
          <div><h3 id={`storage-directory-${directory.id}`}>{directory.label}</h3><p class="path">{directory.location}</p></div>
          {#if directory.error}<span class="error" title={directory.error}>Couldn’t measure</span>{:else}<strong>{formatStorageBytes(directory.bytes)}</strong>{/if}
        </header>
        {#if !directory.error && directory.formats.length === 0}
          <p class="empty">No retained artifacts in this directory.</p>
        {:else if !directory.error}
          <div class="formats">
            <div class="format-bar" role="img" aria-label={`${directory.label}: ${directory.formats.map((format) => `${formatLabel(format.extension)} ${formatStorageBytes(format.bytes)}`).join(", ")}`}>
              {#each directory.formats as format (format.extension)}
                <span style:width={`${formatShare(format, directory.bytes)}%`} style:background-color={formatColor(format.extension)} title={`${formatLabel(format.extension)} · ${formatStorageBytes(format.bytes)}`}></span>
              {/each}
            </div>
            <div class="format-legend">
              {#each directory.formats as format (format.extension)}
                <span><i style:background-color={formatColor(format.extension)}></i><b>{formatLabel(format.extension)}</b> {formatStorageBytes(format.bytes)} · {format.files} file{format.files === 1 ? "" : "s"}</span>
              {/each}
            </div>
          </div>
        {/if}
      </section>
    {/each}
  {/if}
</section>

<style>
  .report { display: grid; gap: 12px; padding-top: 8px; }
  .report-head, .section-head, .refresh-line { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
  .report-head h1, .section-head h3, .published-rows h4 { margin: 0; }
  .report-head h1 { font-size: 16px; }
  .report-head p, .section-head p { margin: 4px 0 0; font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 62%, transparent); }
  .report-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
  .recalculate { display: inline-flex; align-items: center; gap: 7px; }
  .report-state { display: grid; gap: 4px; padding: 16px; border-radius: 10px; background: var(--op-inset); font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 65%, transparent); }
  .refresh-line { font-size: 11px; color: color-mix(in oklch, var(--color-base-content) 58%, transparent); }
  .refresh-line strong { color: color-mix(in oklch, var(--color-base-content) 78%, transparent); }
  .report-card { overflow: hidden; }
  .section-head { padding: 15px 18px; }
  .section-head h3 { font-size: 14px; }
  .section-head > strong { font-size: 17px; font-variant-numeric: tabular-nums; }
  .section-head .path, .published-rows p { font: 11px/1.4 ui-monospace, monospace; }
  .published-rows, .formats, .empty { border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .published-rows article { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 13px 18px; }
  .published-rows article + article { border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .published-rows h4 { font-size: 12px; }
  .published-rows p { margin: 3px 0 0; color: color-mix(in oklch, var(--color-base-content) 58%, transparent); }
  .published-rows strong { font-size: 14px; font-variant-numeric: tabular-nums; }
  .formats { padding: 14px 18px 15px; }
  .format-bar { display: flex; width: 100%; height: 10px; overflow: hidden; border-radius: 999px; background: color-mix(in oklch, var(--color-base-content) 8%, var(--color-base-200)); }
  .format-bar span { min-width: 2px; }
  .format-legend { display: flex; flex-wrap: wrap; gap: 5px 14px; margin-top: 9px; }
  .format-legend span { display: inline-flex; align-items: center; gap: 4px; font-size: 10px; color: color-mix(in oklch, var(--color-base-content) 63%, transparent); }
  .format-legend i { width: 7px; height: 7px; border-radius: 2px; }
  .format-legend b { color: var(--color-base-content); }
  .empty { margin: 0; padding: 14px 18px; font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 58%, transparent); }
  .error { color: var(--color-error); font-size: 12px; font-weight: 600; }
  .refreshing :global(svg) { animation: report-spin .8s linear infinite; }
  @media (max-width: 600px) { .report-head, .section-head, .refresh-line { align-items: start; flex-direction: column; } }
  @keyframes report-spin { to { transform: rotate(360deg); } }
</style>
