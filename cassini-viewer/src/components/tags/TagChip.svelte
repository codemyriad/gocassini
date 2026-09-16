<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { X } from "@lucide/svelte";

  import type { TagColorId, TagIconId } from "../../viewer/tagPalette";
  import TagIcon from "./TagIcon.svelte";

  export let label: string;
  export let color: TagColorId;
  export let icon: TagIconId | "" = "";
  export let removable = false;

  const dispatch = createEventDispatcher<{ remove: void }>();
</script>

<!-- One look wherever a tag is shown. A tag on the whole meeting and a tag on
     sections of it were a block and a pill with a count, a difference nobody
     could read; where the sections are is the transcript's to show. -->
<span class="tag-chip" data-tag-color={color}>
  {#if icon}
    <TagIcon {icon} size={11} />
  {:else}
    <TagIcon size={8} />
  {/if}
  <span class="tag-chip-label">{label}</span>
  {#if removable}
    <button
      type="button"
      class="tag-chip-remove"
      aria-label={`Remove ${label}`}
      on:click|stopPropagation={() => dispatch("remove")}
    >
      <X size={12} aria-hidden="true" />
    </button>
  {/if}
</span>

<style>
  .tag-chip {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 3px;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
    height: 18px;
    padding: 0 5px;
    font-size: 11.5px;
    font-weight: 550;
    line-height: 1;
    white-space: nowrap;
    color: var(--tag);
    background: color-mix(in srgb, var(--tag) 18%, transparent);
    border: 1px solid transparent;
    border-radius: 5px;
  }
  .tag-chip :global(span.tag-dot) {
    margin-right: 2px;
    translate: 0 0.5px;
    background: currentColor;
  }
  .tag-chip-label {
    padding-block: 3px;
    margin-block: -3px;
    overflow: hidden;
    text-overflow: ellipsis;
    text-box: trim-both ex alphabetic;
    translate: 0 0.5px;
  }
  .tag-chip-remove {
    display: inline-flex;
    margin: -2px -3px -2px 0;
    padding: 0 0 0 2px;
    color: inherit;
    background: none;
    border: 0;
    cursor: pointer;
    opacity: 0.7;
  }
  .tag-chip-remove:hover {
    opacity: 1;
  }
</style>
