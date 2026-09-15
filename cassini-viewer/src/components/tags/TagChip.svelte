<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { X } from "@lucide/svelte";

  import type { TagColorId, TagIconId } from "../../viewer/tagPalette";
  import TagIcon from "./TagIcon.svelte";

  export let label: string;
  export let color: TagColorId;
  export let icon: TagIconId | "" = "";
  // whole: on the whole meeting, a solid block. stretch: on stretches of it, a
  // tinted pill with how many.
  export let variant: "whole" | "stretch" = "stretch";
  export let count = 0;
  export let removable = false;

  const dispatch = createEventDispatcher<{ remove: void }>();
</script>

<span class="tag-chip" class:whole={variant === "whole"} data-tag-color={color}>
  {#if icon}
    <TagIcon {icon} size={11} />
  {:else}
    <TagIcon size={8} />
  {/if}
  <span class="tag-chip-label">{label}</span>
  {#if variant === "stretch" && count > 1}
    <span class="tag-chip-count">{count}<span class="sr-only">{" stretches"}</span></span>
  {/if}
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
    background: var(--tag-bg);
    border: 1px solid var(--tag-border);
    border-radius: 999px;
  }
  .tag-chip.whole {
    background: color-mix(in srgb, var(--tag) 18%, transparent);
    border-color: transparent;
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
  .tag-chip-count {
    font-family: var(--font-mono);
    font-size: 10.5px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
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
