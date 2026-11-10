<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { LL } from "cassini-viewer/i18n";
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

<section class="retention-reminder" aria-label={$LL.app.settings.retention_reminder.region_label()}>
  <div>
    <p class="font-semibold">{$LL.app.settings.retention_reminder.title()}</p>
    {#if keepsForever}<p>{$LL.app.settings.retention_reminder.keeps_forever()}</p>{/if}
    {#if settings?.revision === 0 && dirty}<p class="scope">{$LL.app.settings.retention_reminder.unsaved_first()}</p>{/if}
    {#if error}<p role="alert">{error}</p>{/if}
  </div>
  <div class="actions">
    <button class="btn btn-sm" on:click={() => dispatch("review")}>{$LL.app.settings.retention_reminder.review()}</button>
    {#if settings?.revision === 0}
      <button class="btn btn-sm btn-ghost" disabled={dirty || busy || loading} on:click={() => dispatch("ignore")}>
        {dismissing ? $LL.app.settings.retention_reminder.dismissing() : $LL.app.settings.retention_reminder.dismiss()}
      </button>
    {/if}
    {#if error}<button class="btn btn-sm" disabled={loading || busy} on:click={() => dispatch("retry")}>{loading ? $LL.app.settings.retention_reminder.retrying() : $LL.app.settings.retention_reminder.retry()}</button>{/if}
  </div>
</section>

<style>
  .retention-reminder { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 12px 16px; font-size: 14px; border-bottom: 1px solid var(--color-base-300); background: var(--color-base-200); }
  .retention-reminder > div:first-child { flex: 1 1 280px; min-width: 0; }
  .scope { font-size: 12px; opacity: 0.8; margin-top: 4px; }
  .actions { display: flex; flex-wrap: wrap; gap: 8px; }
</style>
