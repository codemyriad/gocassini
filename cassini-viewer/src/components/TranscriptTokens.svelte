<script lang="ts">
  import type { Readable } from "svelte/store";

  import type { DisplayTranscriptToken } from "../core/types";

  /**
   * One block's words, and the only part of the transcript that knows which of
   * them is lit (D-692).
   *
   * WHY THIS IS ITS OWN COMPONENT. The markup below used to sit inline in
   * MeetingView, where each word's class expression read a single component-wide
   * `Map` of active tokens. Legacy Svelte compares objects by identity, so
   * replacing that map — which the playhead does on every change — marked,
   * scheduled and dirty-checked EVERY rendered word in the meeting to discover
   * that one of them had changed. On the largest published meeting that is 7,318
   * effects walked to move a single highlight. Measured against a build that is
   * identical except that these words read one shared map instead of a store per
   * block: 12.3% renderer busy and 340 ms of script against 9.1% and 109 ms.
   *
   * A store passed as a prop fixes it at the root rather than making the walk
   * cheaper. `$activeToken` subscribes through `store_get`, which allocates ONE
   * source per component instance, so this block's words are the only reactions
   * that source has. The playhead writes just the one or two blocks that are
   * actually sounding; every other block in the meeting is never marked, never
   * scheduled and never dirty-checked. The work becomes proportional to what
   * changed instead of to what is on screen.
   *
   * RENDERS INLINE, with no wrapper element. Since D-693 a turn is one
   * paragraph and `blockProse` is called inside it, so a block element here
   * would close that paragraph early and split the turn.
   *
   * The rendered DOM is otherwise what MeetingView produced before — same
   * element order, same handlers, and critically the same `bg-primary` plus
   * `ring-primary` pairing, which `app.css` keys the Nextcloud embedded build's
   * text colour off (D-414). The benchmark harness selects these buttons by
   * `.inline`.
   *
   * NO CSS TRANSITION ON THE HIGHLIGHT, deliberately. These buttons carried
   * `transition duration-150`, so lighting a word animated its background AND
   * the ring's box-shadow. A word lasts about 250 ms, so at any moment two of
   * them were mid-transition and the highlight never actually settled — it
   * lagged the audio it exists to track. It was also, by a wide margin, the
   * most expensive thing left on the playback path: with everything else in
   * this change already landed, removing just this one utility took the largest
   * published meeting from 33.4% renderer busy to 9.1%, and the p90 meeting from
   * 19.7% to 9.8%. Narrowing it to a colours-only transition rather than
   * removing it recovered less than half of that, so the background fade was
   * most of the cost and the ring only part of it. An instant highlight is both
   * cheaper and more honest about where the playhead is.
   *
   * (Class names are spelled carefully in this file's prose: Tailwind v4
   * content-scans source text, so naming a utility in a comment is enough to
   * put its rule back into the bundle.)
   */
  export let tokens: readonly DisplayTranscriptToken[];
  /** Seek target for a token that somehow carries no start of its own. */
  export let fallbackStartMs: number;
  /** The token currently being spoken in THIS block, or null. */
  export let activeToken: Readable<DisplayTranscriptToken | null>;
  export let seek: (ms: number) => void;
</script>

{#each tokens as token}{#if token.spaceBefore}{' '}{/if}{#if token.startMs !== undefined && token.endMs !== undefined}<button
        class="inline p-0 border-0 rounded text-[1.06rem] leading-[1.72] cursor-pointer {token ===
          $activeToken
          ? 'bg-primary ring-1 ring-primary'
          : 'bg-transparent hover:bg-primary/60'} {token.alignment === 'interpolated'
          ? 'border-b border-dashed border-warning/60'
          : ''}"
        on:click={() => seek(token.startMs ?? fallbackStartMs)}
        type="button"
      >{token.text}</button>{:else}<span
        class="inline rounded text-[1.06rem] leading-[1.72] {token.kind ===
        'word'
          ? 'text-base-content/70'
          : 'text-base-content'}"
      >{token.text}</span>{/if}{/each}
