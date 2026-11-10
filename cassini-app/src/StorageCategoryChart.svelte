<script lang="ts">
  import { formatCount, LL, numberFormat } from "cassini-viewer/i18n";
  import type { StorageUsageCategory } from "./operator/types";
  import { formatStorageBytes } from "./operator/storageUsage";
  import type { RetentionPolicy } from "./operator/retention";
  import { bucketLabel, chartDate, defaultStoragePrecision, retentionCutoff, storageBuckets, storageDateExtent, storageRange } from "./operator/storageCharts";
  export let category: StorageUsageCategory;
  export let measuredAt: string;
  export let label: string;
  export let color: string;
  // Which date the chart's files are dated by: when Cassini's lifecycle put
  // them there (UTC), or, for Nextcloud Files, publication or recording.
  export let dateKind: "lifecycle" | "published" = "lifecycle";
  // The saved policy for these files; null when no cleanup rule applies.
  export let policy: RetentionPolicy | null = null;
  const initialExtent = storageDateExtent(category, measuredAt);
  let range = "all";
  let from = initialExtent.from, to = initialExtent.to;
  let selected = -1;
  let scroller: HTMLDivElement;
  let canScrollEarlier = false, canScrollLater = false;
  function trackScroll(node: HTMLDivElement) {
    scroller = node;
    const update = () => {
      canScrollEarlier = node.scrollLeft > 1;
      canScrollLater = node.scrollLeft + node.clientWidth < node.scrollWidth - 1;
    };
    const observer = new ResizeObserver(update);
    observer.observe(node);
    observer.observe(node.firstElementChild!);
    node.addEventListener("scroll", update);
    update();
    return { destroy() { observer.disconnect(); node.removeEventListener("scroll", update); } };
  }
  function scrollTimeline(direction: number) {
    scroller.scrollBy({ left: direction * scroller.clientWidth * 0.8,
      behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth" });
  }
  $: extent = storageDateExtent(category, measuredAt);
  $: window = storageRange(range, extent, from, to);
  $: days = defaultStoragePrecision(window.from, window.to);
  $: result = storageBuckets(category, window.from, window.to, days);
  $: maximum = result.buckets.reduce((largest, bucket) => Math.max(largest, bucket.bytes), 0);
  $: rangeBytes = result.buckets.reduce((sum, b) => sum + b.bytes, 0);
  $: active = result.buckets[selected];
  $: if (result) selected = -1;
  $: cutoff = retentionCutoff(policy);
  // The bar holding the cutoff date; the marker sits on its right edge.
  $: cutoffIndex = cutoff ? result.buckets.findIndex(bucket => bucket.from <= cutoff! && cutoff! <= bucket.to) : -1;
  $: dates = dateKind === "published" ? $LL.app.operator.storage.chart.dates_published() : $LL.app.operator.storage.chart.dates_lifecycle();
  const count = (n: number): string => numberFormat().format(n);
  $: markerLeft = cutoffIndex >= 0 ? (cutoffIndex + 1) / result.buckets.length : 0;
</script>

<div class="category-chart" style:--chart-color={color}>
  <div class="controls">
    <label>{$LL.app.operator.storage.chart.range_label()}
      <select class="select select-bordered" aria-label={$LL.app.operator.storage.chart.range_select_label({ category: label })} bind:value={range}>
        <option value="all">{$LL.app.operator.storage.chart.range_all()}</option>
        <option value="7">{$LL.app.operator.storage.chart.range_last_days({ count: formatCount(7) })}</option><option value="30">{$LL.app.operator.storage.chart.range_last_days({ count: formatCount(30) })}</option>
        <option value="90">{$LL.app.operator.storage.chart.range_last_days({ count: formatCount(90) })}</option><option value="365">{$LL.app.operator.storage.chart.range_last_year()}</option>
        <option value="custom">{$LL.app.operator.storage.chart.range_custom()}</option>
      </select>
    </label>
    {#if range === "custom"}
      <label>{$LL.app.operator.storage.chart.from()}<input class="input input-bordered" aria-label={$LL.app.operator.storage.chart.from_label({ category: label })} type="date" bind:value={from} /></label>
      <label>{$LL.app.operator.storage.chart.through()}<input class="input input-bordered" aria-label={$LL.app.operator.storage.chart.through_label({ category: label })} type="date" bind:value={to} /></label>
    {/if}
  </div>
  {#if category.undated_files > 0}
    <p class="undated">{category.undated_files === 1 ? $LL.app.operator.storage.chart.undated_one({ size: formatStorageBytes(category.undated_bytes) }) : $LL.app.operator.storage.chart.undated_other({ size: formatStorageBytes(category.undated_bytes), count: count(category.undated_files) })}</p>
  {/if}
  {#if result.error}
    <p class="chart-message" role="alert">{result.error}</p>
  {:else if category.days.length === 0}
    <p class="chart-message">{category.files === 0 ? $LL.app.operator.storage.chart.empty_no_files() : $LL.app.operator.storage.chart.empty_no_dated_files()}</p>
  {:else}
    <div class="chart-summary"><strong>{formatStorageBytes(rangeBytes)} <span>{$LL.app.operator.storage.chart.in_selected_range()}</span></strong><span>{days === 1 ? $LL.app.operator.storage.chart.precision_daily() : $LL.app.operator.storage.chart.precision_days({ count: formatCount(days) })} · {dates}</span></div>
    {#if rangeBytes === 0}<p class="chart-message">{$LL.app.operator.storage.chart.empty_range()}</p>{/if}
    {#if canScrollEarlier || canScrollLater}
      <div class="timeline-navigation">
        <button class="op-btn" type="button" aria-label={$LL.app.operator.storage.chart.scroll_earlier_label({ category: label })} disabled={!canScrollEarlier} on:click={() => scrollTimeline(-1)}>← {$LL.app.operator.storage.chart.scroll_earlier()}</button>
        <span>{$LL.app.operator.storage.chart.scroll_hint({ count: count(result.buckets.length) })}</span>
        <button class="op-btn" type="button" aria-label={$LL.app.operator.storage.chart.scroll_later_label({ category: label })} disabled={!canScrollLater} on:click={() => scrollTimeline(1)}>{$LL.app.operator.storage.chart.scroll_later()} →</button>
      </div>
    {/if}
    <div class="plot" class:marked={cutoffIndex >= 0}>
      <div class="y-axis" aria-hidden="true"><span>{formatStorageBytes(maximum)}</span><span>{formatStorageBytes(maximum / 2)}</span><span>0 B</span></div>
      <!-- svelte-ignore a11y_no_noninteractive_tabindex (The scroll region needs keyboard focus for native arrow-key scrolling.) -->
      <div class="plot-scroll" use:trackScroll role="region" tabindex="0" aria-label={$LL.app.operator.storage.chart.plot_label({ category: label })}>
        <div class="chart-timeline" style:min-width={`${Math.max(240,result.buckets.length*14)}px`}>
        {#if cutoffIndex >= 0}
          <div class="cutoff-marker" class:flip={markerLeft < 0.3} aria-hidden="true"
            style:left={`calc(${cutoffIndex + 1} * (100% + 3px) / ${result.buckets.length} - 1.5px)`}>
            <span>{markerLeft < 0.3 ? `← ${$LL.app.operator.storage.chart.cutoff_marker()}` : `${$LL.app.operator.storage.chart.cutoff_marker()} ←`}</span>
          </div>
        {/if}
        <div class="bars">
          {#each result.buckets as bucket, index (bucket.from)}
            <button type="button" class="bar-target" class:active={selected === index} class:removed={!!cutoff && bucket.from <= cutoff}
              aria-label={$LL.app.operator.storage.chart.bar_label({ range: bucketLabel(bucket), size: formatStorageBytes(bucket.bytes), files: bucket.files === 1 ? $LL.app.operator.storage.chart.files_one() : $LL.app.operator.storage.chart.files_other({ count: count(bucket.files) }) })}
              title={`${bucketLabel(bucket)} · ${formatStorageBytes(bucket.bytes)} · ${bucket.files === 1 ? $LL.app.operator.storage.chart.files_one() : $LL.app.operator.storage.chart.files_other({ count: count(bucket.files) })}`}
              on:focus={() => selected = index} on:mouseenter={() => selected = index} on:click={() => selected = index}>
              <span class="bar" class:empty={bucket.bytes === 0} style:height={`${maximum > 0 ? Math.max(bucket.bytes > 0 ? 1 : 0, bucket.bytes / maximum * 100) : 0}%`}></span>
            </button>
          {/each}
        </div>
        <div class="x-axis"><span>{chartDate(window.from)}</span><span>{chartDate(window.to)}</span></div>
        </div>
      </div>
    </div>
    {#if cutoff && policy?.count}
      <p class="cutoff-note">{$LL.app.operator.storage.chart.cutoff_note({ date: chartDate(cutoff), days: formatCount(policy.count) })}</p>
    {/if}
    <p class="bar-detail" aria-live="polite">{#if active}<strong>{bucketLabel(active)}</strong> · {formatStorageBytes(active.bytes)} · {active.files === 1 ? $LL.app.operator.storage.chart.files_one() : $LL.app.operator.storage.chart.files_other({ count: count(active.files) })}{:else}{$LL.app.operator.storage.chart.bar_hint()}{/if}</p>
    <details class="values"><summary>{$LL.app.operator.storage.chart.data_summary()}</summary>
      <div class="table-scroll"><table><caption>{dateKind === "published" ? $LL.app.operator.storage.chart.data_caption_published({ category: label }) : $LL.app.operator.storage.chart.data_caption_lifecycle({ category: label })}</caption><thead><tr><th scope="col">{$LL.app.operator.storage.chart.column_range()}</th><th scope="col">{$LL.app.operator.storage.chart.column_bytes()}</th><th scope="col">{$LL.app.operator.storage.chart.column_files()}</th></tr></thead><tbody>
        {#each result.buckets as bucket (bucket.from)}<tr><th scope="row">{bucketLabel(bucket)}</th><td>{count(bucket.bytes)}</td><td>{count(bucket.files)}</td></tr>{/each}
      </tbody></table></div>
    </details>
  {/if}
</div>

<style>
  .category-chart { padding: 0 20px 20px; }
  .controls { display:flex; flex-wrap:wrap; gap:12px; margin:8px 0 18px; }
  .controls label { display:grid; gap:6px; font-size:12px; font-weight:600; }
  .controls select, .controls input { font-weight:400; font-size:13px; min-height:36px; height:36px; max-width:100%; }
  .chart-summary { display:flex; justify-content:space-between; gap:12px; flex-wrap:wrap; font-size:12px; margin-bottom:16px; }
  .chart-summary strong { font-size:16px; font-variant-numeric:tabular-nums; }
  .chart-summary span, .bar-detail, .cutoff-note, .x-axis, .y-axis { color:color-mix(in oklch,var(--color-base-content) 65%,transparent); }
  .chart-summary strong span { font-weight:400; font-size:12px; }
  .plot { display:flex; gap:10px; }
  .timeline-navigation { display:flex; align-items:center; justify-content:space-between; gap:10px; margin-bottom:12px; font-size:11px; }
  .timeline-navigation span { text-align:center; }
  .timeline-navigation button { flex:none; }
  .y-axis { width:58px; flex:none; display:flex; flex-direction:column; justify-content:space-between; font-size:10px; text-align:right; padding-bottom:3px; height:164px; }
  .plot-scroll { flex:1; min-width:0; overflow-x:auto; padding-top:4px; }
  .chart-timeline { display:block; width:100%; position:relative; }
  .plot.marked .chart-timeline { padding-top:18px; }
  .plot.marked .y-axis { margin-top:18px; }
  .cutoff-marker { position:absolute; top:0; height:178px; border-left:1.5px dashed color-mix(in oklch,var(--color-base-content) 55%,transparent); pointer-events:none; }
  .cutoff-marker span { position:absolute; top:0; right:4px; white-space:nowrap; font-size:10px; line-height:14px; color:color-mix(in oklch,var(--color-base-content) 70%,transparent); }
  .cutoff-marker.flip span { right:auto; left:4px; }
  .bars { height:160px; display:flex; align-items:stretch; gap:3px; border-bottom:1px solid var(--op-border, #8993a044); background:repeating-linear-gradient(to top, transparent 0, transparent calc(50% - 1px), color-mix(in oklch,var(--color-base-content) 9%,transparent) calc(50% - 1px), color-mix(in oklch,var(--color-base-content) 9%,transparent) 50%); }
  .bar-target { border:0; background:transparent; padding:0; flex:1; min-width:8px; display:flex; align-items:end; cursor:pointer; border-radius:3px 3px 0 0; }
  .bar { display:block; width:100%; max-width:64px; margin:0 auto; border-radius:3px 3px 0 0; background:var(--chart-color); opacity:.85; min-height:2px; }
  .bar.empty { background:color-mix(in oklch,var(--color-base-content) 22%,transparent); }
  .bar-target.removed .bar { opacity:.35; }
  .bar-target.active .bar { opacity:1; filter:brightness(1.15); }
  .bar-target:focus-visible, .plot-scroll:focus-visible { outline:2px solid var(--chart-color); outline-offset:2px; }
  .x-axis { display:flex; justify-content:space-between; margin:7px 0 0; font-size:10px; }
  .bar-detail { min-height:18px; margin:12px 0; font-size:12px; }
  .cutoff-note { margin:12px 0 0; font-size:12px; }
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
