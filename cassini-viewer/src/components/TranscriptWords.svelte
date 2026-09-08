<script lang="ts">
  import type { createWordHighlighter } from "../core/wordHighlight";
  import type { TranscriptWordPart } from "../core/wordInteraction";
  import { formatClockTime } from "../core/transcript";

  export let parts: readonly TranscriptWordPart[];
  export let speakerLabel: string;
  export let highlighter: ReturnType<typeof createWordHighlighter>;
  export let seek: (ms: number) => void;

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
{#each parts as part (part.id)}{part.prefix}{#if part.startMs !== undefined}<button
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
  >{part.text}</button>{:else}<span>{part.text}</span>{/if}{/each}
