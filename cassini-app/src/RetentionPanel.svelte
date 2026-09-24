<script lang="ts">
  import { onMount } from "svelte";
  import type { OperatorClient } from "./operator/client";
  import { changeRetentionMode, retentionLabels, type RetentionSettings } from "./operator/retention";
  import RetentionPolicyField from "./RetentionPolicyField.svelte";
  export let operatorClient: OperatorClient | null = null;
  let settings: RetentionSettings | null = null;
  let saved = "", error = "", notice = "", busy = false;
  let split = { recordings: false, history: false };
  async function load() {
    busy = true; error = ""; notice = "";
    try { if (!operatorClient) throw new Error("Operator unavailable"); settings = await operatorClient.getRetention(); saved = JSON.stringify(settings);
      split = { recordings: !!settings.recordings.fine_initialized, history: !!settings.history.fine_initialized };
    } catch (e) { error = e instanceof Error ? e.message : String(e); } finally { busy = false; }
  }
  function mode(key: "recordings" | "history", value: string) {
    if (!settings) return;
    settings[key] = changeRetentionMode(settings[key], value as "group" | "fine", value === "fine" && !split[key]);
    if (value === "fine") split[key] = true;
  }
  async function save() {
    if (!settings || !operatorClient) return; busy = true; error = ""; notice = "";
    try { settings = await operatorClient.putRetention(settings); saved = JSON.stringify(settings); notice = "Retention settings saved."; }
    catch (e) { error = e instanceof Error ? e.message : String(e); } finally { busy = false; }
  }
  onMount(load);
</script>
<section class="grid gap-4">
  <h2 class="text-xl font-semibold">Storage</h2>
  <p>Container-local retention. All categories default to keep forever. External published recordings and job metadata are not deleted.</p>
  <p class="text-sm">Dates use UTC. Months clamp to the last day of the destination month. Cleanup is planned for startup and daily at 02:00 UTC; enforcement is not yet active in this development slice.</p>
  {#if error}<p class="alert alert-error" role="alert">{error}</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if settings}
    <form class="grid gap-4" on:submit|preventDefault={save}>
      <fieldset disabled={busy} class="grid gap-4">
        {#each ["recordings", "history"] as key}
          {@const groupKey = key as "recordings" | "history"}
          <section class="op-tint p-4">
            <h3 class="font-semibold">{key === "recordings" ? "Recordings" : "Attempt history"}</h3>
            <label class="text-sm">Policy control
              <select class="select select-bordered" value={settings[groupKey].mode} on:change={e => mode(groupKey, e.currentTarget.value)}>
                <option value="group">Whole group</option><option value="fine">Fine-grained</option>
              </select>
            </label>
            {#if settings[groupKey].mode === "group"}
              <RetentionPolicyField bind:policy={settings[groupKey].policy} label="Whole group" />
            {:else}
              {#each Object.keys(settings[groupKey].fine) as kind}<RetentionPolicyField bind:policy={settings[groupKey].fine[kind]} label={retentionLabels[kind]} />{/each}
            {/if}
            {#if key === "recordings"}<p class="text-sm">Video cannot outlive audio. Deleting audio removes the entire source capture and prevents rerunning jobs.</p>{/if}
          </section>
        {/each}
        <section class="op-tint p-4"><RetentionPolicyField bind:policy={settings.current} label="Current output archive" /><p class="text-sm">The current .meeting and .opus expire together.</p></section>
        <section class="op-tint p-4"><RetentionPolicyField bind:policy={settings.logs} label="Logs" /><p class="text-sm">Attempt stage logs, not operator service logs.</p></section>
        <p class="text-sm">Saving applies to existing artefacts at the next daily/startup cleanup, using their original lifecycle dates. Saving does not delete files immediately.</p>
        <button class="btn btn-primary justify-self-start" type="submit" disabled={JSON.stringify(settings) === saved}>Save retention settings</button>
      </fieldset>
    </form>
  {/if}
  <button class="btn btn-ghost justify-self-start" disabled={busy} on:click={load}>Reload saved settings</button>
</section>
