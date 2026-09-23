<script lang="ts">
  import { onMount } from "svelte";
  import { RefreshCw, TriangleAlert } from "@lucide/svelte";
  import { formatStorageBytes, storageUsageTotal } from "./operator/storageUsage";
  import type { OperatorClient } from "./operator/client";
  import type { StorageUsage } from "./operator/types";

  export let operatorClient: OperatorClient | null = null;

  let usage: StorageUsage | null = null;
  let loading = true;
  let recalculating = false;
  let loadError = "";

  $: total = usage ? storageUsageTotal(usage.sources) : null;

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
        ? await operatorClient.recalculateNextcloudStorageUsage()
        : await operatorClient.getNextcloudStorageUsage();
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

<section class="report" aria-labelledby="nextcloud-storage-title">
  <header class="report-head">
    <div>
      <p class="report-kicker">Alternative report A</p>
      <h2 id="nextcloud-storage-title">Nextcloud storage modes</h2>
      <p>Published recordings in each possible storage root.</p>
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
    <div class="report-state">
      <strong>This report has not been calculated yet</strong>
      <span>Last refreshed: Never.</span>
    </div>
  {:else if usage}
    {#if loadError}<div class="alert alert-error text-sm"><TriangleAlert size={16} aria-hidden="true" />Couldn’t recalculate: {loadError}</div>{/if}
    <div class="report-card op-tint">
      <div class="report-summary">
        <div><span>Both roots total</span><strong>{total === null ? "Partial result" : formatStorageBytes(total)}</strong></div>
        <p>Last refreshed {measuredAt(usage.measured_at)}</p>
      </div>
      <div class="root-rows">
        {#each usage.sources as source (source.id)}
          <article>
            <div><h3>{source.label}</h3><p>{source.location}</p></div>
            {#if source.error}<span class="error" title={source.error}>Couldn’t measure</span>{:else}<strong>{formatStorageBytes(source.bytes)}</strong>{/if}
          </article>
        {/each}
      </div>
    </div>
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
  .report-card { overflow: hidden; }
  .report-summary { display: flex; align-items: end; justify-content: space-between; gap: 12px; padding: 16px 18px; }
  .report-summary div { display: grid; gap: 3px; }
  .report-summary span { font-size: 10px; font-weight: 650; letter-spacing: .06em; text-transform: uppercase; color: color-mix(in oklch, var(--color-base-content) 55%, transparent); }
  .report-summary strong { font-size: 22px; }
  .report-summary p { margin: 0; font-size: 12px; color: color-mix(in oklch, var(--color-base-content) 60%, transparent); }
  .root-rows { border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .root-rows article { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px 18px; }
  .root-rows article + article { border-top: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200)); }
  .root-rows h3 { margin: 0; font-size: 13px; }
  .root-rows p { margin: 3px 0 0; font: 11px/1.4 ui-monospace, monospace; color: color-mix(in oklch, var(--color-base-content) 58%, transparent); }
  .root-rows strong { flex: none; font-size: 15px; font-variant-numeric: tabular-nums; }
  .error { color: var(--color-error); font-size: 12px; font-weight: 600; }
  .refreshing :global(svg) { animation: report-spin .8s linear infinite; }
  @media (max-width: 600px) { .report-head, .report-summary { align-items: start; flex-direction: column; } }
  @keyframes report-spin { to { transform: rotate(360deg); } }
</style>
