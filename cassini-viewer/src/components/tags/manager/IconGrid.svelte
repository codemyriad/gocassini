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
    class="flex h-8 w-full items-center gap-2 rounded-field px-2 text-sm font-medium hover:bg-base-200 aria-checked:bg-base-200">
    <span class="size-3.5 rounded border border-dashed border-base-content/40" aria-hidden="true"></span>No icon
  </button>
  <div class="mt-1.5 grid grid-cols-8 gap-1 border-t border-base-300 pt-1.5">
    {#each TAG_ICONS as icon, index (icon)}
      <button bind:this={buttons[index]} type="button" role="radio" aria-checked={icon === value} aria-label={styleName(icon)} title={styleName(icon)}
        tabindex={index === focused ? 0 : -1} on:click={() => dispatch("select", icon)} on:focus={() => (focused = index)}
        class="grid h-7 place-items-center rounded-field text-(--tag) hover:bg-base-200 aria-checked:bg-(--tag-bg) aria-checked:ring-1 aria-checked:ring-(--tag-border)">
        <TagIcon {icon} size={15} />
      </button>
    {/each}
  </div>
</div>
