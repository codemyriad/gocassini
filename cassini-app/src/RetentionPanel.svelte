<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { unsavedChanges, leavePrompt, cancelLeave, confirmLeave, guardLeave } from "./operator/unsaved";
  import type { OperatorClient } from "./operator/client";
  import { changeRetentionMode, retentionLabels, type RetentionSettings } from "./operator/retention";
  import RetentionPolicyField from "./RetentionPolicyField.svelte";
  export let operatorClient: OperatorClient | null = null;
  let settings: RetentionSettings | null = null;
  let saved = "", error = "", notice = "", busy = false;
  let split = false;
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
<section class="grid gap-4">
  <h2 class="text-xl font-semibold">Storage</h2>
  <p>Container-local retention. All categories default to keep forever. External published recordings and job metadata are not deleted.</p>
  <p class="text-sm">Dates use UTC. Months clamp to the last day of the destination month. Cleanup runs at startup and daily at 02:00 UTC. Active jobs are protected; busy or unsafe artefacts are retried on a later pass.</p>
  {#if error}<p class="alert alert-error" role="alert">{error}</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if settings}
    <form class="grid gap-4" on:submit|preventDefault={save}>
      <fieldset disabled={busy} class="grid gap-4">
        <section class="op-tint p-4">
          <h3 class="font-semibold">Recordings</h3>
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
