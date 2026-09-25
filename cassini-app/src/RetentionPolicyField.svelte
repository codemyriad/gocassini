<script lang="ts">
  import type { RetentionPolicy } from "./operator/retention";
  export let policy: RetentionPolicy;
  export let label: string;
  const presets = [7, 30, 60, 90];
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
  <legend class="font-medium text-sm">{label}</legend>
  <label class="flex items-center gap-2 text-sm"><input type="checkbox" checked={policy.forever} on:change={e => setForever(e.currentTarget.checked)} />Keep forever</label>
    {#each presets as days}
      <button type="button" class="btn btn-sm" class:btn-primary={!policy.forever && !showCustom && policy.count === days} aria-pressed={!policy.forever && !showCustom && policy.count === days} on:click={() => selectDays(days)}>{days} days</button>
    {/each}
    <button type="button" class="btn btn-sm" class:btn-primary={!policy.forever && showCustom} aria-pressed={!policy.forever && showCustom} on:click={selectCustom}>Custom days</button>
    {#if !policy.forever && showCustom}
      <label class="text-sm">Keep for <input class="input input-bordered w-24" aria-label={`${label} days`} type="number" min="1" max="9999" step="1" required bind:value={policy.count} /> days</label>
    {/if}
</fieldset>
