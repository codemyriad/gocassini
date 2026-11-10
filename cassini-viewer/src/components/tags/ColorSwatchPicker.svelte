<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";

  import { LL } from "../../i18n";
  import { TAG_COLORS, type TagColorId } from "../../viewer/tagPalette";
  import { popover, stepIndex } from "./popover";

  export let value: TagColorId;
  // Empty means "Tag colour", in the reader's language.
  export let label = "";
  // Focus returns here on Esc, and a click on it does not count as outside.
  export let anchor: HTMLElement | null = null;

  const COLUMNS = 6;
  const dispatch = createEventDispatcher<{ select: TagColorId; close: void }>();
  let buttons: HTMLButtonElement[] = [];
  let focused = Math.max(0, TAG_COLORS.indexOf(value));

  onMount(() => buttons[focused]?.focus());

  function onKeydown(event: KeyboardEvent) {
    const next = stepIndex(focused, event.key, TAG_COLORS.length, COLUMNS);
    if (next !== null) {
      event.preventDefault();
      buttons[next]?.focus();
    }
  }
</script>

<div
  use:popover={{ anchor, close: () => dispatch("close") }}
  class="tag-popover swatch-picker"
  role="radiogroup"
  aria-label={label || $LL.viewer.browse.tags.colour_picker()}
>
  {#each TAG_COLORS as color, index (color)}
    <button
      bind:this={buttons[index]}
      type="button"
      role="radio"
      aria-checked={color === value}
      aria-label={$LL.viewer.browse.tags.colors[color]()}
      title={$LL.viewer.browse.tags.colors[color]()}
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
