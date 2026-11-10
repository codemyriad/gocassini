<script lang="ts">
  import { createEventDispatcher, onMount, onDestroy } from "svelte";
  import { unsavedChanges, leavePrompt, guardLeave } from "./operator/unsaved";
  import type { OperatorClient } from "./operator/client";
  import { LL } from "cassini-viewer/i18n";
  import { changeRetentionMode, retentionLabel, type RetentionSettings } from "./operator/retention";
  import RetentionPolicyField from "./RetentionPolicyField.svelte";
  import { notifyRetentionChanged } from "./operator/retentionSignal";
  import { retentionMutationBusy, withRetentionMutation } from "./operator/retentionReminder";
  import RetentionActions from "./RetentionActions.svelte";
  export let operatorClient: OperatorClient | null = null;
  // Storage owns the complete policy editor and its draft.
  export let initialSettings: RetentionSettings | null = null;
  export let busy = false;
  export let disabled = false;
  export let formId = "retention-settings-form";
  const dispatch = createEventDispatcher<{ saved: RetentionSettings }>();
  let alive = true;
  let settings: RetentionSettings | null = null;
  let saved = "", error = "", notice = "";
  function accept(value: RetentionSettings) {
    if (!alive) return;
    settings = JSON.parse(JSON.stringify(value));
    saved = JSON.stringify(settings);
    split = value.history.mode === "fine" || !!value.history.fine_initialized;
    notifyRetentionChanged(value);
  }
  let split = false;
  const supportedZones = (Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  const timezones = [...new Set(["UTC", Intl.DateTimeFormat().resolvedOptions().timeZone, ...supportedZones])];
  // A saved zone the browser does not list must stay selectable, or the select would drop it.
  $: zoneOptions = settings && !timezones.includes(settings.schedule.timezone) ? [settings.schedule.timezone, ...timezones] : timezones;
  async function load() {
    busy = true; error = ""; notice = "";
    try { if (!operatorClient) throw new Error($LL.app.settings.retention.operator_unavailable()); accept(await operatorClient.getRetention());
    } catch (e) {
      if (!alive) return;
      error = e instanceof Error ? e.message : String(e);
      // guardLeave cleared the flag before attempting a reload. If it failed,
      // the draft is still here and must still be protected on the next exit.
      unsavedChanges.set(!!settings && JSON.stringify(settings) !== saved);
    } finally { busy = false; }
  }
  function mode(value: string) {
    if (!settings) return;
    settings.history = changeRetentionMode(settings.history, value as "group" | "fine", value === "fine" && !split);
    if (value === "fine") split = true;
  }
  async function save() {
    if (!settings || !operatorClient || busy || disabled || $retentionMutationBusy) return;
    busy = true;
    error = "";
    notice = "";
    const snapshot = settings;
    const client = operatorClient;
    try {
      const result = await withRetentionMutation(() => client.putRetention(snapshot));
      // Navigation may remove this editor while the server commits the save.
      // The shell still needs the committed revision; never restore the draft.
      if (!alive) { notifyRetentionChanged(result); return; }
      accept(result);
      notice = $LL.app.settings.retention.saved();
      dispatch("saved", result);
    } catch (e) {
      if (alive) error = e instanceof Error ? e.message : String(e);
    } finally { busy = false; }
  }
  function beforeUnload(event: BeforeUnloadEvent) {
    if ($unsavedChanges || busy) {
      event.preventDefault();
      event.returnValue = "";
    }
  }
  onMount(() => { if (initialSettings) accept(initialSettings); else void load(); });
  $: unsavedChanges.set(!!settings && JSON.stringify(settings) !== saved);
  onDestroy(() => { alive = false; unsavedChanges.set(false); leavePrompt.set(null); });
</script>
<svelte:window on:beforeunload={beforeUnload} />
<section class="retention-panel" id="retention-policies">
  <div class="retention-content">
  <div class="grid gap-1">
    <h2 class="text-xl font-semibold" tabindex="-1">{$LL.app.settings.retention.heading()}</h2>
    <p class="text-sm opacity-80">{$LL.app.settings.retention.intro()}</p>
  </div>
  {#if settings}
    <form id={formId} class="grid gap-4" on:submit|preventDefault={save}>
      <fieldset disabled={busy || disabled || $retentionMutationBusy} class="grid gap-4">
        <section class="op-tint p-4 category">
          <h3 class="font-semibold">{$LL.app.settings.retention.recordings_heading()}</h3>
          <p class="text-sm opacity-80">{$LL.app.settings.retention.recordings_description()}</p>
          <RetentionPolicyField bind:policy={settings.recordings} label={$LL.app.settings.retention.recordings_heading()} hideLabel />
        </section>
        <section class="op-tint p-4 category">
          <h3 class="font-semibold">{$LL.app.settings.retention.current_heading()}</h3>
          <p class="text-sm opacity-80">{$LL.app.settings.retention.current_description()}</p>
          <RetentionPolicyField bind:policy={settings.current} label={$LL.app.settings.retention.current_heading()} hideLabel />
        </section>
        <section class="op-tint p-4 category">
          <h3 class="font-semibold">{$LL.app.settings.retention.history_heading()}</h3>
          <p class="text-sm opacity-80">{$LL.app.settings.retention.history_description()}</p>
          {#if settings.history.mode === "group"}
            <RetentionPolicyField bind:policy={settings.history.policy} label={$LL.app.settings.retention.history_heading()} hideLabel />
            <button type="button" class="link text-sm justify-self-start" on:click={() => mode("fine")}>{$LL.app.settings.retention.split_kinds()}</button>
          {:else}
            {#each Object.keys(settings.history.fine) as kind (kind)}<RetentionPolicyField bind:policy={settings.history.fine[kind]} label={retentionLabel(kind)} />{/each}
            <button type="button" class="link text-sm justify-self-start" on:click={() => mode("group")}>{$LL.app.settings.retention.join_kinds()}</button>
          {/if}
        </section>
        <section class="op-tint p-4 category">
          <h3 class="font-semibold">{$LL.app.settings.retention.logs_heading()}</h3>
          <p class="text-sm opacity-80">{$LL.app.settings.retention.logs_description()}</p>
          <RetentionPolicyField bind:policy={settings.logs} label={$LL.app.settings.retention.logs_heading()} hideLabel />
        </section>
        {#if settings.nextcloud}
        <section class="op-tint p-4 category">
          <h3 class="font-semibold">{$LL.app.settings.retention.nextcloud_heading()}</h3>
          <p class="text-sm opacity-80">{$LL.app.settings.retention.nextcloud_description()}</p>
          <RetentionPolicyField bind:policy={settings.nextcloud.meetings} label={$LL.app.settings.retention.nextcloud_heading()} hideLabel />
        </section>
        {/if}
        <section class="op-tint p-4 category">
          <h3 class="font-semibold">{$LL.app.settings.retention.schedule_heading()}</h3>
          <div class="flex flex-wrap items-center gap-2 text-sm">
            <span>{$LL.app.settings.retention.run_at()}</span>
            <input class="input input-bordered w-32" type="time" step="60" required aria-label={$LL.app.settings.retention.cleanup_time_label()} bind:value={settings.schedule.time} />
            <select class="select select-bordered w-auto max-w-full" required aria-label={$LL.app.settings.retention.timezone_label()} bind:value={settings.schedule.timezone}>
              {#each zoneOptions as timezone (timezone)}<option value={timezone}>{timezone}</option>{/each}
            </select>
          </div>
        </section>
      </fieldset>
    </form>
  {:else if busy}
    <p role="status">{$LL.app.settings.retention.loading()}</p>
  {/if}
  </div>
  <div class="retention-footer">
    <RetentionActions {formId} {error} {notice} dirty={$unsavedChanges}
      disabled={busy || disabled || $retentionMutationBusy} canSave={!!settings && (settings.revision === 0 || JSON.stringify(settings) !== saved)}
      on:reload={() => guardLeave(load)} />
  </div>
</section>

<style>
  .retention-panel, .retention-content { display: grid; gap: 16px; min-width: 0; }
  .category { display: grid; gap: 4px; }
</style>
