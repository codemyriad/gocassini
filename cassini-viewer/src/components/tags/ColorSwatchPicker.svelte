<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";

  import { TAG_COLORS, colorName, type TagColorId } from "../../viewer/tagPalette";
  import { isOutside, stepIndex } from "./popover";

  export let value: TagColorId;
  export let label = "Tag colour";
  // Focus returns here on Esc, and a click on it does not count as outside.
  export let anchor: HTMLElement | null = null;

  const COLUMNS = 6;
  const dispatch = createEventDispatcher<{ select: TagColorId; close: void }>();
  let root: HTMLElement;
  let buttons: HTMLButtonElement[] = [];
  let focused = Math.max(0, TAG_COLORS.indexOf(value));

  onMount(() => buttons[focused]?.focus());

  function onKeydown(event: KeyboardEvent) {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      anchor?.focus();
      dispatch("close");
      return;
    }
    const next = stepIndex(focused, event.key, TAG_COLORS.length, COLUMNS);
    if (next !== null) {
      event.preventDefault();
      buttons[next]?.focus();
    }
  }
</script>

<svelte:window on:pointerdown={(event) => isOutside(event, root, anchor) && dispatch("close")} />

<div bind:this={root} class="swatch-picker" role="radiogroup" aria-label={label}>
  {#each TAG_COLORS as color, index (color)}
    <button
      bind:this={buttons[index]}
      type="button"
      role="radio"
      aria-checked={color === value}
      aria-label={colorName(color)}
      title={colorName(color)}
      tabindex={index === focused ? 0 : -1}
      data-tag-color={color}
      on:click={() => dispatch("select", color)}
      on:focus={() => (focused = index)}
      on:keydown={onKeydown}
    >
      <span aria-hidden="true"></span>
    </button>
  {/each}
</div>

<style>
  .swatch-picker {
    display: grid;
    grid-template-columns: repeat(6, 28px);
    gap: 4px;
    padding: 8px;
    background: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 0.5rem);
    box-shadow:
      0 2px 6px oklch(0% 0 0 / 0.1),
      0 12px 32px oklch(0% 0 0 / 0.16);
  }
  button {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    padding: 0;
    background: none;
    border: 0;
    border-radius: 999px;
    cursor: pointer;
  }
  button span {
    width: 18px;
    height: 18px;
    border-radius: 999px;
    background: var(--tag);
    transition: transform 0.1s ease;
  }
  button:hover span {
    transform: scale(1.12);
  }
  button[aria-checked="true"] {
    box-shadow: inset 0 0 0 2px var(--tag);
  }
</style>
