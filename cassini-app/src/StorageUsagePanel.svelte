<script lang="ts">
  import { onMount, tick } from "svelte";
  import { HardDrive, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import { OperatorClient } from "./operator/client";
  import {
    formatStorageBytes,
    formatStorageDuration,
    storageUsageTotal,
    summarizeStorageLoads,
    type StorageLoadSample,
    type StorageLoadSummary,
  } from "./operator/storageUsage";
  import type { StorageUsage } from "./operator/types";
  import DetailedStorageUsageReport from "./DetailedStorageUsageReport.svelte";

  const SAMPLE_STORAGE_KEY = "cassini.storage-usage-performance.v1";
  const SAMPLE_LIMIT = 20;
  const BENCHMARK_RUNS = 5;

  // Settings.svelte owns configuration and hands its one client to every panel.
  // This panel only reads current folder sizes; refresh is explicit because a
  // recursive WebDAV listing can be expensive on a large recordings archive.
  export let operatorClient: OperatorClient | null = null;

  let usage: StorageUsage | null = null;
  let loading = true;
  let recalculating = false;
  let loadError = "";
  let latestSample: StorageLoadSample | null = null;
  let samples: StorageLoadSample[] = [];
  let benchmarking = false;
  let benchmarkProgress = 0;
  let benchmarkSummary: StorageLoadSummary | null = null;

  $: total = usage ? storageUsageTotal(usage.sources) : null;
  $: nextcloudSource = usage?.sources.find((source) => source.requests > 0) ?? null;
  $: sessionSummary = summarizeStorageLoads(samples.filter((sample) => sample.operation === "lookup"));

  onMount(() => {
    samples = restoreSamples();
    void load();
  });

  async function load(recalculate = false): Promise<StorageLoadSample | null> {
    if (!operatorClient) {
      loading = false;
      loadError = "Cassini’s operator connection is not available.";
      return null;
    }
    const started = now();
    loading = true;
    recalculating = recalculate;
    loadError = "";
    try {
      const nextUsage = recalculate
        ? await operatorClient.recalculateStorageUsage()
        : await operatorClient.getStorageUsage();
      const requestFinished = now();
      usage = nextUsage;
      loading = false;
      await nextPaint();
      const finished = now();
      const sample: StorageLoadSample = {
        operation: recalculate ? "recalculate" : "lookup",
        measured_at: nextUsage.measured_at,
        end_to_end_ms: finished - started,
        request_ms: requestFinished - started,
        render_ms: finished - requestFinished,
        operator_ms: nextUsage.duration_ms,
      };
      if (nextUsage.measured_at !== "") {
        latestSample = sample;
        samples = [...samples, sample].slice(-SAMPLE_LIMIT);
        saveSamples(samples);
      }
      recalculating = false;
      return sample;
    } catch (error) {
      loadError = error instanceof Error ? error.message : String(error);
      loading = false;
      recalculating = false;
      return null;
    }
  }

  async function runBenchmark() {
    benchmarking = true;
    benchmarkProgress = 0;
    benchmarkSummary = null;
    const run: StorageLoadSample[] = [];
    for (let index = 0; index < BENCHMARK_RUNS; index += 1) {
      benchmarkProgress = index + 1;
      const sample = await load();
      if (!sample) break;
      run.push(sample);
    }
    benchmarkSummary = summarizeStorageLoads(run);
    benchmarking = false;
  }

  function now(): number {
    return globalThis.performance?.now?.() ?? Date.now();
  }

  async function nextPaint(): Promise<void> {
    await tick();
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
    });
  }

  function restoreSamples(): StorageLoadSample[] {
    try {
      const value = JSON.parse(sessionStorage.getItem(SAMPLE_STORAGE_KEY) ?? "[]");
      if (!Array.isArray(value)) return [];
      return value.filter(isStorageLoadSample).slice(-SAMPLE_LIMIT);
    } catch {
      return [];
    }
  }

  function saveSamples(value: StorageLoadSample[]) {
    try {
      sessionStorage.setItem(SAMPLE_STORAGE_KEY, JSON.stringify(value));
    } catch {
      // The measurement remains visible even when browser storage is disabled.
    }
  }

  function isStorageLoadSample(value: unknown): value is StorageLoadSample {
    if (value == null || typeof value !== "object") return false;
    const row = value as Record<string, unknown>;
    return typeof row.measured_at === "string"
      && (row.operation === "lookup" || row.operation === "recalculate")
      && typeof row.end_to_end_ms === "number" && Number.isFinite(row.end_to_end_ms)
      && typeof row.request_ms === "number" && Number.isFinite(row.request_ms)
      && typeof row.render_ms === "number" && Number.isFinite(row.render_ms)
      && typeof row.operator_ms === "number" && Number.isFinite(row.operator_ms);
  }

  function measuredAt(value: string): string {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "just now" : date.toLocaleString();
  }
