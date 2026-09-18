<script lang="ts">
  import Kbd from "./Kbd.svelte";

  // One button of a small panel: the label at its start and the key that does
  // the same at its end, at one size and one alignment whatever its tone.
  // `ink` is filled with the page's ink, like the player's play button;
  // `plain` is outlined in it; `tag` in the colour of the tag on the element
  // (data-tag-color).
  export let tone: "ink" | "primary" | "neutral" | "error" | "plain" | "tag" = "neutral";
  // The key for the same action; left out where there are no keys.
  export let key = "";
  // Fills the width it is given, as in a stacked panel, rather than fitting
  // its label.
  export let block = false;
  // Tighter at the sides, for two to a narrow row.
  export let compact = false;
  export let disabled = false;
  export let element: HTMLButtonElement | undefined = undefined;

  const tones = {
    ink: "ab-ink",
    primary: "btn-primary",
    neutral: "btn-neutral",
    error: "btn-error",
    plain: "btn-outline ab-plain",
    tag: "btn-outline ab-tag",
  } as const;
</script>

<button
  bind:this={element}
  type="button"
  class="ab btn btn-sm {tones[tone]}"
  class:ab-block={block}
  class:ab-compact={compact}
  {disabled}
  {...$$restProps}
  on:click
>
  <span class="ab-label"><slot /></span>
  {#if key}<Kbd>{key}</Kbd>{/if}
</button>

<style>
  .ab {
    flex-wrap: nowrap;
    gap: 5px;
    min-width: 0;
    padding-inline: 8px;
  }
  /* Two to a row, a label and its key can outgrow the half they are given; the
     label gives way, and the key keeps its size, rather than one lying on the
     other. */
  .ab :global(.cassini-kbd) {
    flex: none;
  }
  .ab-compact {
    gap: 4px;
    padding-inline: 6px;
  }
  .ab-compact :global(.cassini-kbd) {
    padding-inline: 3px;
  }
  .ab-block {
    width: 100%;
    justify-content: space-between;
  }
  .ab-label {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
  }
  .ab-ink {
    --btn-color: var(--color-base-content);
    --btn-fg: var(--color-base-100);
  }
  .ab-plain {
    color: var(--color-base-content);
    border-color: color-mix(in oklch, var(--color-base-content) 35%, transparent);
  }
  .ab-plain:hover {
    color: var(--color-base-content);
    background-color: color-mix(in oklch, var(--color-base-content) 10%, transparent);
    border-color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .ab-tag {
    color: var(--tag);
    border-color: color-mix(in oklch, var(--tag) 55%, transparent);
  }
  .ab-tag:hover {
    color: var(--tag);
    background-color: color-mix(in oklch, var(--tag) 14%, transparent);
    border-color: var(--tag);
  }
</style>
