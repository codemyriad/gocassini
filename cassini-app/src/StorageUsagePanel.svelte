<script lang="ts">
  import { onMount } from "svelte";
  import { HardDrive, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import { OperatorClient } from "./operator/client";
  import { formatStorageBytes, storageUsageTotal } from "./operator/storageUsage";
  import type { StorageUsage } from "./operator/types";
  import NextcloudStorageUsageReport from "./NextcloudStorageUsageReport.svelte";
  import ArtifactStorageUsageReport from "./ArtifactStorageUsageReport.svelte";

  // Settings.svelte owns configuration and hands its one client to every panel.
  // This panel only reads current folder sizes; refresh is explicit because a
  // recursive WebDAV listing can be expensive on a large recordings archive.
  export let operatorClient: OperatorClient | null = null;

  let usage: StorageUsage | null = null;
  let loading = true;
  let recalculating = false;
  let loadError = "";

  $: total = usage ? storageUsageTotal(usage.sources) : null;

  onMount(() => {
    void load();
  });

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
        ? await operatorClient.recalculateStorageUsage()
        : await operatorClient.getStorageUsage();
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

<header class="op-panel-head">
  <div>
    <div class="op-panel-title">
      <HardDrive size={18} aria-hidden="true" />
      <h1>Storage</h1>
    </div>
    <p>Folder sizes for Cassini’s recordings and build artifacts.</p>
  </div>
  <div class="op-panel-actions">
    <button class="op-btn recalculate" class:refreshing={recalculating} type="button" on:click={() => void load(true)} disabled={loading}>
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
      <p class="storage-measured">Last refreshed {measuredAt(usage.measured_at)}</p>
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
{/if}

<NextcloudStorageUsageReport {operatorClient} />
<ArtifactStorageUsageReport {operatorClient} />

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
  .storage-measured { margin: 0; font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 60%, transparent); }
  .storage-rows { border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .storage-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px 18px; }
  .storage-row + .storage-row { border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .storage-row h2 { margin: 0; font-size: 13px; font-weight: 600; }
  .storage-row p { margin: 3px 0 0; font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 60%, transparent); }
  .storage-value { flex: none; text-align: right; }
  .storage-value strong { font-size: 15px; font-variant-numeric: tabular-nums; }
  .storage-error { font-size: 12px; font-weight: 600; color: var(--color-error); }
  .storage-note { margin: -4px 0 0; font-size: 12px; line-height: 1.5; color: color-mix(in oklch, var(--color-base-content) 60%, transparent); }
  .refreshing :global(svg) { animation: storage-spin 0.8s linear infinite; }
  @keyframes storage-spin { to { transform: rotate(360deg); } }
</style>
