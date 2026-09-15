<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";

  import { TAG_ICONS, styleName, type TagIconId } from "../../../viewer/tagPalette";
  import TagIcon from "../TagIcon.svelte";
  import { popover, stepIndex } from "../popover";

  // Tinted by the editor's data-tag-color around it.
  export let value: TagIconId | "";
  export let anchor: HTMLElement | null = null;

  const dispatch = createEventDispatcher<{ select: TagIconId | ""; close: void }>();
  let none: HTMLButtonElement;
  let buttons: HTMLButtonElement[] = [];
  let focused = Math.max(0, TAG_ICONS.indexOf(value as TagIconId));

  onMount(() => (value ? buttons[focused] : none).focus());

  function onKeydown(event: KeyboardEvent) {
    const next = event.target === none ? null : stepIndex(focused, event.key, TAG_ICONS.length, 8);
    if (next !== null) {
      event.preventDefault();
      buttons[next].focus();
    }
  }
</script>

<div use:popover={{ anchor, close: () => dispatch("close") }} role="radiogroup" aria-label="Tag icon" tabindex="-1"
  class="tag-popover w-[264px] p-2" on:keydown={onKeydown}>
  <button bind:this={none} type="button" role="radio" aria-checked={value === ""} on:click={() => dispatch("select", "")}
    class="ig-opt flex h-8 w-full cursor-pointer items-center gap-2 rounded-field px-2 text-sm font-medium hover:bg-base-content/8">
    <span class="ig-none-box size-3 rounded-full border border-dashed border-base-content/40" aria-hidden="true"></span>No icon
  </button>
  <div class="mt-1.5 grid grid-cols-8 gap-1 border-t border-base-300 pt-1.5">
    {#each TAG_ICONS as icon, index (icon)}
      <button bind:this={buttons[index]} type="button" role="radio" aria-checked={icon === value} aria-label={styleName(icon)} title={styleName(icon)}
        tabindex={index === focused ? 0 : -1} on:click={() => dispatch("select", icon)} on:focus={() => (focused = index)}
        class="ig-opt grid h-7 cursor-pointer place-items-center rounded-field text-(--tag) hover:bg-base-content/8">
        <TagIcon {icon} size={15} />
      </button>
    {/each}
  </div>
</div>

<style>
  /* The mark, not a tint: aria-checked used to read as a slightly darker
     hover, which on "No icon" — the one option with no icon to recognise —
     left nothing saying it was the choice in force. Drawn here rather than
     with ring-* utilities, which need @property and do not resolve inside the
     shadow root this runs in. */
  .ig-opt[aria-checked="true"] {
    background-color: var(--tag-bg);
    box-shadow: inset 0 0 0 1px var(--tag);
  }
  .ig-opt[aria-checked="true"] .ig-none-box {
    border-style: solid;
    border-color: var(--tag);
    background-color: var(--tag);
  }
</style>
