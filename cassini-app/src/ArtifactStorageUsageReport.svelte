<script lang="ts">
  import { onMount } from "svelte";
  import { RefreshCw, TriangleAlert } from "@lucide/svelte";
  import { formatStorageBytes } from "./operator/storageUsage";
  import type { OperatorClient } from "./operator/client";
  import type { ArtifactStorageFileType, ArtifactStorageUsage } from "./operator/types";

  const FORMAT_COLORS = ["#5e81ac", "#a3be8c", "#d08770", "#b48ead", "#ebcb8b", "#88c0d0", "#bf616a"];
  export let operatorClient: OperatorClient | null = null;

  let usage: ArtifactStorageUsage | null = null;
  let loading = true;
  let recalculating = false;
  let loadError = "";

  onMount(() => void load());

  async function load(recalculate = false) {
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
        ? await operatorClient.recalculateArtifactStorageUsage()
        : await operatorClient.getArtifactStorageUsage();
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

  function formatLabel(extension: string): string {
    return extension === "none" ? "No extension" : extension;
  }

  function formatColor(extension: string): string {
    let hash = 0;
    for (const char of extension) hash = ((hash << 5) - hash + char.charCodeAt(0)) | 0;
    return FORMAT_COLORS[Math.abs(hash) % FORMAT_COLORS.length] ?? FORMAT_COLORS[0];
  }

  function formatShare(format: ArtifactStorageFileType, total: number): number {
    return total > 0 ? (format.bytes / total) * 100 : 0;
  }
</script>

<section class="report" aria-labelledby="artifact-storage-title">
  <header class="report-head">
    <div>
      <p class="report-kicker">Alternative report B</p>
      <h2 id="artifact-storage-title">Working archive and build history</h2>
      <p>Each retained artifact entry, split by the formats of the files inside it.</p>
    </div>
    <button class="op-btn recalculate" class:refreshing={recalculating} type="button" on:click={() => void load(true)} disabled={loading}>
      <RefreshCw size={15} aria-hidden="true" />
      {recalculating ? "Calculating…" : usage?.measured_at ? "Recalculate" : "Calculate"}
    </button>
  </header>

  {#if loadError && !usage}
    <div class="alert alert-error text-sm"><TriangleAlert size={16} aria-hidden="true" />Couldn’t load this report: {loadError}</div>
  {:else if loading && !usage}
    <div class="report-state">Loading this index…</div>
  {:else if usage && usage.measured_at === ""}
    <div class="report-state"><strong>This report has not been calculated yet</strong><span>Last refreshed: Never.</span></div>
  {:else if usage}
    {#if loadError}<div class="alert alert-error text-sm"><TriangleAlert size={16} aria-hidden="true" />Couldn’t recalculate: {loadError}</div>{/if}
    <p class="refreshed">Last refreshed {measuredAt(usage.measured_at)}</p>
    {#each usage.roots as root (root.id)}
      <section class="root-card op-tint" aria-labelledby={`artifact-root-${root.id}`}>
        <header class="root-head">
          <div><h3 id={`artifact-root-${root.id}`}>{root.label}</h3><p>{root.location}</p></div>
          {#if root.error}<span class="error" title={root.error}>Couldn’t measure</span>{:else}<strong>{formatStorageBytes(root.bytes)}</strong>{/if}
        </header>
        {#if !root.error && root.items.length === 0}
          <p class="empty-root">No retained artifacts in this directory.</p>
        {:else}
          <div class="artifact-list">
            {#each root.items as item (item.name)}
              <article class="artifact-row">
                <div class="artifact-title"><h4>{item.name}</h4>{#if item.error}<span class="error" title={item.error}>Couldn’t measure</span>{:else}<strong>{formatStorageBytes(item.bytes)}</strong>{/if}</div>
                {#if !item.error}
                  <div class="format-bar" role="img" aria-label={`${item.name}: ${item.formats.map((format) => `${formatLabel(format.extension)} ${formatStorageBytes(format.bytes)}`).join(", ")}`}>
                    {#each item.formats as format (format.extension)}
                      <span style:width={`${formatShare(format, item.bytes)}%`} style:background-color={formatColor(format.extension)} title={`${formatLabel(format.extension)} · ${formatStorageBytes(format.bytes)}`}></span>
                    {/each}
                  </div>
                  <div class="format-legend">
                    {#each item.formats as format (format.extension)}
                      <span><i style:background-color={formatColor(format.extension)}></i><b>{formatLabel(format.extension)}</b> {formatStorageBytes(format.bytes)} · {format.files} file{format.files === 1 ? "" : "s"}</span>
                    {/each}
                  </div>
                {/if}
              </article>
            {/each}
          </div>
        {/if}
      </section>
    {/each}
  {/if}
</section>

<style>
  .report { display: grid; gap: 12px; padding-top: 8px; }
  .report-head { display: flex; align-items: end; justify-content: space-between; gap: 16px; }
  .report-head h2 { margin: 2px 0 0; font-size: 16px; }
  .report-head p { margin: 4px 0 0; font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 62%, transparent); }
  .report-head .report-kicker { margin: 0; font-size: 10px; font-weight: 700; letter-spacing: .08em; text-transform: uppercase; }
  .recalculate { display: inline-flex; align-items: center; gap: 7px; }
  .report-state { display: grid; gap: 4px; padding: 16px; border-radius: 10px; background: var(--op-inset); font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 65%, transparent); }
  .refreshed { margin: -3px 0 0; font-size: 11px; text-align: right; color: color-mix(in oklch, var(--color-base-content) 58%, transparent); }
  .root-card { overflow: hidden; }
  .root-head { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 15px 18px; }
  .root-head h3 { margin: 0; font-size: 14px; }
  .root-head p { margin: 3px 0 0; font: 11px/1.4 ui-monospace, monospace; color: color-mix(in oklch, var(--color-base-content) 58%, transparent); }
  .root-head > strong { font-size: 17px; font-variant-numeric: tabular-nums; }
  .artifact-list { border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .artifact-row { padding: 14px 18px 15px; }
  .artifact-row + .artifact-row { border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .artifact-title { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .artifact-title h4 { margin: 0; font: 600 12px/1.4 ui-monospace, monospace; }
  .artifact-title strong { font-size: 13px; font-variant-numeric: tabular-nums; }
  .format-bar { display: flex; width: 100%; height: 9px; margin-top: 10px; overflow: hidden; border-radius: 999px; background: color-mix(in oklch, var(--color-base-content) 8%, var(--color-base-200)); }
  .format-bar span { min-width: 2px; }
  .format-legend { display: flex; flex-wrap: wrap; gap: 5px 14px; margin-top: 8px; }
  .format-legend span { display: inline-flex; align-items: center; gap: 4px; font-size: 10px; color: color-mix(in oklch, var(--color-base-content) 63%, transparent); }
  .format-legend i { width: 7px; height: 7px; border-radius: 2px; }
  .format-legend b { color: var(--color-base-content); }
  .empty-root { margin: 0; padding: 14px 18px; border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 58%, transparent); }
  .error { color: var(--color-error); font-size: 12px; font-weight: 600; }
  .refreshing :global(svg) { animation: report-spin .8s linear infinite; }
  @media (max-width: 600px) { .report-head { align-items: start; flex-direction: column; } .refreshed { text-align: left; } }
  @keyframes report-spin { to { transform: rotate(360deg); } }
</style>
