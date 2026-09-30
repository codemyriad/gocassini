<script lang="ts">
  import type { StorageUsageCategory } from "./operator/types";
  import { retentionDayPresets } from "./operator/retention";
  import { formatStorageBytes } from "./operator/storageUsage";
  import { bucketLabel, chartDate, defaultStoragePrecision, storageBuckets, storageDateExtent, storageRange } from "./operator/storageCharts";
  export let category: StorageUsageCategory;
  export let measuredAt: string;
  export let label: string;
  export let color: string;
  const initialExtent = storageDateExtent(category, measuredAt);
  const initialPrecision = defaultStoragePrecision(initialExtent.from, initialExtent.to);
  let range = "all";
  let from = initialExtent.from, to = initialExtent.to;
  let precision = [1, ...retentionDayPresets].includes(initialPrecision) ? String(initialPrecision) : "custom";
  let customPrecision = initialPrecision;
  let selected = -1;
  $: extent = storageDateExtent(category, measuredAt);
  $: window = storageRange(range, extent, from, to);
  $: days = precision === "custom" ? customPrecision : Number(precision);
  $: result = storageBuckets(category, window.from, window.to, days);
  $: maximum = Math.max(0, ...result.buckets.map(b => b.bytes));
  $: rangeBytes = result.buckets.reduce((sum, b) => sum + b.bytes, 0);
  $: active = result.buckets[selected];
  $: if (result) selected = -1;
</script>

