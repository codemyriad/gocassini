<script lang="ts">
  import { X } from "@lucide/svelte";

  export let label: string;
  export let title: string | undefined = undefined;
</script>

<button type="button" class="close-button btn btn-sm btn-square btn-quiet" aria-label={label} {title} {...$$restProps} on:click>
  <X size={16} aria-hidden="true" />
</button>
