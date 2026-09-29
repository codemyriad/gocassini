<script lang="ts">
  import { onMount } from "svelte";
  import { HardDrive, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import { formatStorageBytes, storageUsageTotal } from "./operator/storageUsage";
  import type { OperatorClient } from "./operator/client";
  import type { DetailedStorageUsage } from "./operator/types";

  import StorageCategoryChart from "./StorageCategoryChart.svelte";
  import { categoryPresentation, displayStorageCategories } from "./operator/storageCharts";
  let splitHistory = false;
  let expanded: Record<string, boolean> = {};
  $: categories = usage ? displayStorageCategories(usage.categories, splitHistory) : [];
  $: localTotal = usage ? usage.categories.reduce((sum, category) => sum + category.bytes, 0) : 0;
  $: partial = !!usage && (!!usage.category_error || usage.directories.some(directory => !!directory.error));
  $: largest = Math.max(0, ...categories.map(category => category.bytes));
  function presentation(id: string) { return categoryPresentation[id] ?? { label: id, description: "Retained local files.", color: "#7d8490" }; }
  function openCategory(id: string) {
    expanded = { ...expanded, [id]: true };
    requestAnimationFrame(() => document.getElementById(`category-${id}`)?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }
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
</script>

<section class="report" aria-labelledby="detailed-storage-title">
  <header class="report-head">
    <div>
      <div class="op-panel-title"><HardDrive size={18} aria-hidden="true" /><h1 id="detailed-storage-title">Storage</h1></div>
      <p>See what is retained, when it is from, and which policy controls it.</p>
    </div>
    <div class="report-actions">
      <button class="op-btn" type="button" on:click={() => document.getElementById("retention-policies")?.scrollIntoView({ block: "start" })}>Retention policies</button>
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

    <div class="totals">
      <section class="total-card op-tint"><span>Retained locally</span><strong>{partial ? "Partial result" : usage.categories.length === 0 ? "Not available" : formatStorageBytes(localTotal)}</strong><p>Source recordings, outputs, history and logs.</p></section>
      <section class="total-card op-tint"><span>Published in Nextcloud</span><strong>{publishedTotal === null ? "Couldn’t measure" : formatStorageBytes(publishedTotal)}</strong><p>Includes any legacy archives. Local retention does not delete these files.</p></section>
    </div>
    <p class="scope-note">File sizes in the recording and build folders, including each retained copy. These totals do not measure free disk space.</p>
    {#if partial}<p class="alert alert-warning" role="status">Some local data could not be measured or classified. Values below are partial; recalculate to retry.</p>{/if}
    {#if publishedTotal === null || partial}
      <details class="measurement-errors"><summary>Measurement details</summary>
        {#if usage.category_error}<p>{usage.category_error}</p>{/if}
        {#each [...usage.published, ...usage.directories].filter(source => source.error) as source}<p>{source.label}: {source.error}</p>{/each}
      </details>
    {/if}
    {#if usage.categories.length === 0}
      <p class="report-state">Category data is not available yet. Recalculate after updating the operator.</p>
    {:else}
      <section class="report-card op-tint" aria-labelledby="category-comparison-title">
        <header class="section-head"><div><h2 id="category-comparison-title">Usage by retention category</h2><p>Compare all retained bytes. Select a category to explore its dates.</p></div>
          <label class="history-toggle"><input type="checkbox" bind:checked={splitHistory} />Split attempt history</label>
        </header>
        <div class="comparison">
          {#each categories as category (category.id)}
            {@const info = presentation(category.id)}
            <button type="button" class="comparison-row" on:click={() => openCategory(category.id)} aria-label={`Explore ${info.label}: ${formatStorageBytes(category.bytes)}`}>
              <span class="category-label">{info.label}</span>
              <span class="comparison-track" aria-hidden="true"><span style:width={`${largest > 0 ? category.bytes / largest * 100 : 0}%`} style:background={info.color}></span></span>
              <strong>{formatStorageBytes(category.bytes)}</strong>
            </button>
          {/each}
        </div>
      </section>
      <div class="dates-heading"><h2>Explore retained storage by date</h2><p>Bytes that are still retained, grouped by the UTC lifecycle dates used for retention. This is not a history of past disk usage. Chart controls do not change retention policies.</p></div>
      <div class="category-list">
        {#each categories as category (category.id)}
          {@const info = presentation(category.id)}
          <details class="category-card op-tint" id={`category-${category.id}`} bind:open={expanded[category.id]}>
            <summary><span class="category-dot" style:background={info.color}></span><span class="category-name">{info.label}<small>{category.files.toLocaleString()} file{category.files === 1 ? "" : "s"}</small></span><strong>{formatStorageBytes(category.bytes)}</strong></summary>
            <p class="category-description">{info.description}</p>
            <StorageCategoryChart {category} measuredAt={usage.measured_at} label={info.label} color={info.color} />
          </details>
        {/each}
      </div>
    {/if}
  {/if}
</section>

<style>
  .report { display:grid; grid-template-columns:minmax(0,1fr); min-width:0; gap:14px; padding:8px 0 28px; }
  .report-head, .section-head, .refresh-line { display:flex; align-items:center; justify-content:space-between; gap:16px; }
  .report-head h1, h2 { margin:0; font-size:16px; }
  .report-head p, .section-head p, .dates-heading p { margin:5px 0 0; font-size:12px; color:color-mix(in oklch,var(--color-base-content) 65%,transparent); }
  .report-actions { display:flex; gap:8px; flex-wrap:wrap; }
  .recalculate { display:inline-flex; align-items:center; gap:7px; }
  .report-state { display:grid; gap:4px; padding:16px; border-radius:10px; background:var(--op-inset); font-size:12px; }
  .refresh-line, .scope-note { font-size:11px; color:color-mix(in oklch,var(--color-base-content) 62%,transparent); }
  .scope-note { margin:0; }
  .totals { display:grid; grid-template-columns:1fr 1fr; gap:12px; }
  .total-card { display:grid; gap:7px; padding:18px 20px; }
  .total-card > span { font-size:12px; font-weight:600; }
  .total-card strong { font-size:28px; letter-spacing:-.03em; font-variant-numeric:tabular-nums; }
  .total-card p { font-size:11px; margin:0; color:color-mix(in oklch,var(--color-base-content) 65%,transparent); }
  .report-card { overflow:hidden; }
  .section-head { padding:18px 20px 10px; }
  .section-head h2, .dates-heading h2 { font-size:14px; }
  .history-toggle { display:flex; align-items:center; gap:7px; font-size:12px; white-space:nowrap; }
  .comparison { display:grid; gap:4px; padding:8px 12px 16px; }
  .comparison-row { display:grid; grid-template-columns:minmax(140px, 1fr) minmax(80px, 2fr) 70px; align-items:center; gap:18px; width:100%; background:transparent; border:0; border-radius:6px; padding:9px 8px; text-align:left; cursor:pointer; font-size:12px; color:inherit; }
  .comparison-row:hover { background:var(--op-inset); }
  .comparison-row:focus-visible { outline:2px solid var(--color-primary); outline-offset:1px; }
  .comparison-row strong { text-align:right; font-variant-numeric:tabular-nums; }
  .comparison-track { display:block; height:18px; border-radius:3px; background:color-mix(in oklch,var(--color-base-content) 5%,transparent); overflow:hidden; }
  .comparison-track > span { display:block; height:100%; border-radius:3px; }
  .dates-heading { margin-top:8px; }
  .category-list { display:grid; grid-template-columns:minmax(0,1fr); gap:8px; }
  .category-card { scroll-margin-top:20px; }
  .category-card > summary { display:flex; align-items:center; gap:10px; padding:16px 20px; cursor:pointer; list-style:none; }
  .category-card > summary::-webkit-details-marker { display:none; }
  .category-card > summary::after { content:"+"; font-size:18px; width:14px; text-align:center; }
  .category-card[open] > summary::after { content:"−"; }
  .category-name { flex:1; font-size:13px; font-weight:600; }
  .category-name small { display:block; margin-top:3px; font-size:10px; font-weight:400; color:color-mix(in oklch,var(--color-base-content) 60%,transparent); }
  .category-card summary strong { font-size:14px; font-variant-numeric:tabular-nums; }
  .category-dot { width:8px; height:8px; border-radius:2px; flex:none; }
  .category-description { margin:0; padding:0 20px 12px; font-size:12px; color:color-mix(in oklch,var(--color-base-content) 65%,transparent); }
  .measurement-errors { font-size:12px; overflow-wrap:anywhere; }
  .measurement-errors summary { cursor:pointer; }
  .refreshing :global(svg) { animation:report-spin .8s linear infinite; }
  @media(max-width:600px) { .report-head { align-items:start; flex-direction:column; } .totals { grid-template-columns:1fr; } .section-head { align-items:start; flex-direction:column; } .comparison-row { grid-template-columns:minmax(90px,1fr) minmax(40px,1fr) 56px; gap:8px; font-size:11px; } .category-card > summary { padding:14px 12px; } .category-description { padding:0 12px 12px; } }
  @media(prefers-reduced-motion:reduce) { .refreshing :global(svg) { animation:none; } }
  @keyframes report-spin { to { transform:rotate(360deg); } }
</style>
