<script lang="ts">
  import { createEventDispatcher } from "svelte";

  import { formatClockTime } from "../../core/transcript";
  import TagChip from "../tags/TagChip.svelte";
  import { describeMark, type PlacedMark } from "./session";

  export let marks: readonly PlacedMark[] = [];

  const dispatch = createEventDispatcher<{ jump: PlacedMark }>();
</script>

<div class="max-h-[50vh] overflow-y-auto rounded-box border border-base-300 bg-base-100 p-1.5" role="region" aria-label="Marks in this meeting">
  {#if marks.length === 0}
    <p class="p-2 text-xs text-base-content/60">No marks yet. Drag down the rail beside the transcript to grab a stretch.</p>
  {:else}
    <ul class="grid gap-0.5">
      {#each marks as mark (mark.item.id)}
        <li>
          <button
            type="button"
            class="flex w-full min-w-0 items-center gap-2 rounded-field px-2 py-1.5 text-left hover:bg-base-200"
            data-tag-color={mark.color}
            aria-label={`${describeMark(mark)}. Jump to it.`}
            on:click={() => dispatch("jump", mark)}
          >
            <span class="w-1 self-stretch rounded-full bg-(--tag)" aria-hidden="true"></span>
            <TagChip label={mark.tag.label} color={mark.color} icon={mark.icon} />
            <span class="font-mono text-xs tabular-nums whitespace-nowrap">
              {formatClockTime(mark.startMs)} → {formatClockTime(mark.endMs)}
            </span>
            <span class="ml-auto min-w-0 truncate text-xs text-base-content/60">{mark.item.actor.id}</span>
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>
