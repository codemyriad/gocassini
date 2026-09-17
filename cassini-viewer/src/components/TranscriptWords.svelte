<script lang="ts">
  import type { createWordHighlighter } from "../core/wordHighlight";
  import type { TranscriptWordPart } from "../core/wordInteraction";
  import { formatClockTime } from "../core/transcript";
  import { describeMark, type PlacedMark } from "./marking/session";
  import TagChip from "./tags/TagChip.svelte";

  export let parts: readonly TranscriptWordPart[];
  export let speakerLabel: string;
  export let highlighter: ReturnType<typeof createWordHighlighter>;
  export let seek: (ms: number) => void;
  // Tagged sections that begin at a word, drawn in front of it; only where the
  // transcript has no column of its own for them.
  export let chips: ReadonlyMap<string, readonly PlacedMark[]> | null = null;
  export let openMark: ((mark: PlacedMark) => void) | undefined = undefined;

  // Each word subscribes once. Playback touches only entering/leaving words,
  // never a component-wide reactive map or every word in a long transcript.
  // This deliberately adds no clock, animation, or background transition (D-692).
  function trackWord(node: HTMLButtonElement, id: string) {
    const apply = (active: boolean) => node.setAttribute("data-active", String(active));
    let unsubscribe = highlighter.subscribe(id, apply);
    return {
      update(nextId: string) {
        unsubscribe();
        unsubscribe = highlighter.subscribe(nextId, apply);
      },
      destroy() { unsubscribe(); },
    };
  }
</script>

<!-- Inline only: both ordinary speech and interjections live in one paragraph.
     Keep separators outside buttons so hover paints the word, not its gap. -->
{#each parts as part (part.id)}{#if part.prefix}<span class="cassini-gap" data-gap-for={part.id}>{part.prefix}</span>{/if}{#each chips?.get(part.id) ?? [] as mark (mark.item.id)}<button
    type="button"
    class="cassini-tag-start"
    aria-label={`${describeMark(mark)}. Open it.`}
    on:click={() => openMark?.(mark)}
  ><TagChip label={mark.tag.label} color={mark.color} icon={mark.icon} /></button>{/each}{#if part.startMs !== undefined}<button
    type="button"
    class="cassini-word"
    class:cassini-word-interpolated={part.alignment === "interpolated"}
    data-word-id={part.id}
    data-start-ms={part.startMs}
    data-end-ms={part.endMs}
    data-speaker={speakerLabel}
    data-active="false"
    use:trackWord={part.id}
    aria-label={`${part.text} — seek to ${formatClockTime(part.startMs)}, ${speakerLabel}`}
    title={part.alignment === "interpolated" ? "Estimated word timing" : undefined}
    on:click={() => seek(part.startMs!)}
  >{part.text}</button>{:else}<span data-word-id={part.id}>{part.text}</span>{/if}{/each}

<style>
  /* In the line, kept out of a copied passage: it names the passage, it is not
     part of what was said. */
  .cassini-tag-start {
    display: inline-block;
    margin-right: 4px;
    padding: 0;
    vertical-align: 1px;
    line-height: 1;
    cursor: pointer;
    user-select: none;
    background: none;
    border: 0;
  }
</style>