<div class="category-chart" style:--chart-color={color}>
  <div class="controls">
    <label>Time range
      <select class="select select-bordered" aria-label={`${label} time range`} bind:value={range}>
        <option value="all">All retained dates</option>
        <option value="7">Last 7 days</option><option value="30">Last 30 days</option>
        <option value="90">Last 90 days</option><option value="365">Last year</option>
        <option value="custom">Custom range</option>
      </select>
    </label>
    <label>Precision
      <select class="select select-bordered" aria-label={`${label} precision`} bind:value={precision}>
        <option value="1">Daily</option>
        {#each retentionDayPresets as preset}<option value={String(preset)}>{preset} days</option>{/each}
        <option value="custom">Custom days</option>
      </select>
    </label>
    {#if precision === "custom"}
      <label>Days per bar<input class="input input-bordered" aria-label={`${label} days per bar`} type="number" min="1" max="9999" step="1" bind:value={customPrecision} /></label>
    {/if}
    {#if range === "custom"}
      <label>From<input class="input input-bordered" aria-label={`${label} from date`} type="date" bind:value={from} /></label>
      <label>Through<input class="input input-bordered" aria-label={`${label} through date`} type="date" bind:value={to} /></label>
    {/if}
  </div>
  {#if category.undated_files > 0}
    <p class="undated">{formatStorageBytes(category.undated_bytes)} in {category.undated_files} file{category.undated_files === 1 ? "" : "s"} without a known lifecycle date. Included in the category total, excluded from the date chart.</p>
  {/if}
  {#if result.error}
    <p class="chart-message" role="alert">{result.error}</p>
  {:else if category.days.length === 0}
    <p class="chart-message">{category.files === 0 ? "No retained files in this category." : "No dated files to plot in this category."}</p>
  {:else}
    <div class="chart-summary"><strong>{formatStorageBytes(rangeBytes)} <span>in selected range</span></strong><span>{days === 1 ? "Daily" : `${days} days per bar`} · UTC dates</span></div>
    {#if rangeBytes === 0}<p class="chart-message">No retained bytes in this date range. Try a wider range.</p>{/if}
    <div class="plot">
      <div class="y-axis" aria-hidden="true"><span>{formatStorageBytes(maximum)}</span><span>{formatStorageBytes(maximum / 2)}</span><span>0 B</span></div>
      <div class="plot-scroll" role="region" aria-label={`${label} usage by date`}>
        <div class="timeline" style:min-width={`${Math.max(240,result.buckets.length*14)}px`}>
        <div class="bars">
          {#each result.buckets as bucket, index (bucket.from)}
            <button type="button" class="bar-target" class:active={selected === index}
              aria-label={`${bucketLabel(bucket)}: ${formatStorageBytes(bucket.bytes)}, ${bucket.files} files`}
              title={`${bucketLabel(bucket)} · ${formatStorageBytes(bucket.bytes)} · ${bucket.files} files`}
              on:focus={() => selected = index} on:mouseenter={() => selected = index} on:click={() => selected = index}>
              <span class="bar" class:empty={bucket.bytes === 0} style:height={`${maximum > 0 ? Math.max(bucket.bytes > 0 ? 1 : 0, bucket.bytes / maximum * 100) : 0}%`}></span>
            </button>
          {/each}
        </div>
        <div class="x-axis"><span>{chartDate(window.from)}</span><span>{chartDate(window.to)}</span></div>
        </div>
      </div>
    </div>
    <p class="bar-detail" aria-live="polite">{#if active}<strong>{bucketLabel(active)}</strong> · {formatStorageBytes(active.bytes)} · {active.files} file{active.files === 1 ? "" : "s"}{:else}Hover, focus or tap a bar for its dates and exact usage.{/if}</p>
    <details class="values"><summary>View chart data</summary>
      <div class="table-scroll"><table><caption>{label} · retained bytes by UTC lifecycle date</caption><thead><tr><th scope="col">Date range</th><th scope="col">Bytes</th><th scope="col">Files</th></tr></thead><tbody>
        {#each result.buckets as bucket}<tr><th scope="row">{bucketLabel(bucket)}</th><td>{bucket.bytes.toLocaleString()}</td><td>{bucket.files.toLocaleString()}</td></tr>{/each}
      </tbody></table></div>
    </details>
  {/if}
</div>

<style>
  .category-chart { padding: 0 20px 20px; }
  .controls { display:flex; flex-wrap:wrap; gap:12px; margin:8px 0 18px; }
  .controls label { display:grid; gap:6px; font-size:12px; font-weight:600; }
  .controls select, .controls input { font-weight:400; font-size:13px; min-height:36px; height:36px; max-width:100%; }
  .controls input[type=number] { width:110px; }
  .chart-summary { display:flex; justify-content:space-between; gap:12px; flex-wrap:wrap; font-size:12px; margin-bottom:16px; }
  .chart-summary strong { font-size:16px; font-variant-numeric:tabular-nums; }
  .chart-summary span, .bar-detail, .x-axis, .y-axis { color:color-mix(in oklch,var(--color-base-content) 65%,transparent); }
  .chart-summary strong span { font-weight:400; font-size:12px; }
  .plot { display:flex; gap:10px; }
  .y-axis { width:58px; flex:none; display:flex; flex-direction:column; justify-content:space-between; font-size:10px; text-align:right; padding-bottom:3px; height:164px; }
  .plot-scroll { flex:1; min-width:0; overflow-x:auto; padding-top:4px; }
  .bars { height:160px; display:flex; align-items:stretch; gap:3px; border-bottom:1px solid var(--op-border, #8993a044); background:repeating-linear-gradient(to top, transparent 0, transparent calc(50% - 1px), color-mix(in oklch,var(--color-base-content) 9%,transparent) calc(50% - 1px), color-mix(in oklch,var(--color-base-content) 9%,transparent) 50%); }
  .bar-target { border:0; background:transparent; padding:0; flex:1; min-width:8px; display:flex; align-items:end; cursor:pointer; border-radius:3px 3px 0 0; }
  .bar { display:block; width:100%; max-width:64px; margin:0 auto; border-radius:3px 3px 0 0; background:var(--chart-color); opacity:.85; min-height:2px; }
  .bar.empty { background:color-mix(in oklch,var(--color-base-content) 22%,transparent); }
  .bar-target.active .bar { opacity:1; filter:brightness(1.15); }
  .bar-target:focus-visible, .plot-scroll:focus-visible { outline:2px solid var(--chart-color); outline-offset:2px; }
  .x-axis { display:flex; justify-content:space-between; margin:7px 0 0; font-size:10px; }
  .bar-detail { min-height:18px; margin:12px 0; font-size:12px; }
  .chart-message, .undated { font-size:12px; padding:12px; background:var(--op-inset); border-radius:8px; margin:12px 0; }
  .values { font-size:12px; }
  .values summary { cursor:pointer; padding:6px 0; }
  .table-scroll { overflow:auto; max-height:280px; }
  table { width:100%; border-collapse:collapse; }
  caption { text-align:left; padding:8px 0; }
  th, td { text-align:right; padding:7px; border-bottom:1px solid color-mix(in oklch,var(--color-base-content) 12%,transparent); }
  th:first-child { text-align:left; }
  @media(max-width:600px) { .category-chart { padding:0 12px 16px; } .controls label { flex:1 1 135px; min-width:0; } .controls select, .controls input { width:100%; } }
</style>
