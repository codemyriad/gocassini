<script module lang="ts">
  // How far a tag drops for each section it overlaps, so two tags that would
  // stand in the same place, or stick at the same line, both show.
  export const LABEL_STEP = 22;
</script>

<script lang="ts">
  import { createEventDispatcher } from "svelte";

  import TagChip from "../tags/TagChip.svelte";
  import { describeMark, type PlacedMark } from "./session";

  export let brackets: readonly { mark: PlacedMark; top: number; height: number }[] = [];
  export let selectedId: string | undefined = undefined;
  export let hoverId: string | null = null;
  // Where the sticky bars above this end, so a tag can hold that line while
  // its own section scrolls past.
  export let stickTop = 0;

  const dispatch = createEventDispatcher<{ select: PlacedMark }>();

  const step = 12;
  $: labelLeft = (Math.max(0, ...brackets.map(({ mark }) => mark.column)) + 1) * step + 22;
</script>

{#each brackets as { mark, top, height } (mark.item.id)}
  {@const on = mark.item.id === selectedId || mark.item.id === hoverId}
  <!-- A bracket, not a bar: the arms reach further in at the top and bottom so
       the shape says where a tagged section starts and ends, and the wider
       target makes it something a pointer can find. -->
  <button
    type="button"
    class="mb-bracket absolute cursor-pointer p-0 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-(--tag)"
    class:on
    data-tag-color={mark.color}
    style:top="{top}px"
    style:height="{height}px"
    style:left="{mark.column * step}px"
    aria-label={describeMark(mark)}
    on:pointerenter={() => (hoverId = mark.item.id)}
    on:pointerleave={() => (hoverId = null)}
    on:focus={() => (hoverId = mark.item.id)}
    on:blur={() => (hoverId = null)}
    on:click={() => dispatch("select", mark)}
  ></button>
  <!-- The tag is the point of the bracket, so it is always there rather than
       waiting for a pointer to find the shape first. It rides its own section:
       it holds the line under the bars while any part of that section is on
       screen, and leaves with it. -->
  <div
    class="mb-label-track pointer-events-none absolute"
    style:top="{top + mark.column * LABEL_STEP}px"
    style:height="{Math.max(0, height - mark.column * LABEL_STEP)}px"
    style:left="{labelLeft}px"
  >
    <button
      type="button"
      tabindex="-1"
      class="mb-label pointer-events-auto sticky flex max-w-full cursor-pointer"
      class:on
      style:top="{stickTop + mark.column * LABEL_STEP}px"
      aria-hidden="true"
      on:pointerenter={() => (hoverId = mark.item.id)}
      on:pointerleave={() => (hoverId = null)}
      on:click={() => dispatch("select", mark)}
    >
      <TagChip label={mark.tag.label} color={mark.color} icon={mark.icon} />
    </button>
  </div>
{/each}

<style>
  /* Drawn in CSS rather than utilities: three edges of one box, with arms that
     grow when the bracket is the one in hand. */
  .mb-bracket {
    /* The arms ARE the box's top and bottom borders, so the shape and the hit
       area are the same 22 pixels: drawn arms that the pointer passed through
       made the bracket look bigger than it could be hovered. */
    width: 22px;
    background: none;
    border: 2px solid var(--tag);
    border-left: 0;
    border-radius: 0 4px 4px 0;
  }
  /* Hover changes colour, never geometry: growing the arms and the border
     moved the shape under the pointer, which left and re-entered it. */
  .mb-bracket.on {
    background-color: var(--tag-bg);
  }

  .mb-label-track {
    right: 0;
  }
  .mb-label {
    padding: 0;
    background: none;
    border: 0;
    opacity: 0.85;
  }
  .mb-label.on {
    opacity: 1;
  }
</style>
