<script lang="ts">
  import { createEventDispatcher } from "svelte";

  import TagIcon from "../tags/TagIcon.svelte";
  import { describeMark, type PlacedMark } from "./session";

  export let brackets: readonly { mark: PlacedMark; top: number; height: number }[] = [];
  export let selectedId: string | undefined = undefined;
  export let hoverId: string | null = null;
  export let labels = true;

  const COLUMN_PX = 12;
  const dispatch = createEventDispatcher<{ select: PlacedMark }>();

  $: labelLeft = (Math.max(0, ...brackets.map(({ mark }) => mark.column)) + 1) * COLUMN_PX + 4;
</script>

{#each brackets as { mark, top, height } (mark.item.id)}
  {@const on = mark.item.id === selectedId || mark.item.id === hoverId}
  <button
    type="button"
    class="absolute w-2.5 cursor-pointer rounded-r p-0 before:absolute before:inset-y-0 before:left-0 before:w-1.5 before:rounded-r-[3px] before:border-l-0 before:border-(--tag) before:content-[''] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-(--tag) {on
      ? 'bg-(--tag-bg) before:border-[3.5px] before:border-l-0'
      : 'bg-transparent before:border-[2.5px] before:border-l-0'}"
    data-tag-color={mark.color}
    style:top="{top}px"
    style:height="{height}px"
    style:left="{mark.column * COLUMN_PX}px"
    aria-label={describeMark(mark)}
    title={labels ? undefined : describeMark(mark)}
    on:pointerenter={() => (hoverId = mark.item.id)}
    on:pointerleave={() => (hoverId = null)}
    on:focus={() => (hoverId = mark.item.id)}
    on:blur={() => (hoverId = null)}
    on:click={() => dispatch("select", mark)}
  ></button>
  {#if labels && on}
    <span
      class="pointer-events-none absolute inline-flex max-w-[calc(100%-20px)] items-center gap-1 rounded-full border border-(--tag-border) bg-(--tag-bg) px-1.5 py-px text-[11px] font-semibold whitespace-nowrap text-(--tag)"
      data-tag-color={mark.color}
      style:top="{top}px"
      style:left="{labelLeft}px"
      aria-hidden="true"
    >
      <TagIcon icon={mark.icon} /><span class="truncate">{mark.tag.label}</span>
    </span>
  {/if}
{/each}
