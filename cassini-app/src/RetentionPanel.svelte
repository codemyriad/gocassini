<script lang="ts">
  import { createEventDispatcher, onMount, onDestroy } from "svelte";
  import { unsavedChanges, leavePrompt, cancelLeave, confirmLeave, guardLeave } from "./operator/unsaved";
  import type { OperatorClient } from "./operator/client";
  import { changeRetentionMode, retentionLabels, type RetentionSettings } from "./operator/retention";
  import RetentionPolicyField from "./RetentionPolicyField.svelte";
  export let operatorClient: OperatorClient | null = null;
  // Both Storage and initial setup render this entire editor. Hosts only own
  // completion/navigation; policy structure and persistence stay here.
  export let initialSettings: RetentionSettings | null = null;
  export let review = false;
  export let busy = false;
  export let disabled = false;
  const dispatch = createEventDispatcher<{ saved: RetentionSettings }>();
  let settings: RetentionSettings | null = null;
  let saved = "", error = "", notice = "";
  function accept(value: RetentionSettings) {
    settings = JSON.parse(JSON.stringify(value));
    saved = JSON.stringify(settings);
    split = value.history.mode === "fine" || !!value.history.fine_initialized;
  }
  let split = false;
  const supportedZones = (Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  const timezones = [...new Set(["UTC", Intl.DateTimeFormat().resolvedOptions().timeZone, ...supportedZones])];
  async function load() {
    busy = true; error = ""; notice = "";
    try { if (!operatorClient) throw new Error("Operator unavailable"); accept(await operatorClient.getRetention());
    } catch (e) { error = e instanceof Error ? e.message : String(e); } finally { busy = false; }
  }
  function mode(value: string) {
    if (!settings) return;
    settings.history = changeRetentionMode(settings.history, value as "group" | "fine", value === "fine" && !split);
    if (value === "fine") split = true;
  }
  async function save() {
    if (!settings || !operatorClient || busy || disabled) return; busy = true; error = ""; notice = "";
    try { const result = await operatorClient.putRetention(settings); accept(result); notice = "Retention settings saved."; dispatch("saved", result); }
    catch (e) { error = e instanceof Error ? e.message : String(e); } finally { busy = false; }
  }
  onMount(() => { if (initialSettings) accept(initialSettings); else void load(); });
  $: unsavedChanges.set(!!settings && JSON.stringify(settings) !== saved);
  onDestroy(() => { unsavedChanges.set(false); leavePrompt.set(null); });
</script>
<section class="grid gap-4" id="retention-policies">
  <h2 class="text-xl font-semibold">Retention policies</h2>
  <p>Choose how long Cassini keeps files in its container. Keep forever uses more space as recordings accumulate; shorter windows free space after cleanup. All categories currently default to keep forever.</p>
  <p class="text-sm">Published recordings in Nextcloud and job metadata are not deleted by these policies. These settings do not choose whether cameras are captured.</p>
  <p class="text-sm">Choose 7, 30, 60, 90 or a custom number of days. Retention ages use UTC dates. Cleanup runs at startup and on the daily schedule below. Active jobs are protected; busy or unsafe artefacts are retried on a later pass.</p>
  {#if error}<p class="alert alert-error" role="alert">{error}</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if settings}
    <form class="grid gap-4" on:submit|preventDefault={save}>
      <fieldset disabled={busy || disabled} class="grid gap-4">
        <section class="op-tint p-4">
          <h3 class="font-semibold">Recordings</h3>
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
        <p class="text-sm">Saving applies to existing artefacts at the next daily/startup cleanup, using their original lifecycle dates. Saving does not delete files immediately.</p>
        <button class="btn btn-primary justify-self-start" type="submit" disabled={!review && JSON.stringify(settings) === saved}>{review ? "Save and continue" : "Save retention settings"}</button>
      </fieldset>
    </form>
  {:else if busy}
    <p role="status">Loading retention settings…</p>
  {/if}
  <button class="btn btn-ghost justify-self-start" disabled={busy || disabled} on:click={() => guardLeave(load)}>Reload saved settings</button>
  {#if $leavePrompt}
    <div class="alert" role="alertdialog" tabindex="-1" aria-label="Leave without saving?">
      <p>You have unsaved changes. Leave without saving?</p>
      <button class="btn btn-sm" on:click={cancelLeave}>Stay</button>
      <button class="btn btn-sm" on:click={confirmLeave}>Leave</button>
    </div>
  {/if}
</section>