</script>

<header class="op-panel-head">
  <div>
    <div class="op-panel-title">
      <HardDrive size={18} aria-hidden="true" />
      <h1>Storage</h1>
    </div>
    <p>Folder sizes for Cassini’s recordings and build artifacts.</p>
  </div>
  <div class="op-panel-actions">
    <button class="op-btn" type="button" on:click={runBenchmark} disabled={loading || benchmarking || !usage?.measured_at}>
      {benchmarking ? `Benchmark ${benchmarkProgress}/${BENCHMARK_RUNS}` : "Run benchmark"}
    </button>
    <button class="op-btn recalculate" class:refreshing={recalculating} type="button" on:click={() => void load(true)} disabled={loading || benchmarking}>
      <RefreshCw size={16} aria-hidden="true" />
      {recalculating ? "Recalculating…" : "Recalculate"}
    </button>
  </div>
</header>

{#if loadError && usage}
  <section class="alert alert-error text-sm" aria-live="polite">
    <TriangleAlert size={16} aria-hidden="true" />
    Couldn’t recalculate storage: {loadError}
  </section>
{/if}

{#if loading && !usage}
  <section class="op-tint storage-state" aria-live="polite">Loading the storage index…</section>
{:else if loadError && !usage}
  <section class="alert alert-error text-sm" aria-live="polite">
    <TriangleAlert size={16} aria-hidden="true" />
    Couldn’t measure storage: {loadError}
  </section>
{:else if usage && usage.measured_at === ""}
  <section class="op-tint storage-state storage-empty" aria-live="polite">
    <h2>Storage usage has not been calculated yet</h2>
    <p>Last refreshed: Never. Use Recalculate to scan the recording folders and build the in-memory index.</p>
  </section>
{:else if usage}
  <section class="storage-card op-tint" aria-label="Cassini storage usage">
    <div class="storage-summary">
      <div>
        <p class="storage-overline">{total === null ? "Partial result" : "Reported folders total"}</p>
        {#if total === null}
          <strong>Some folders could not be measured</strong>
        {:else}
          <strong>{formatStorageBytes(total)}</strong>
        {/if}
      </div>
      <div class="storage-measurement">
        <p class="storage-measured">Last refreshed {measuredAt(usage.measured_at)}</p>
        {#if latestSample}
          <p>
            {latestSample.operation === "recalculate" ? "Recalculated" : "Index loaded"} in {formatStorageDuration(latestSample.end_to_end_ms)}
            {#if latestSample.operation === "recalculate" && nextcloudSource}
              · Nextcloud Files {formatStorageDuration(nextcloudSource.duration_ms)}
            {/if}
            · UI {formatStorageDuration(latestSample.render_ms)}
          </p>
        {/if}
      </div>
    </div>

    <div class="storage-rows">
      {#each usage.sources as source (source.id)}
        <article class="storage-row">
          <div>
            <h2>{source.label}</h2>
            <p>{source.location}</p>
          </div>
          <div class="storage-value">
            {#if source.error}
              <span class="storage-error" title={source.error}>Couldn’t measure</span>
            {:else}
              <strong>{formatStorageBytes(source.bytes)}</strong>
            {/if}
          </div>
        </article>
      {/each}
    </div>
  </section>

  <p class="storage-note">
    Sizes are logical file bytes in each folder. Raw audio and retained video share the working archive.
  </p>

  {#if latestSample}
    <details class="storage-benchmark op-tint">
      <summary>Measurement details</summary>
      <div class="benchmark-body">
        <p>
          Run benchmark measures in-memory dashboard lookups. Recalculate performs the filesystem and
          Nextcloud scan. Results stay in this browser tab.
        </p>
        <dl>
          <div>
            <dt>{latestSample.operation === "recalculate" ? "Recalculation end to end" : "Index lookup end to end"}</dt>
            <dd>{formatStorageDuration(latestSample.end_to_end_ms)}</dd>
          </div>
          <div><dt>Request and response</dt><dd>{formatStorageDuration(latestSample.request_ms)}</dd></div>
          <div><dt>Last operator scan</dt><dd>{formatStorageDuration(latestSample.operator_ms)}</dd></div>
          {#each usage.sources as source (source.id)}
            <div>
              <dt>{source.label}</dt>
              <dd>
                {formatStorageDuration(source.duration_ms)}
                {#if source.requests > 0}
                  · {source.requests} PROPFINDs · {source.files} files · {source.collections} folders
                {:else}
                  · {source.files} files · {source.collections} folders
                {/if}
              </dd>
            </div>
          {/each}
          <div><dt>UI update</dt><dd>{formatStorageDuration(latestSample.render_ms)}</dd></div>
        </dl>
        {#if benchmarkSummary}
          <p class="benchmark-result">
            Five-run benchmark: median {formatStorageDuration(benchmarkSummary.median_ms)},
            min {formatStorageDuration(benchmarkSummary.min_ms)},
            max {formatStorageDuration(benchmarkSummary.max_ms)}.
          </p>
        {/if}
        {#if sessionSummary}
          <p class="benchmark-session">
            This session: {sessionSummary.count} index load{sessionSummary.count === 1 ? "" : "s"},
            median {formatStorageDuration(sessionSummary.median_ms)},
            slowest {formatStorageDuration(sessionSummary.max_ms)}.
          </p>
        {/if}
      </div>
    </details>
  {/if}
{/if}

<DetailedStorageUsageReport {operatorClient} />

<style>
  .storage-state { padding: 16px; font-size: 13px; color: color-mix(in oklch, var(--color-base-content) 68%, transparent); }
  .storage-empty h2 { margin: 0; font-size: 14px; color: var(--color-base-content); }
  .storage-empty p { margin: 5px 0 0; }
  .recalculate { display: inline-flex; align-items: center; gap: 7px; }
  .recalculate :global(svg) { width: 15px; height: 15px; }
  .storage-card { overflow: hidden; }
  .storage-summary { display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: 12px; padding: 18px; }
  .storage-overline { margin: 0 0 3px; font-size: 10px; font-weight: 650; letter-spacing: 0.07em; text-transform: uppercase; color: color-mix(in oklch, var(--color-base-content) 55%, transparent); }
  .storage-summary strong { font-size: 24px; line-height: 1.2; letter-spacing: -0.02em; }
  .storage-measurement { text-align: right; }
  .storage-measurement p { margin: 0; font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 60%, transparent); }
  .storage-measurement p + p { margin-top: 4px; color: color-mix(in oklch, var(--color-base-content) 78%, transparent); }
  .storage-rows { border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .storage-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px 18px; }
  .storage-row + .storage-row { border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .storage-row h2 { margin: 0; font-size: 13px; font-weight: 600; }
  .storage-row p { margin: 3px 0 0; font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 60%, transparent); }
  .storage-value { flex: none; text-align: right; }
  .storage-value strong { font-size: 15px; font-variant-numeric: tabular-nums; }
  .storage-error { font-size: 12px; font-weight: 600; color: var(--color-error); }
  .storage-note { margin: -4px 0 0; font-size: 12px; line-height: 1.5; color: color-mix(in oklch, var(--color-base-content) 60%, transparent); }
  .storage-benchmark { padding: 0 16px; }
  .storage-benchmark summary { padding: 13px 0; cursor: pointer; font-size: 12px; font-weight: 600; }
  .benchmark-body { padding: 0 0 16px; }
  .benchmark-body > p { margin: 0 0 12px; font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 60%, transparent); }
  .benchmark-body dl { margin: 0; }
  .benchmark-body dl div { display: flex; justify-content: space-between; gap: 16px; padding: 7px 0; border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .benchmark-body dt, .benchmark-body dd { margin: 0; font-size: 12px; }
  .benchmark-body dd { text-align: right; font-variant-numeric: tabular-nums; }
  .benchmark-body .benchmark-result { margin: 14px 0 0; font-weight: 600; color: var(--color-base-content); }
  .benchmark-body .benchmark-session { margin: 6px 0 0; }
  .refreshing :global(svg) { animation: storage-spin 0.8s linear infinite; }
  @media (max-width: 600px) {
    .storage-measurement { width: 100%; text-align: left; }
    .benchmark-body dl div { display: block; }
    .benchmark-body dd { margin-top: 2px; text-align: left; }
  }
  @keyframes storage-spin { to { transform: rotate(360deg); } }
</style>
