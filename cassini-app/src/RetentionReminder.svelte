<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import type { RetentionSettings } from "./operator/retention";
  export let settings: RetentionSettings | null = null;
  export let error = "";
  export let loading = false;
  const dispatch = createEventDispatcher<{ review: void; retry: void }>();
  $: keepsForever = settings && [settings.recordings, settings.current, settings.logs,
    ...(settings.history.mode === "fine" ? Object.values(settings.history.fine) : [settings.history.policy])]
    .every(policy => policy.forever);
</script>

<section class="retention-reminder" aria-label="Retention reminder">
  <div>
    <p class="font-semibold">Review retention settings</p>
    <p>{keepsForever ? "Cassini currently keeps container files indefinitely." : "Review how long Cassini keeps container files."} You can keep using the app and review this in Storage.</p>
    {#if error}<p role="alert">{error}</p>{/if}
  </div>
  <div class="actions">
    <button class="btn btn-sm" on:click={() => dispatch("review")}>Review settings</button>
    {#if error}<button class="btn btn-sm" disabled={loading} on:click={() => dispatch("retry")}>{loading ? "Retrying…" : "Retry"}</button>{/if}
  </div>
</section>

<style>
  .retention-reminder { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 12px 16px; font-size: 14px; border-bottom: 1px solid var(--color-base-300); background: var(--color-base-200); }
  .retention-reminder > div:first-child { flex: 1 1 280px; min-width: 0; }
  .actions { display: flex; flex-wrap: wrap; gap: 8px; }
</style>
