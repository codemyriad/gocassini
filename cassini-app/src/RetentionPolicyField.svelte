<script lang="ts">
  import { LL } from "cassini-viewer/i18n";
  import { retentionDayPresets, type RetentionPolicy } from "./operator/retention";
  export let policy: RetentionPolicy;
  export let label: string;
  // Category cards already show the label as their heading.
  export let hideLabel = false;
  const presets: readonly number[] = retentionDayPresets;
  let custom = !policy.forever && !presets.includes(policy.count ?? 0);
  function setForever(forever: boolean) {
    custom = false;
    policy = forever ? { forever: true } : { forever: false, count: 30, unit: "days" };
  }
  function selectDays(days: number) {
    custom = false;
    policy = { forever: false, count: days, unit: "days" };
  }
  function selectCustom() {
    if (policy.forever) policy = { forever: false, count: 30, unit: "days" };
    custom = true;
  }
  $: showCustom = custom || (!policy.forever && !presets.includes(policy.count ?? 0));
</script>
<fieldset class="flex flex-wrap items-center gap-3 py-2">
  <legend class="font-medium text-sm {hideLabel ? 'sr-only' : ''}">{label}</legend>
  <label class="flex items-center gap-2 text-sm"><input type="checkbox" checked={policy.forever} on:change={e => setForever(e.currentTarget.checked)} />{$LL.app.settings.retention_policy.keep_forever()}</label>
    {#each presets as days (days)}
      <button type="button" class="btn btn-sm" class:btn-primary={!policy.forever && !showCustom && policy.count === days} aria-pressed={!policy.forever && !showCustom && policy.count === days} on:click={() => selectDays(days)}>{$LL.app.settings.retention_policy.preset_days_other({ count: days })}</button>
    {/each}
    <button type="button" class="btn btn-sm" class:btn-primary={!policy.forever && showCustom} aria-pressed={!policy.forever && showCustom} on:click={selectCustom}>{$LL.app.settings.retention_policy.custom()}</button>
    {#if !policy.forever && showCustom}
      <label class="text-sm">{$LL.app.settings.retention_policy.keep_for_before()} <input class="input input-bordered w-24" aria-label={$LL.app.settings.retention_policy.custom_days_label({ label })} type="number" min="1" max="9999" step="1" required bind:value={policy.count} /> {$LL.app.settings.retention_policy.keep_for_after()}</label>
    {/if}
</fieldset>
