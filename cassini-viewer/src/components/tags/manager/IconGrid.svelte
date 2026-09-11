<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";

  import { TAG_ICONS, type TagColorId, type TagIconId } from "../../../viewer/tagPalette";
  import TagIcon from "../TagIcon.svelte";
  import { anchored, isOutside, stepIndex } from "../popover";

  export let value: TagIconId | "";
  export let color: TagColorId;
  export let anchor: HTMLElement | null = null;

  const dispatch = createEventDispatcher<{ select: TagIconId | ""; close: void }>();
  let root: HTMLElement;
  let none: HTMLButtonElement;
  let buttons: HTMLButtonElement[] = [];
  let focused = Math.max(0, TAG_ICONS.indexOf(value as TagIconId));

  onMount(() => (value ? buttons[focused] : none).focus());

  function onKeydown(event: KeyboardEvent) {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      anchor?.focus();
      dispatch("close");
    }
    const next = event.target === none ? null : stepIndex(focused, event.key, TAG_ICONS.length, 8);
    if (next !== null) {
      event.preventDefault();
      buttons[next].focus();
    }
  }

  const name = (icon: TagIconId) => icon.charAt(0).toUpperCase() + icon.slice(1);
</script>

<svelte:window on:pointerdown={(event) => isOutside(event, root, anchor) && dispatch("close")} />

<div bind:this={root} use:anchored={anchor} role="radiogroup" aria-label="Tag icon" tabindex="-1" data-tag-color={color}
  class="z-50 w-[264px] rounded-box border border-base-300 bg-base-100 p-2 shadow-lg" on:keydown={onKeydown}>
  <button bind:this={none} type="button" role="radio" aria-checked={value === ""} on:click={() => dispatch("select", "")}
    class="flex h-8 w-full items-center gap-2 rounded-field px-2 text-sm font-medium hover:bg-base-200 aria-checked:bg-base-200">
    <span class="size-3.5 rounded border border-dashed border-base-content/40" aria-hidden="true"></span>No icon
  </button>
  <div class="mt-1.5 grid grid-cols-8 gap-1 border-t border-base-300 pt-1.5">
    {#each TAG_ICONS as icon, index (icon)}
      <button bind:this={buttons[index]} type="button" role="radio" aria-checked={icon === value} aria-label={name(icon)} title={name(icon)}
        tabindex={index === focused ? 0 : -1} on:click={() => dispatch("select", icon)} on:focus={() => (focused = index)}
        class="grid h-7 place-items-center rounded-field text-(--tag) hover:bg-base-200 aria-checked:bg-(--tag-bg) aria-checked:ring-1 aria-checked:ring-(--tag-border)">
        <TagIcon {icon} size={15} />
      </button>
    {/each}
  </div>
</div>
