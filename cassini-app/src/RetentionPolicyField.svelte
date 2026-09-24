<script lang="ts">
  import type { RetentionPolicy } from "./operator/retention";
  export let policy: RetentionPolicy;
  export let label: string;
  function setForever(forever: boolean) { policy = forever ? { forever: true } : { forever: false, count: 30, unit: "days" }; }
</script>
<fieldset class="flex flex-wrap items-center gap-3 py-2">
  <legend class="font-medium text-sm">{label}</legend>
  <label class="flex items-center gap-2 text-sm"><input type="checkbox" checked={policy.forever} on:change={e => setForever(e.currentTarget.checked)} />Keep forever</label>
  {#if !policy.forever}
    <label class="text-sm">Keep for <input class="input input-bordered w-24" aria-label={`${label} count`} type="number" min="1" max="9999" step="1" required bind:value={policy.count} /></label>
    <select class="select select-bordered" aria-label={`${label} unit`} bind:value={policy.unit}>
      <option value="days">days</option><option value="weeks">weeks</option><option value="months">months</option>
    </select>
  {/if}
</fieldset>
