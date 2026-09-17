<script lang="ts">
  import { TAG_ICON_PATHS, type TagIconId } from "../../viewer/tagPalette";

  // A tag's mark: its icon, or a dot when it has none. Both take currentColor.
  export let icon: TagIconId | "" = "";
  export let size = 12;

  $: path = icon ? TAG_ICON_PATHS[icon] : undefined;
</script>

{#if path}
  <svg
    class="shrink-0"
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"><path d={path} /></svg
  >
{:else}
  <span class="tag-dot" style:--dot="{Math.round((size * 2) / 3)}px" aria-hidden="true"></span>
{/if}

<style>
  .tag-dot {
    flex: none;
    width: var(--dot);
    height: var(--dot);
    border-radius: 999px;
    background: currentColor;
  }
</style>
