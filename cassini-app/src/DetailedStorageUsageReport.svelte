<script lang="ts">
  import { dateFormat, LL } from "cassini-viewer/i18n";
  import { onDestroy, onMount } from "svelte";
  import { HardDrive, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import { formatStorageBytes, storageUsageTotal } from "./operator/storageUsage";
  import type { OperatorClient } from "./operator/client";
  import type { DetailedStorageUsage } from "./operator/types";

  import StorageCategoryChart from "./StorageCategoryChart.svelte";
  import { categoryPresentation, chartRetentionPolicy, displayStorageCategories, historyIDs } from "./operator/storageCharts";
  import { onRetentionChanged } from "./operator/retentionSignal";
  import type { RetentionSettings } from "./operator/retention";
  // Previous attempts is one row that opens onto its four kinds. It starts
  // open when the saved policy sets each kind separately.
  let splitHistory = false;
  let savedFine: boolean | null = null;
  // Saved retention drives the cleanup markers on each date chart.
  let retention: RetentionSettings | null = null;
  function applyRetention(settings: RetentionSettings) {
    retention = settings;
    const fine = settings.history.mode === "fine";
    if (fine !== savedFine) { savedFine = fine; splitHistory = fine; }
  }
  const stopRetention = onRetentionChanged(applyRetention);
  // The retention editor normally publishes its settings when it loads; read
  // them once here if it has not, so the markers never depend on the editor.
  async function loadRetention() {
    if (retention || !operatorClient) return;
    try {
      const settings = await operatorClient.getRetention();
      if (!retention) applyRetention(settings);
    } catch { /* Markers are optional; the report stays usable without them. */ }
  }
  onDestroy(stopRetention);
  let expanded: Record<string, boolean> = {};
  $: publishedCategories = usage?.published_category ? [usage.published_category] : [];
  $: categories = [...(usage?.categories.length ? displayStorageCategories(usage.categories, splitHistory) : []), ...publishedCategories];
  $: rows = [...(usage?.categories.length ? displayStorageCategories(usage.categories, false) : []), ...publishedCategories];
  $: kinds = usage ? usage.categories.filter(category => historyIDs.includes(category.id)) : [];
  $: localTotal = usage ? usage.categories.reduce((sum, category) => sum + category.bytes, 0) : 0;
  $: partial = !!usage && (!!usage.category_error || usage.directories.some(directory => !!directory.error));
  $: largest = Math.max(0, ...rows.map(category => category.bytes));
  function presentation(id: string) { return categoryPresentation[id] ?? { label: id, description: $LL.app.operator.storage.report.unknown_category_description(), color: "#7d8490" }; }
  function openCategory(id: string) {
    expanded = { ...expanded, [id]: true };
    requestAnimationFrame(() => document.getElementById(`category-${id}`)?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }
  export let operatorClient: OperatorClient | null = null;

  let usage: DetailedStorageUsage | null = null;
  let loading = true;
  let recalculating = false;
  let loadError = "";

  $: publishedTotal = usage ? storageUsageTotal(usage.published) : null;
  $: publishedPartial = !!usage && (publishedTotal === null || !!usage.published_category_error);
  // Show the cached state immediately, then rebuild without making the first
  // paint wait for a recursive filesystem and WebDAV scan.
  onMount(() => void loadThenRebuild());

  async function loadThenRebuild() {
    await load();
    void loadRetention();
    void load(true);
  }

  async function load(recalculate = false): Promise<void> {
    if (!operatorClient) {
      loading = false;
      loadError = $LL.app.operator.storage.report.no_operator();
      return;
    }
    loading = true;
    recalculating = recalculate;
    loadError = "";
    try {
      usage = recalculate
        ? await operatorClient.recalculateDetailedStorageUsage()
        : await operatorClient.getDetailedStorageUsage();
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
    return Number.isNaN(date.getTime()) ? "" : dateFormat({ dateStyle: "medium", timeStyle: "short" }).format(date);
  }
</script>

<section class="report" aria-labelledby="detailed-storage-title">
  <header class="report-head">
    <div class="op-panel-title"><HardDrive size={18} aria-hidden="true" /><h1 id="detailed-storage-title">{$LL.app.operator.storage.report.title()}</h1></div>
    <div class="report-actions">
      {#if usage?.measured_at && measuredAt(usage.measured_at)}<span class="measured">{$LL.app.operator.storage.report.last_calculated({ when: measuredAt(usage.measured_at) })}</span>{/if}
      <button class="op-btn recalculate" class:refreshing={recalculating} type="button" on:click={() => void load(true)} disabled={loading}>
        <RefreshCw size={15} aria-hidden="true" />
        {recalculating ? $LL.app.operator.storage.report.calculating() : usage?.measured_at ? $LL.app.operator.storage.report.recalculate() : $LL.app.operator.storage.report.calculate()}
      </button>
    </div>
  </header>

  {#if loadError && !usage}
    <div class="alert alert-error text-sm"><TriangleAlert size={16} aria-hidden="true" />{$LL.app.operator.storage.report.load_failed({ message: loadError })}</div>
  {:else if loading && !usage}
    <div class="report-state">{$LL.app.operator.storage.report.loading()}</div>
  {:else if usage && usage.measured_at === ""}
    <div class="report-state"><strong>{$LL.app.operator.storage.report.never_title()}</strong><span>{$LL.app.operator.storage.report.never_line()}</span></div>
  {:else if usage}
    {#if loadError}<div class="alert alert-error text-sm"><TriangleAlert size={16} aria-hidden="true" />{$LL.app.operator.storage.report.recalculate_failed({ message: loadError })}</div>{/if}
    <div class="totals">
      <section class="total-card op-tint"><span>{$LL.app.operator.storage.report.local_heading()}</span><strong>{partial ? $LL.app.operator.storage.report.partial_result() : usage.categories.length === 0 ? $LL.app.operator.storage.report.not_available() : formatStorageBytes(localTotal)}</strong><p>{$LL.app.operator.storage.report.local_description()}</p></section>
      <section class="total-card op-tint"><span>{$LL.app.operator.storage.report.published_heading()}</span><strong>{publishedTotal === null ? $LL.app.operator.storage.report.not_measured() : formatStorageBytes(publishedTotal)}</strong><p>{$LL.app.operator.storage.report.published_description()}</p></section>
    </div>
    {#if partial}<p class="alert alert-warning" role="status">{$LL.app.operator.storage.report.partial_local()}</p>{/if}
    {#if publishedPartial}<p class="alert alert-warning" role="status">{$LL.app.operator.storage.report.partial_published()}</p>{/if}
    {#if !usage.published_category}<p class="report-state">{$LL.app.operator.storage.report.published_dates_missing()}</p>{/if}
    {#if publishedPartial || partial}
      <details class="measurement-errors"><summary>{$LL.app.operator.storage.report.errors_summary()}</summary>
        {#if usage.category_error}<p>{usage.category_error}</p>{/if}
        {#if usage.published_category_error}<p>{usage.published_category_error}</p>{/if}
        {#each [...usage.published, ...usage.directories].filter(source => source.error) as source, index (index)}<p>{source.label}: {source.error}</p>{/each}
      </details>
    {/if}
    {#if usage.categories.length === 0}
      <p class="report-state">{$LL.app.operator.storage.report.categories_missing()}</p>
    {/if}
    {#if categories.length > 0}
      <div class="dates-heading"><h2 id="category-comparison-title">{$LL.app.operator.storage.report.by_type()}</h2></div>
      <section class="report-card op-tint" aria-labelledby="category-comparison-title">
        <div class="comparison">
          {#each rows as category (category.id)}
            {@const info = presentation(category.id)}
            {#if category.id === "history"}
              <button type="button" class="comparison-row" aria-expanded={splitHistory} on:click={() => splitHistory = !splitHistory} aria-label={$LL.app.operator.storage.report.row_label({ category: info.label, size: formatStorageBytes(category.bytes) })}>
                <span class="category-label">{info.label} <span class="expand-sign" aria-hidden="true">{splitHistory ? "−" : "+"}</span></span>
                <span class="comparison-track" aria-hidden="true"><span style:width={`${largest > 0 ? category.bytes / largest * 100 : 0}%`} style:background={info.color}></span></span>
                <strong>{formatStorageBytes(category.bytes)}</strong>
              </button>
              {#if splitHistory}
                {#each kinds as kind (kind.id)}
                  {@const kindInfo = presentation(kind.id)}
                  <button type="button" class="comparison-row kind-row" on:click={() => openCategory(kind.id)} aria-label={$LL.app.operator.storage.report.explore_label({ category: kindInfo.label, size: formatStorageBytes(kind.bytes) })}>
                    <span class="category-label">{kindInfo.label}</span>
                    <span class="comparison-track" aria-hidden="true"><span style:width={`${largest > 0 ? kind.bytes / largest * 100 : 0}%`} style:background={kindInfo.color}></span></span>
                    <strong>{formatStorageBytes(kind.bytes)}</strong>
                  </button>
                {/each}
              {/if}
            {:else}
              <button type="button" class="comparison-row" on:click={() => openCategory(category.id)} aria-label={$LL.app.operator.storage.report.explore_label({ category: info.label, size: formatStorageBytes(category.bytes) })}>
                <span class="category-label">{info.label}</span>
                <span class="comparison-track" aria-hidden="true"><span style:width={`${largest > 0 ? category.bytes / largest * 100 : 0}%`} style:background={info.color}></span></span>
                <strong>{category.id === "published" && publishedPartial ? `${$LL.app.operator.storage.report.partial_prefix()} · ` : ""}{formatStorageBytes(category.bytes)}</strong>
              </button>
            {/if}
          {/each}
        </div>
      </section>
      <div class="dates-heading"><h2>{$LL.app.operator.storage.report.by_date()}</h2></div>
      <div class="category-list">
        {#each categories as category (category.id)}
          {@const info = presentation(category.id)}
          <details class="category-card op-tint" id={`category-${category.id}`} bind:open={expanded[category.id]}>
            <summary><span class="category-dot" style:background={info.color}></span><span class="category-name">{info.label}<small>{$LL.app.operator.storage.chart.files({ count: category.files })}</small></span><strong>{formatStorageBytes(category.bytes)}</strong></summary>
            <p class="category-description">{info.description}</p>
            {#if category.id === "published" && publishedPartial}<p class="category-description" role="status">{$LL.app.operator.storage.report.partial_category()}</p>{/if}
            <StorageCategoryChart {category} measuredAt={usage.measured_at} label={info.label} color={info.color} dateKind={category.id === "published" ? "published" : "lifecycle"} policy={chartRetentionPolicy(category.id, retention)} />
          </details>
        {/each}
      </div>
    {/if}
  {/if}
</section>

<style>
  .report { display:grid; grid-template-columns:minmax(0,1fr); min-width:0; gap:14px; padding:8px 0 28px; }
  .report-head { display:flex; align-items:center; justify-content:space-between; gap:16px; }
  .report-head h1, h2 { margin:0; font-size:16px; }
  .report-actions { display:flex; align-items:center; gap:10px; flex-wrap:wrap; }
  .measured { font-size:11px; color:color-mix(in oklch,var(--color-base-content) 62%,transparent); }
  .recalculate { display:inline-flex; align-items:center; gap:7px; }
  .report-state { display:grid; gap:4px; padding:16px; border-radius:10px; background:var(--op-inset); font-size:12px; }
  .totals { display:grid; grid-template-columns:1fr 1fr; gap:12px; }
  .total-card { display:grid; gap:7px; padding:18px 20px; }
  .total-card > span { font-size:12px; font-weight:600; }
  .total-card strong { font-size:28px; letter-spacing:-.03em; font-variant-numeric:tabular-nums; }
  .total-card p { font-size:11px; margin:0; color:color-mix(in oklch,var(--color-base-content) 65%,transparent); }
  .report-card { overflow:hidden; }
  .dates-heading h2 { font-size:14px; }
  .expand-sign { display:inline-block; width:14px; text-align:center; font-weight:600; color:color-mix(in oklch,var(--color-base-content) 62%,transparent); }
  .kind-row .category-label { padding-left:14px; }
  .comparison { display:grid; gap:4px; padding:12px 12px 16px; }
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
  @media(max-width:600px) { .report-head { align-items:start; flex-direction:column; } .totals { grid-template-columns:1fr; } .comparison-row { grid-template-columns:minmax(90px,1fr) minmax(40px,1fr) 56px; gap:8px; font-size:11px; } .category-card > summary { padding:14px 12px; } .category-description { padding:0 12px 12px; } }
  @media(prefers-reduced-motion:reduce) { .refreshing :global(svg) { animation:none; } }
  @keyframes report-spin { to { transform:rotate(360deg); } }
</style>
