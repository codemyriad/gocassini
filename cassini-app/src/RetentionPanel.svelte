<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { unsavedChanges, leavePrompt, cancelLeave, confirmLeave, guardLeave } from "./operator/unsaved";
  import type { OperatorClient } from "./operator/client";
  import { changeRetentionMode, retentionLabels, type RetentionSettings } from "./operator/retention";
  import RetentionPolicyField from "./RetentionPolicyField.svelte";
  export let operatorClient: OperatorClient | null = null;
  let settings: RetentionSettings | null = null;
  let saved = "", error = "", notice = "", busy = false;
  let preview: import("./operator/retention").RetentionPreview | null = null;
  let previewSettings = "";
  let operations: import("./operator/retention").RetentionOperations | null = null;
  async function loadOperations(offset = 0) {
    if (!operatorClient) return;
    try { operations = await operatorClient.retentionOperations(offset); }
    catch(e) { error=e instanceof Error?e.message:String(e); }
  }
  async function previewNextcloud() {
   if (!settings || !operatorClient) return;
   busy=true; error="";
   try { preview=await operatorClient.previewRetention(settings);previewSettings=JSON.stringify(settings); }
   catch(e) {error=e instanceof Error?e.message:String(e);} finally {busy=false;}
  }
  let split = false;
  const supportedZones = (Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  const timezones = [...new Set(["UTC", Intl.DateTimeFormat().resolvedOptions().timeZone, ...supportedZones])];
  async function load() {
    busy = true; error = ""; notice = "";
    try { if (!operatorClient) throw new Error("Operator unavailable"); settings = await operatorClient.getRetention(); saved = JSON.stringify(settings);
      split = settings.history.mode === "fine" || !!settings.history.fine_initialized;
    } catch (e) { error = e instanceof Error ? e.message : String(e); } finally { busy = false; }
  }
  function mode(value: string) {
    if (!settings) return;
    settings.history = changeRetentionMode(settings.history, value as "group" | "fine", value === "fine" && !split);
    if (value === "fine") split = true;
  }
  async function save() {
    if (!settings || !operatorClient) return; busy = true; error = ""; notice = "";
    try { settings = await operatorClient.putRetention(settings); saved = JSON.stringify(settings); notice = "Retention settings saved."; }
    catch (e) { error = e instanceof Error ? e.message : String(e); } finally { busy = false; }
  }
  onMount(load);
  $: unsavedChanges.set(!!settings && JSON.stringify(settings) !== saved);
  onDestroy(() => { unsavedChanges.set(false); leavePrompt.set(null); });
</script>
<section class="grid gap-4" id="retention-policies">
  <h2 class="text-xl font-semibold">Retention policies</h2>
  <p>Container storage and Nextcloud Files have separate policies. All categories default to keep forever. Job metadata is retained.</p>
  <p class="text-sm">Choose 7, 30, 60, 90 or a custom number of days. Retention ages use UTC dates. Cleanup runs at startup and on the daily schedule below. Active jobs are protected; busy or unsafe artefacts are retried on a later pass.</p>
  {#if error}<p class="alert alert-error" role="alert">{error}</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if settings}
    <form class="grid gap-4" on:submit|preventDefault={save}>
      <fieldset disabled={busy} class="grid gap-4">
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
        <section class="op-tint p-4">
          <h3 class="font-semibold">Container recordings</h3>
          <RetentionPolicyField bind:policy={settings.recordings} label="Source recordings" />
          <p class="text-sm">Deletes the entire original recording, including captured audio, video and supporting files. Once deleted, the job cannot be rerun.</p>
        </section>
        <section class="op-tint p-4">
          <h3 class="font-semibold">Attempt history</h3>
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
        <section class="op-tint p-4"><RetentionPolicyField bind:policy={settings.current} label="Current output archive" /><p class="text-sm">The current .meeting and .opus expire together.</p></section>
        <section class="op-tint p-4"><RetentionPolicyField bind:policy={settings.logs} label="Logs" /><p class="text-sm">Attempt stage logs, not operator service logs.</p></section>
        {#if settings.nextcloud}
        <section class="op-tint p-4 grid gap-3">
          <h3 class="font-semibold">Nextcloud Files</h3>
          <RetentionPolicyField bind:policy={settings.nextcloud.meetings} label="Whole meetings" />
          <p class="text-sm">Deletes the published meeting, including its audio, transcription and meeting notes. The meeting disappears from shared Files and Cassini.</p>
          <p class="text-sm">Ages use the original recording date, including existing managed meetings. Nextcloud manages previous versions and Deleted files; their storage usage is unknown.</p>
          <button class="btn btn-secondary justify-self-start" type="button" on:click={previewNextcloud}>Preview Nextcloud retention</button>
          {#if preview && previewSettings === JSON.stringify(settings)}
            <div role="status">
              <p>{preview.retire} meeting removals due as of {preview.now}.</p>
              {#if !preview.capability}<p>{preview.reason}</p>{/if}
              <p>{preview.historyNotice}</p>
              <p>Active Nextcloud meetings: {preview.usage.count} files, {preview.usage.bytes.toLocaleString()} logical bytes.</p>
              <ul>{#each preview.meetings as effect}<li>{effect.name}: {effect.action}{effect.deadline ? `; expires ${effect.deadline}` : ""}{effect.reason ? `; ${effect.reason}` : ""}</li>{/each}</ul>
            </div>
          {/if}
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
        <button class="btn btn-primary justify-self-start" type="submit" disabled={JSON.stringify(settings) === saved}>Save retention settings</button>
      </fieldset>
    </form>
  {/if}
  <button class="btn btn-ghost justify-self-start" disabled={busy} on:click={() => guardLeave(load)}>Reload saved settings</button>
  {#if $leavePrompt}
    <div class="alert" role="alertdialog" tabindex="-1" aria-label="Leave without saving?">
      <p>You have unsaved changes. Leave without saving?</p>
      <button class="btn btn-sm" on:click={cancelLeave}>Stay</button>
      <button class="btn btn-sm" on:click={confirmLeave}>Leave</button>
    </div>
  {/if}
</section>
