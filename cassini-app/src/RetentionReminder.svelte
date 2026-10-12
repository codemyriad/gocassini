<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import type { RetentionSettings } from "./operator/retention";
  export let settings: RetentionSettings | null = null;
  export let error = "";
  export let loading = false;
  export let dirty = false;
  export let busy = false;
  export let dismissing = false;
  const dispatch = createEventDispatcher<{ review: void; retry: void; ignore: void }>();
  $: keepsForever = settings && [settings.recordings, settings.current, settings.logs,
    ...(settings.nextcloud ? [settings.nextcloud.meetings] : []),
    ...(settings.history.mode === "fine" ? Object.values(settings.history.fine) : [settings.history.policy])]
    .every(policy => policy.forever);
</script>

<section class="retention-reminder" aria-label="Retention reminder">
  <div>
    <p class="font-semibold">Choose how long Cassini keeps recordings</p>
    {#if keepsForever}<p>Recordings and published meetings are kept indefinitely until you set a limit.</p>{/if}
    {#if settings?.revision === 0 && dirty}<p class="scope">Save or discard your changes before dismissing.</p>{/if}
    {#if error}<p role="alert">{error}</p>{/if}
  </div>
  <div class="actions">
    <button class="btn btn-sm" on:click={() => dispatch("review")}>Review</button>
    {#if settings?.revision === 0}
      <button class="btn btn-sm btn-ghost" disabled={dirty || busy || loading} on:click={() => dispatch("ignore")}>
        {dismissing ? "Dismissing…" : "Dismiss"}
      </button>
    {/if}
    {#if error}<button class="btn btn-sm" disabled={loading || busy} on:click={() => dispatch("retry")}>{loading ? "Retrying…" : "Retry"}</button>{/if}
  </div>
</section>

<style>
  .retention-reminder { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 12px 16px; font-size: 14px; border-bottom: 1px solid var(--color-base-300); background: var(--color-base-200); }
  .retention-reminder > div:first-child { flex: 1 1 280px; min-width: 0; }
  .scope { font-size: 12px; opacity: 0.8; margin-top: 4px; }
  .actions { display: flex; flex-wrap: wrap; gap: 8px; }
</style>
