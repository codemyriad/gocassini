<script lang="ts">
  import { createEventDispatcher } from "svelte";

  import { railTicks } from "../../core/marking";
  import { formatClockTime } from "../../core/transcript";
  import type { TagColorId } from "../../viewer/tagPalette";
  import { describeMark, type PlacedMark } from "./session";

  // The whole meeting top to bottom: drag to grab a stretch, click for one turn.
  // Without labels, on a narrow screen where the reader's own text selection
  // does the grabbing, it is a map instead: where each tagged section lies,
  // tapped to go to it.
  export let durationMs: number;
  export let marks: readonly PlacedMark[] = [];
  export let selection: { startMs: number; endMs: number } | null = null;
  export let color: TagColorId = "slate";
  export let stops: readonly number[] = [];
  export let current = -1;
  export let playheadMs = 0;
  // The stretch of the meeting the transcript is showing right now.
  export let visible: { startMs: number; endMs: number } | null = null;
  export let labels = true;

  const CLICK_SLOP_PX = 4;
  const dispatch = createEventDispatcher<{
    grab: { aMs: number; bMs: number; handle: boolean };
    pick: number;
    go: number;
    select: PlacedMark;
  }>();
  let track: HTMLElement;
  // A handle drags one end with the other as the anchor, so crossing it swaps them.
  let drag: { anchorMs: number; y0: number; moved: boolean; handle: boolean } | null = null;

  export function focus() {
    track.focus({ preventScroll: true });
  }

  $: map = !labels;
  $: columns = Math.max(0, ...marks.map((mark) => mark.column)) + 1;

  const pct = (ms: number) => `${(Math.min(Math.max(ms / durationMs, 0), 1) * 100).toFixed(3)}%`;

  function msAt(event: MouseEvent): number {
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

<!-- What the transcript is showing, as a soft box: with the playhead beside
     it, whether the reader is where the audio is reads at a glance. -->
{#snippet seen()}
  {#if visible && durationMs > 0}
    <i
      class="mr-seen"
      style:top={pct(visible.startMs)}
      style:height={pct(Math.max(0, visible.endMs - visible.startMs))}
    ></i>
  {/if}
{/snippet}

<div class="relative h-full select-none" data-tag-color={color}>
  {#if labels}
    {#each railTicks(durationMs) as tick (tick)}
      <span
        class="absolute right-[calc(100%-26px)] -translate-y-1/2 font-mono text-[10px] leading-none whitespace-nowrap text-base-content/50"
        style:top={pct(tick)}
        aria-hidden="true">{formatClockTime(tick)}</span
      >
    {/each}
  {/if}
  {#if map}
    <!-- Tapping between sections goes to that moment in the text; the audio
         stays where it is, which the player owns. -->
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_noninteractive_element_interactions -->
    <div
      bind:this={track}
      class="absolute inset-y-0 left-0.5 w-3.5 rounded-sm bg-base-content/8"
      role="group"
      aria-label="Tagged sections across the whole meeting"
      on:click={(event) => durationMs > 0 && dispatch("go", msAt(event))}
    >
      {@render seen()}
      {#each stops as ms, index}
        <i class="pointer-events-none absolute bg-warning {index === current ? '-inset-x-1 -mt-0.5 h-1' : 'inset-x-px -mt-px h-0.5'}" style:top={pct(ms)}></i>
      {/each}
      {#each marks as mark (mark.item.id)}
        <button
          type="button"
          class="mr-seg absolute cursor-pointer p-0"
          data-tag-color={mark.color}
          style:top={pct(mark.startMs)}
          style:height={pct(mark.endMs - mark.startMs)}
          style:left="calc({(mark.column / columns) * 100}% + 1px)"
          style:width="calc({100 / columns}% - 2px)"
          aria-label={`${describeMark(mark)}. Go to it.`}
          on:click|stopPropagation={() => dispatch("select", mark)}
        ></button>
      {/each}
      {#if selection}
        <i
          class="pointer-events-none absolute -inset-x-[3px] min-h-1 rounded-xs border-y-[3px] border-(--tag)"
          style:top={pct(selection.startMs)}
          style:height={pct(selection.endMs - selection.startMs)}
        ></i>
      {/if}
      <i class="mr-playhead" style:top={pct(playheadMs)}></i>
    </div>
  {:else}
  <div
    bind:this={track}
    class="absolute inset-y-0 left-9 w-3.5 cursor-crosshair touch-none rounded-sm bg-base-content/8 focus-visible:outline-2 focus-visible:outline-offset-3"
    tabindex="0"
    role="group"
    aria-label="The whole meeting. Drag down it to grab a section; click, or press Enter, for one turn."
    on:pointerdown={(event) => down(event)}
    on:pointermove={move}
    on:pointerup={up}
    on:pointercancel={() => (drag = null)}
    on:keydown={onKeydown}
  >
    {@render seen()}
    {#each stops as ms, index}
      <i class="pointer-events-none absolute bg-warning {index === current ? '-inset-x-1 -mt-0.5 h-1' : 'inset-x-px -mt-px h-0.5'}" style:top={pct(ms)}></i>
    {/each}
    {#if selection}
      <i
        class="pointer-events-none absolute inset-x-0 min-h-1 bg-(--tag-bg)"
        style:top={pct(selection.startMs)}
        style:height={pct(selection.endMs - selection.startMs)}
      ></i>
    {/if}
    <!-- The playhead is the player's own thumb, where the audio is; a
         selection's ends are handles, drawn like the pins in the text — a line
         in the tag's colour with a dot past the rail's edge — so the two never
         read as the same thing, and the handles lie over the playhead where
         they meet. -->
    <i class="mr-playhead" style:top={pct(playheadMs)}></i>
    {#if selection}
      {#each [[selection.startMs, selection.endMs], [selection.endMs, selection.startMs]] as [at, anchor] (at)}
        <span
          class="absolute -inset-x-2 -mt-[9px] h-[18px] cursor-ns-resize touch-none"
          style:top={pct(at)}
          aria-hidden="true"
          on:pointerdown={(event) => down(event, anchor)}
        >
          <i class="pointer-events-none absolute inset-x-[5px] top-2 h-0.5 bg-(--tag)"></i>
          <i class="pointer-events-none absolute top-1/2 right-0 size-2.5 -translate-y-1/2 rounded-full bg-(--tag)"></i>
        </span>
      {/each}
    {/if}
  </div>
  <!-- No coloured strips beside it here: on a wide screen a tag is drawn
       once, in the bracket column on the right of the transcript, and a second
       set of marks in the margin read as a different thing to work out. -->
  {/if}
</div>

<style>
  /* A short section is still something to see and to hit: a few pixels tall,
     with a target wider and taller than it is drawn. */
  .mr-seg {
    min-height: 4px;
    background-color: var(--tag);
    border: 0;
    border-radius: 2px;
  }
  .mr-seen {
    position: absolute;
    inset-inline: -3px;
    min-height: 6px;
    pointer-events: none;
    background-color: color-mix(in oklch, var(--color-base-content) 6%, transparent);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 35%, transparent);
    border-radius: 4px;
  }
  /* The player's scrubber thumb in miniature: a ring of the page's ink. */
  .mr-playhead {
    position: absolute;
    left: 50%;
    width: 12px;
    height: 12px;
    translate: -50% -50%;
    pointer-events: none;
    background-color: var(--color-base-100);
    border: 3px solid var(--color-base-content);
    border-radius: 999px;
  }
  .mr-seg::before {
    content: "";
    position: absolute;
    inset: -5px -4px;
  }
</style>
