<script lang="ts">
  import { createEventDispatcher, onMount, onDestroy, tick } from "svelte";
  import { unsavedChanges, leavePrompt, guardLeave } from "./operator/unsaved";
  import type { OperatorClient } from "./operator/client";
  import { changeRetentionMode, retentionLabels, type RetentionSettings } from "./operator/retention";
  import RetentionPolicyField from "./RetentionPolicyField.svelte";
  import RetentionActions from "./RetentionActions.svelte";
  export let operatorClient: OperatorClient | null = null;
  // Both Storage and initial setup render this entire editor. Hosts only own
  // completion/navigation; policy structure and persistence stay here.
  export let initialSettings: RetentionSettings | null = null;
  export let review = false;
  export let busy = false;
  export let disabled = false;
  export let formId = "retention-settings-form";
  export let beforeSave: (() => Promise<void>) | null = null;
  export let extraDirty = false;
  export let beforeReload: (() => Promise<void>) | null = null;
  let scroller: HTMLDivElement;
  // Latch after reaching the end: scrolling back to edit must not relock Save.
  let reviewed = false;
  function checkReview() {
    if (settings && scroller?.clientHeight > 0 && scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight <= 2) reviewed = true;
  }
  function observeContent(node: HTMLElement) {
    if (!review) return;
    const observer = new ResizeObserver(checkReview);
    observer.observe(node);
    observer.observe(node.parentElement!);
    return { destroy: () => observer.disconnect() };
  }
  const dispatch = createEventDispatcher<{ saved: RetentionSettings }>();
  let settings: RetentionSettings | null = null;
  let saved = "", error = "", notice = "";
  function accept(value: RetentionSettings) {
    reviewed = false;
    settings = JSON.parse(JSON.stringify(value));
    saved = JSON.stringify(settings);
    split = value.history.mode === "fine" || !!value.history.fine_initialized;
    if (review) void tick().then(checkReview);
  }
  let split = false;
  const supportedZones = (Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  const timezones = [...new Set(["UTC", Intl.DateTimeFormat().resolvedOptions().timeZone, ...supportedZones])];
  async function load() {
    busy = true; error = ""; notice = "";
    try { if (!operatorClient) throw new Error("Operator unavailable"); if (beforeReload) await beforeReload(); accept(await operatorClient.getRetention());
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      // guardLeave cleared the flag before attempting a reload. If it failed,
      // the draft is still here and must still be protected on the next exit.
      unsavedChanges.set(extraDirty || (!!settings && JSON.stringify(settings) !== saved));
    } finally { busy = false; }
  }
  function mode(value: string) {
    if (!settings) return;
    settings.history = changeRetentionMode(settings.history, value as "group" | "fine", value === "fine" && !split);
    if (value === "fine") split = true;
  }
  async function save() {
    if (!settings || !operatorClient || busy || disabled || (review && !reviewed)) return; busy = true; error = ""; notice = "";
    try { if (beforeSave) await beforeSave(); const result = await operatorClient.putRetention(settings); accept(result); notice = "Retention settings saved."; dispatch("saved", result); }
    catch (e) { error = e instanceof Error ? e.message : String(e); } finally { busy = false; }
  }
  onMount(() => { if (initialSettings) accept(initialSettings); else void load(); });
  $: unsavedChanges.set(extraDirty || (!!settings && JSON.stringify(settings) !== saved));
  onDestroy(() => { unsavedChanges.set(false); leavePrompt.set(null); });
</script>
<section class="retention-panel" class:review id="retention-policies">
  <!-- svelte-ignore a11y_no_noninteractive_tabindex (the scroll region must support keyboard review) -->
  <div class="retention-scroll" bind:this={scroller} on:scroll={checkReview} tabindex={review ? 0 : undefined} role={review ? "region" : undefined} aria-label={review ? "Setup settings" : undefined}>
  <div class="retention-content" use:observeContent>
  <slot name="before" />
  <h2 class="text-xl font-semibold">Retention policies</h2>
  <p>Choose how long Cassini keeps files in its container. Keep forever uses more space as recordings accumulate; shorter windows free space after cleanup. All categories currently default to keep forever.</p>
  <p class="text-sm">Published recordings in Nextcloud and job metadata are not deleted by these policies. These settings do not choose whether cameras are captured.</p>
  <p class="text-sm">Choose 7, 30, 60, 90 or a custom number of days. Retention ages use UTC dates. Cleanup runs at startup and on the daily schedule below. Active jobs are protected; busy or unsafe artefacts are retried on a later pass.</p>
  {#if settings}
    <form id={formId} class="grid gap-4" on:submit|preventDefault={save}>
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
      </fieldset>
    </form>
  {:else if busy}
    <p role="status">Loading retention settings…</p>
  {/if}
  </div>
  </div>
  <div class="retention-footer">
    <RetentionActions {formId} {review} {reviewed} {error} {notice}
      disabled={busy || disabled} canSave={!!settings && (review ? reviewed : JSON.stringify(settings) !== saved)}
      on:reload={() => guardLeave(load)} />
  </div>
</section>

<style>
  .retention-panel, .retention-content { display: grid; gap: 16px; min-width: 0; }
  .review { display: flex; flex-direction: column; min-height: 0; height: 100%; gap: 0; }
  .review .retention-scroll { flex: 1; min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
  .review .retention-content { padding: 24px; }
  .review .retention-footer { flex: none; border-top: 1px solid var(--color-base-300); padding: 16px 24px; }
  @media (max-width: 600px) {
    .review .retention-content { padding: 16px; }
    .review .retention-footer { padding: 12px 16px; }
  }
</style>
