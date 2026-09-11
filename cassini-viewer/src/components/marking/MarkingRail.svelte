<script lang="ts">
  import { createEventDispatcher } from "svelte";

  import { railTicks } from "../../core/marking";
  import { formatClockTime } from "../../core/transcript";
  import type { TagColorId } from "../../viewer/tagPalette";
  import { describeMark, type PlacedMark } from "./session";

  // The whole meeting top to bottom: drag to grab a stretch, click for one turn.
  export let durationMs: number;
  export let marks: readonly PlacedMark[] = [];
  export let selection: { startMs: number; endMs: number } | null = null;
  export let color: TagColorId = "slate";
  export let stops: readonly number[] = [];
  export let current = -1;
  export let playheadMs = 0;
  export let labels = true;

  const CLICK_SLOP_PX = 4;
  const dispatch = createEventDispatcher<{
    grab: { aMs: number; bMs: number; handle: boolean };
    pick: number;
    select: PlacedMark;
  }>();
  let track: HTMLElement;
  // A handle drags one end with the other as the anchor, so crossing it swaps them.
  let drag: { anchorMs: number; y0: number; moved: boolean; handle: boolean } | null = null;

  const pct = (ms: number) => `${(Math.min(Math.max(ms / durationMs, 0), 1) * 100).toFixed(3)}%`;

  function msAt(event: PointerEvent): number {
    const box = track.getBoundingClientRect();
    return Math.min(Math.max((event.clientY - box.top) / box.height, 0), 1) * durationMs;
  }

  function down(event: PointerEvent, handleAnchorMs?: number) {
    if (event.button > 0 || !(durationMs > 0)) {
      return;
    }
    event.preventDefault();
    event.stopPropagation();
    track.setPointerCapture?.(event.pointerId);
    const handle = handleAnchorMs !== undefined;
    drag = { anchorMs: handle ? handleAnchorMs : msAt(event), y0: event.clientY, moved: false, handle };
  }

  function move(event: PointerEvent) {
    if (!drag) {
      return;
    }
    drag.moved ||= Math.abs(event.clientY - drag.y0) > CLICK_SLOP_PX;
    if (drag.moved) {
      dispatch("grab", { aMs: drag.anchorMs, bMs: msAt(event), handle: drag.handle });
    }
  }

  function up() {
    if (drag && !drag.moved && !drag.handle) {
      dispatch("pick", drag.anchorMs);
    }
    drag = null;
  }

  // Not once the frame has taken Enter to confirm a stretch: picking a turn
  // then would swap the stretch being tagged for the one under the playhead.
  function onKeydown(event: KeyboardEvent) {
    if (!event.defaultPrevented && event.target === track && (event.key === "Enter" || event.key === " ")) {
      event.preventDefault();
      event.stopPropagation();
      dispatch("pick", playheadMs);
    }
  }
</script>

<div class="relative h-full select-none" data-tag-color={color}>
  {#if labels}
    {#each railTicks(durationMs) as tick (tick)}
      <span
        class="absolute right-[calc(100%-32px)] -translate-y-1/2 font-mono text-[10px] leading-none whitespace-nowrap text-base-content/50"
        style:top={pct(tick)}
        aria-hidden="true">{formatClockTime(tick)}</span
      >
    {/each}
  {/if}
  <div
    bind:this={track}
    class="absolute inset-y-0 w-3.5 cursor-crosshair touch-none rounded-sm bg-base-content/8 focus-visible:outline-2 focus-visible:outline-offset-3 {labels ? 'left-9' : 'left-0.5'}"
    tabindex="0"
    role="group"
    aria-label="The whole meeting. Drag down it to grab a stretch; click, or press Enter, for one turn."
    on:pointerdown={(event) => down(event)}
    on:pointermove={move}
    on:pointerup={up}
    on:pointercancel={() => (drag = null)}
    on:keydown={onKeydown}
  >
    {#each stops as ms, index}
      <i class="pointer-events-none absolute bg-warning {index === current ? '-inset-x-1 -mt-0.5 h-1' : 'inset-x-px -mt-px h-0.5'}" style:top={pct(ms)}></i>
    {/each}
    <i class="pointer-events-none absolute -inset-x-1 -mt-px h-0.5 bg-primary" style:top={pct(playheadMs)}></i>
    {#if selection}
      <i
        class="pointer-events-none absolute -inset-x-[3px] min-h-1 rounded-xs border-y-[3px] border-(--tag) bg-(--tag-bg)"
        style:top={pct(selection.startMs)}
        style:height={pct(selection.endMs - selection.startMs)}
      ></i>
      {#each [[selection.startMs, selection.endMs], [selection.endMs, selection.startMs]] as [at, anchor] (at)}
        <span
          class="absolute -inset-x-2 -mt-[9px] h-[18px] cursor-ns-resize touch-none"
          style:top={pct(at)}
          aria-hidden="true"
          on:pointerdown={(event) => down(event, anchor)}
        ></span>
      {/each}
    {/if}
  </div>
  <div class="absolute inset-y-0 {labels ? 'left-[54px]' : 'left-5'}" aria-hidden="true">
    {#each marks as mark (mark.item.id)}
      <button
        type="button"
        tabindex="-1"
        class="absolute min-h-[3px] w-[3px] cursor-pointer rounded-xs bg-(--tag) p-0"
        data-tag-color={mark.color}
        style:top={pct(mark.startMs)}
        style:height={pct(mark.endMs - mark.startMs)}
        style:left="{mark.column * 4}px"
        title={describeMark(mark)}
        on:click={() => dispatch("select", mark)}
      ></button>
    {/each}
  </div>
</div>
