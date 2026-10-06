<script lang="ts">
  import { createEventDispatcher, onMount, onDestroy } from "svelte";
  import { unsavedChanges, leavePrompt, guardLeave } from "./operator/unsaved";
  import type { OperatorClient } from "./operator/client";
  import { changeRetentionMode, retentionLabels, type RetentionSettings } from "./operator/retention";
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
  let operations: import("./operator/retention").RetentionOperations | null = null;
  async function loadOperations(offset = 0) {
    if (!operatorClient) return;
    try { operations = await operatorClient.retentionOperations(offset); }
    catch(e) { error=e instanceof Error?e.message:String(e); }
  }
  let split = false;
  const supportedZones = (Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  const timezones = [...new Set(["UTC", Intl.DateTimeFormat().resolvedOptions().timeZone, ...supportedZones])];
  async function load() {
    busy = true; error = ""; notice = "";
    try { if (!operatorClient) throw new Error("Operator unavailable"); accept(await operatorClient.getRetention());
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
      notice = "Retention settings saved.";
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
  <h2 class="text-xl font-semibold" tabindex="-1">Retention policies</h2>
  <p>Container storage and Nextcloud Files have separate policies. All categories default to keep forever. Job metadata is retained.</p>
  <p class="text-sm">Choose 7, 30, 60, 90 or a custom number of days. Retention ages use UTC dates. Cleanup runs at startup and on the daily schedule below. Active jobs are protected; busy or unsafe artefacts are retried on a later pass.</p>
  {#if settings}
    <form id={formId} class="grid gap-4" on:submit|preventDefault={save}>
      <fieldset disabled={busy || disabled || $retentionMutationBusy} class="grid gap-4">
        <section class="op-tint p-4">
          <h3 class="font-semibold">Container recordings</h3>
          <RetentionPolicyField bind:policy={settings.recordings} label="Source recordings" />
          <p class="text-sm">Original captured audio, any captured video and supporting files can take substantial space. Cleanup deletes the entire source recording. Once deleted, the job cannot be rerun.</p>
        </section>
        <section class="op-tint p-4">
          <h3 class="font-semibold">Attempt history</h3>
          <p class="text-sm">Failed recordings, intermediate build files and replaced outputs help diagnose problems but can accumulate across retries. Choose one window for all of them or a separate window for each kind.</p>
          <label class="text-sm">Policy control
            <select class="select select-bordered" value={settings.history.mode} on:change={e => mode(e.currentTarget.value)}>
              <option value="group">Whole group</option><option value="fine">Fine-grained</option>
            </select>
          </label>
          {#if settings.history.mode === "group"}
            <RetentionPolicyField bind:policy={settings.history.policy} label="Whole group" />
          {:else}
            {#each Object.keys(settings.history.fine) as kind}<RetentionPolicyField bind:policy={settings.history.fine[kind]} label={retentionLabels[kind]} />{/each}
          {/if}
        </section>
        <section class="op-tint p-4"><RetentionPolicyField bind:policy={settings.current} label="Current output archive" /><p class="text-sm">The latest local meeting archive and playable audio copy expire together. Removing these local copies saves space; recordings already published in Nextcloud remain available.</p></section>
        <section class="op-tint p-4"><RetentionPolicyField bind:policy={settings.logs} label="Logs" /><p class="text-sm">Processing logs help diagnose failures and grow with each attempt. This does not include operator service logs.</p></section>
        <section class="op-tint p-4 grid gap-3">
          <h3 class="font-semibold">Daily cleanup schedule</h3>
          <label class="text-sm">Sweep time
            <input class="input input-bordered" type="time" step="60" required bind:value={settings.schedule.time} />
          </label>
          <label class="text-sm">Timezone
            <input class="input input-bordered w-full" list="retention-timezones" required bind:value={settings.schedule.timezone} placeholder="UTC or Europe/Zagreb" />
            <datalist id="retention-timezones">{#each timezones as timezone}<option value={timezone}></option>{/each}</datalist>
          </label>
          <p class="text-sm">Default: 02:00 UTC. Saving updates the next scheduled cleanup without restarting. If daylight saving skips the chosen time, cleanup runs at the first available time afterward; if the time repeats, it runs at the first occurrence only.</p>
        </section>
        {#if settings.nextcloud}
        <section class="op-tint p-4 grid gap-3">
          <h3 class="font-semibold">Nextcloud Files</h3>
          <RetentionPolicyField bind:policy={settings.nextcloud.meetings} label="Whole meetings" />
          <p class="text-sm">Deletes the published meeting, including its audio, transcription and meeting notes. The meeting disappears from shared Files and Cassini.</p>
          <p class="text-sm">Ages use the recorded local date when available, otherwise the published meeting’s creation date. Nextcloud manages previous versions and Deleted files; their storage usage is unknown.</p>
          <button class="btn btn-secondary justify-self-start" type="button" on:click={() => loadOperations()}>Refresh Nextcloud operation status</button>
          {#if operations}
            <div aria-label="Nextcloud retention operations">
              {#if operations.operations.length === 0}<p>No operations on this page.</p>{/if}
              <ul>{#each operations.operations as operation}<li>{operation.name}: {operation.status} ({operation.updatedAt}){operation.error ? `; ${operation.error}` : ""}</li>{/each}</ul>
              {#if operations.operations.length === 100}<button class="btn btn-ghost" type="button" on:click={() => loadOperations(operations?.nextOffset)}>Next operations</button>{/if}
            </div>
          {/if}
        </section>
        {/if}
        <p class="text-sm">Saving applies to existing artefacts at the next daily/startup cleanup, using their original lifecycle dates. Saving does not delete files immediately.</p>
      </fieldset>
    </form>
  {:else if busy}
    <p role="status">Loading retention settings…</p>
  {/if}
  </div>
  <div class="retention-footer">
    <RetentionActions {formId} {error} {notice}
      disabled={busy || disabled || $retentionMutationBusy} canSave={!!settings && (settings.revision === 0 || JSON.stringify(settings) !== saved)}
      on:reload={() => guardLeave(load)} />
  </div>
</section>

<style>
  .retention-panel, .retention-content { display: grid; gap: 16px; min-width: 0; }
</style>
