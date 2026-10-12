<script lang="ts">
  // One downloadable model as a single row: name · size · state · one action.
  // The action goes in the "action" slot; progress, errors and notes go in
  // the default slot under the row.
  export let name: string;
  export let size = "";
  export let state = "";
</script>

<div class="model-row">
  <div class="model-line">
    <strong class="model-name">{name}</strong>
    {#if size}<span class="model-meta">{size}</span>{/if}
    <span class="model-meta" role="status">{state}</span>
    {#if $$slots.action}<span class="model-action"><slot name="action" /></span>{/if}
  </div>
  {#if $$slots.default}<div class="model-detail"><slot /></div>{/if}
</div>

<style>
  .model-row { display: grid; gap: 8px; padding: 10px 14px; border-radius: 8px; background: var(--op-inset, color-mix(in oklch, var(--color-base-content) 5%, transparent)); }
  .model-line { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 16px; min-width: 0; }
  .model-name { font-size: 13.5px; overflow-wrap: anywhere; }
  .model-meta { font-size: 12.5px; color: color-mix(in oklch, var(--color-base-content) 65%, transparent); }
  .model-action { margin-left: auto; }
  .model-detail { display: grid; gap: 6px; font-size: 12.5px; min-width: 0; }
</style>
